// Package httpapi exposes the authenticated panel boundary.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"instadeck/internal/config"
	"instadeck/internal/fleet"
	"instadeck/internal/identity"
	"instadeck/internal/operation"
	"instadeck/internal/store/sqlite"
	"instadeck/internal/terminal"
)

// AuditStore appends events and reads bounded cursor pages.
type AuditStore interface {
	audit.Writer
	ReadAuditEvents(context.Context, int64, int) ([]sqlite.AuditRecord, error)
}

// Dependencies contains the assembled services and public origin.
type Dependencies struct {
	Config             config.Config
	SetupStore         SetupStore
	SetupOutput        io.Writer
	Identity           *identity.Service
	Accounts           *account.Service
	Fleet              *fleet.Service
	Operations         *operation.Service
	Terminal           *terminal.Manager
	Audit              AuditStore
	Assets             fs.FS
	Logger             *slog.Logger
	AbsoluteSessionTTL time.Duration
}

// API owns the closed public route set and authenticated business group.
type API struct {
	dependencies Dependencies
	router       *chi.Mux
	policy       atomic.Pointer[originPolicy]
	setup        setupState
	authMutex    sync.Mutex
	// reauthOff is inverted so the zero value keeps the protection on.
	reauthOff atomic.Bool
	// settingsMutex keeps a switch's stored value and its effect in step.
	settingsMutex sync.Mutex
}

type contextKey int

const (
	requestIDKey contextKey = iota
	sessionKey
)

// New constructs a default-deny router. Assets are relative to the dist directory.
func New(dependencies Dependencies) (*API, error) {
	if dependencies.Identity == nil || dependencies.Accounts == nil || dependencies.Fleet == nil || dependencies.Operations == nil || dependencies.Terminal == nil || dependencies.Audit == nil || dependencies.AbsoluteSessionTTL <= 0 {
		return nil, errors.New("HTTP dependencies required")
	}
	validated, err := config.FromEnv(func(name string) string {
		switch name {
		case "INSTADECK_ORIGIN":
			return dependencies.Config.Origin
		case "INSTADECK_MASTER_KEY_FILE":
			return dependencies.Config.MasterKeyPath
		case "INSTADECK_TRUSTED_PROXY":
			return dependencies.Config.TrustedProxy
		case "INSTADECK_LISTEN":
			return dependencies.Config.ListenAddress
		}
		return ""
	})
	if err != nil {
		return nil, errors.New("invalid HTTP configuration")
	}
	dependencies.Config.Origin = validated.Origin
	if dependencies.Logger == nil {
		dependencies.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	api := &API{dependencies: dependencies, router: chi.NewRouter()}
	api.setOrigin(validated.Origin)
	if err := api.initializeSetup(); err != nil {
		return nil, err
	}
	api.router.Use(api.boundary)
	api.router.Route("/api/v1", func(router chi.Router) {
		router.Get("/setup/status", api.setupStatus)
		router.Post("/setup/start", api.setupStart)
		router.Post("/setup/complete", api.setupComplete)
		router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
		router.Group(func(public chi.Router) {
			public.Use(api.origin)
			public.Post("/auth/password", api.password)
			public.Post("/auth/totp", api.complete)
			public.Post("/auth/recovery", api.complete)
		})
		router.Group(func(business chi.Router) {
			business.Use(api.authenticate, api.origin, api.csrf)
			api.registerBusiness(business)
		})
	})
	api.router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, r, 404, "not_found")
			return
		}
		api.assets(w, r)
	})
	api.router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { fail(w, r, 405, "method_not_allowed") })
	return api, nil
}

// Routes exposes the registered route tree for automatic authorization checks.
func (api *API) Routes() chi.Routes { return api.router }

// ServeHTTP serves the assembled handler.
func (api *API) ServeHTTP(w http.ResponseWriter, r *http.Request) { api.router.ServeHTTP(w, r) }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, r *http.Request, status int, code string) {
	message := "Request rejected."
	switch code {
	case "unauthorized", "authentication_failed":
		message = "Authentication required or invalid."
	case "reauthentication_required":
		message = "Recent authentication required."
	case "origin_rejected":
		message = "Origin rejected."
	case "csrf_rejected":
		message = "CSRF token rejected."
	case "weak_password":
		message = "Password must contain at least 12 characters and at most 1024 bytes."
	case "password_unchanged":
		message = "New password must differ from the current password."
	case "totp_pending_missing":
		message = "Pending authenticator is missing or expired."
	case "cannot_revoke_current":
		message = "Use logout to revoke the current session."
	case "invalid_request":
		message = "Invalid request."
	case "not_found":
		message = "Resource not found."
	case "unavailable", "audit_unavailable":
		message = "Service unavailable."
	case "frontend_not_built":
		message = "Frontend assets have not been built."
	case "confirmation_mismatch":
		message = "Machine name confirmation does not match."
	case "capability_denied", "missing_scope":
		message = "Credential lacks the required capability."
	case "machine_not_visible":
		message = "Machine is not visible through this credential."
	case "invalid_body":
		message = "Invalid operation body."
	case "invalid_intent":
		message = "Invalid operation intent."
	case "request_conflict":
		message = "Client request ID conflicts with an existing action."
	case "credential_unavailable", "disabled", "replaced":
		message = "Credential is unavailable."
	case "forbidden":
		message = "Credential access is forbidden."
	case "rate_limited":
		message = "Credential requests are rate limited."
	case "invalid_identity":
		message = "Credential identity is invalid."
	case "deleted":
		message = "Credential has been deleted."
	case "invalid_name":
		message = "Credential name is invalid."
	case "storage_unavailable":
		message = "Storage is unavailable."
	case "random_unavailable":
		message = "Random generation is unavailable."
	case "encryption_failed", "decryption_failed":
		message = "Credential encryption is unavailable."
	case "timeout":
		message = "Credential request timed out."
	case "canceled":
		message = "Credential request was canceled."
	case "pagination_limit":
		message = "Credential pagination limit reached."
	case "operation_rejected":
		message = "Operation rejected."
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": status >= 500, "requestId": r.Context().Value(requestIDKey)}})
}

func decode(w http.ResponseWriter, r *http.Request, value any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(w, r, 400, "invalid_request")
		return false
	}
	return true
}

func (api *API) boundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var identifier [16]byte
		if _, err := rand.Read(identifier[:]); err != nil {
			fail(w, r, 503, "unavailable")
			return
		}
		requestID := hex.EncodeToString(identifier[:])
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, requestID))
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("Cache-Control", "no-store")
		// xterm measures and positions terminal cells with inline styles.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'; style-src 'self' 'unsafe-inline'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if api.policy.Load().secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		started := time.Now()
		defer func() {
			if recover() != nil {
				fail(wrapped, r, 500, "internal_error")
			}
			status := wrapped.Status()
			if status == 0 {
				status = 200
			}
			api.dependencies.Logger.Info("request", "method", r.Method, "route", chi.RouteContext(r.Context()).RoutePattern(), "status", status, "duration", time.Since(started), "requestId", requestID)
		}()
		if api.policy.Load().origin == "" && !strings.HasPrefix(r.URL.Path, "/api/v1/setup/") && (r.Method != "GET" || r.URL.Path == "/api/v1/terminal/ws") {
			fail(wrapped, r, 403, "origin_rejected")
			return
		}
		next.ServeHTTP(wrapped, r)
	})
}

func (api *API) origin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path == "/api/v1/terminal/ws" {
			if values := r.Header.Values("Origin"); api.policy.Load().origin == "" || len(values) != 1 || values[0] != api.policy.Load().origin {
				fail(w, r, 403, "origin_rejected")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (api *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := api.token(r, api.policy.Load().sessionCookie)
		session, err := api.dependencies.Identity.Session(r.Context(), token)
		if token == "" || err != nil {
			fail(w, r, 401, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, session)))
	})
}

func (api *API) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && !identity.CheckCSRF(currentSession(r), r.Header.Get("X-CSRF-Token")) {
			fail(w, r, 403, "csrf_rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func currentSession(r *http.Request) identity.Session {
	session, _ := r.Context().Value(sessionKey).(identity.Session)
	return session
}

func (api *API) recent(w http.ResponseWriter, r *http.Request) bool {
	if !api.dependencies.Identity.RecentlyAuthenticated(currentSession(r)) {
		fail(w, r, 403, "reauthentication_required")
		return false
	}
	return true
}

// sensitive applies the optional second confirmation. Credential changes such as
// passwords and session revocation keep their own checks whatever this says.
func (api *API) sensitive(w http.ResponseWriter, r *http.Request) bool {
	return api.reauthOff.Load() || api.recent(w, r)
}

func (api *API) token(r *http.Request, name string) string {
	cookies := r.CookiesNamed(name)
	if len(cookies) != 1 || len(cookies[0].Value) != 43 {
		return ""
	}
	return cookies[0].Value
}

func (api *API) cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: api.policy.Load().secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

// ClientIP trusts only the rightmost forwarded hop from a local proxy connection.
func ClientIP(r *http.Request) string { return clientIP(r, false) }
func (api *API) clientIP(r *http.Request) string {
	return clientIP(r, api.dependencies.Config.TrustedProxy == "private")
}
func clientIP(r *http.Request, private bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "invalid"
	}
	address = address.Unmap()
	if address.IsLoopback() || private && address.IsPrivate() {
		values := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
		if values != "" {
			hops := strings.Split(values, ",")
			forwarded, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1]))
			if err != nil {
				return "invalid"
			}
			return forwarded.Unmap().String()
		}
	}
	return address.Unmap().String()
}

func (api *API) event(r *http.Request, kind, result, credentialID, machineID string) error {
	requestID, _ := r.Context().Value(requestIDKey).(string)
	return api.dependencies.Audit.WriteEvent(r.Context(), audit.Event{Time: time.Now().UTC(), Type: kind, SubjectID: currentSession(r).AdministratorID, Result: result, CredentialID: credentialID, MachineID: machineID, RequestID: requestID, SourceAddress: api.clientIP(r)})
}

func (api *API) requireAudit(w http.ResponseWriter, r *http.Request, kind, credentialID, machineID string) bool {
	if api.event(r, kind, "intent", credentialID, machineID) != nil {
		fail(w, r, 503, "audit_unavailable")
		return false
	}
	return true
}

func (api *API) assets(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(w, r, 405, "method_not_allowed")
		return
	}
	if api.dependencies.Assets == nil {
		fail(w, r, 503, "frontend_not_built")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if !fs.ValidPath(name) || strings.HasPrefix(name, ".") {
		name = "index.html"
	}
	if info, err := fs.Stat(api.dependencies.Assets, name); err != nil || info.IsDir() {
		name = "index.html"
	}
	if _, err := fs.Stat(api.dependencies.Assets, "index.html"); err != nil {
		fail(w, r, 503, "frontend_not_built")
		return
	}
	copy := r.Clone(r.Context())
	copy.URL.Path = "/" + name
	if name == "index.html" {
		content, err := fs.ReadFile(api.dependencies.Assets, name)
		if err != nil {
			fail(w, r, 503, "frontend_not_built")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != "HEAD" {
			_, _ = w.Write(content)
		}
		return
	}
	http.FileServer(http.FS(api.dependencies.Assets)).ServeHTTP(w, copy)
}
