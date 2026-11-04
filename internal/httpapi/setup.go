package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"instadeck/internal/config"
	"instadeck/internal/identity"
	"instadeck/internal/store/sqlite"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// SetupStore persists initialization while keeping pending secrets in memory.
type SetupStore interface {
	identity.SetupRepository
	HasAdministrator(context.Context) (bool, error)
	PanelOrigin(context.Context) (string, error)
}

type originPolicy struct {
	origin, sessionCookie, challengeCookie string
	secure                                 bool
}

func (api *API) setOrigin(origin string) {
	policy := &originPolicy{origin: origin, sessionCookie: "instadeck_session", challengeCookie: "instadeck_challenge", secure: strings.HasPrefix(origin, "https://")}
	if policy.secure {
		policy.sessionCookie = "__Host-" + policy.sessionCookie
		policy.challengeCookie = "__Host-" + policy.challengeCookie
	}
	api.policy.Store(policy)
}

type setupPending struct {
	cookieHash [32]byte
	username   string
	password   identity.PasswordHash
	secret     []byte
	expires    time.Time
	attempts   int
	origin     string
}

type setupState struct {
	mutex   sync.Mutex
	code    []byte
	pending *setupPending
	limiter *identity.Limiter
	expiry  *time.Timer
}

func (api *API) initializeSetup() error {
	api.setup.limiter = identity.NewLimiter(15*time.Minute, 5, 30)
	if api.dependencies.SetupStore == nil {
		return nil
	}
	if api.policy.Load().origin == "" {
		origin, err := api.dependencies.SetupStore.PanelOrigin(context.Background())
		if err != nil {
			return err
		}
		if origin != "" {
			origin, err = config.ValidateOrigin(origin)
			if err != nil {
				return err
			}
			api.setOrigin(origin)
		}
	}
	initialized, err := api.dependencies.SetupStore.HasAdministrator(context.Background())
	if err != nil || initialized {
		return err
	}
	var random [15]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random[:])
	grouped := encoded[:6] + "-" + encoded[6:12] + "-" + encoded[12:18] + "-" + encoded[18:]
	api.setup.code = []byte(grouped)
	output := api.dependencies.SetupOutput
	if output == nil {
		output = io.Discard
	}
	_, err = fmt.Fprintf(output, "InstaDeck 首次设置码：%s（用于防止他人抢先设置，仅本次启动有效）\n", grouped)
	return err
}

func loopbackHost(host string) bool {
	parsed, err := url.Parse("http://" + host)
	if err != nil {
		return false
	}
	address := net.ParseIP(parsed.Hostname())
	return parsed.Hostname() == "localhost" || address != nil && address.IsLoopback()
}

func localSetup(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() || !loopbackHost(r.Host) {
		return false
	}
	for name := range r.Header {
		lower := strings.ToLower(name)
		if lower == "forwarded" || lower == "x-real-ip" || lower == "via" || lower == "cf-connecting-ip" || lower == "true-client-ip" || lower == "x-client-ip" || strings.HasPrefix(lower, "x-forwarded-") {
			return false
		}
	}
	// Same-origin GETs carry no Origin, and no-referrer removes Referer. The
	// status answer only decides whether the form shows the code field.
	if r.Method == "GET" && r.URL.Path == "/api/v1/setup/status" {
		return true
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && loopbackHost(origin.Host)
}

func (api *API) setupRequired(ctx context.Context) (bool, error) {
	if api.dependencies.SetupStore == nil {
		return false, nil
	}
	initialized, err := api.dependencies.SetupStore.HasAdministrator(ctx)
	return !initialized, err
}

func (api *API) setupStatus(w http.ResponseWriter, r *http.Request) {
	required, err := api.setupRequired(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	writeJSON(w, 200, map[string]bool{"required": required, "codeRequired": !localSetup(r)})
}

func (api *API) validateSetup(w http.ResponseWriter, r *http.Request) (string, bool) {
	required, err := api.setupRequired(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return "", false
	}
	if !required {
		fail(w, r, 409, "already_initialized")
		return "", false
	}
	values := r.Header.Values("Origin")
	if len(values) != 1 {
		fail(w, r, 403, "origin_rejected")
		return "", false
	}
	parsed, err := url.Parse(values[0])
	if err != nil || parsed.Host != r.Host {
		fail(w, r, 403, "origin_rejected")
		return "", false
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackHost(parsed.Host)) {
		fail(w, r, 403, "https_required")
		return "", false
	}
	origin, err := config.ValidateOrigin(values[0])
	if err != nil || origin != values[0] || api.policy.Load().origin != "" && api.policy.Load().origin != origin {
		fail(w, r, 403, "origin_rejected")
		return "", false
	}
	return origin, true
}

func (api *API) setupStart(w http.ResponseWriter, r *http.Request) {
	origin, valid := api.validateSetup(w, r)
	if !valid {
		return
	}
	var input struct {
		SetupCode string `json:"setupCode"`
		Username  string `json:"username"`
		Password  string `json:"password"`
	}
	if !decode(w, r, &input, 8192) {
		return
	}
	api.setup.mutex.Lock()
	validCode := subtle.ConstantTimeCompare(api.setup.code, []byte(input.SetupCode)) == 1 && len(api.setup.code) > 0
	api.setup.mutex.Unlock()
	if !localSetup(r) && !validCode {
		// Only failures count, so wrong guesses from elsewhere cannot lock out
		// the administrator who holds the correct code.
		if !api.setup.limiter.Allow(api.clientIP(r), time.Now()) {
			fail(w, r, 429, "rate_limited")
			return
		}
		fail(w, r, 403, "invalid_setup_code")
		return
	}
	if utf8.RuneCountInString(input.Username) < 1 || utf8.RuneCountInString(input.Username) > 64 {
		fail(w, r, 400, "invalid_request")
		return
	}
	if utf8.RuneCountInString(input.Password) < identity.MinPasswordLength || len(input.Password) > 1024 {
		fail(w, r, 400, "weak_password")
		return
	}
	password, err := identity.HashPassword(input.Password)
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	secret, err := identity.GenerateTOTPSecret()
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		clear(secret)
		fail(w, r, 503, "unavailable")
		return
	}
	cookie := base64.RawURLEncoding.EncodeToString(random[:])
	api.setup.mutex.Lock()
	defer api.setup.mutex.Unlock()
	required, err := api.setupRequired(r.Context())
	if err != nil || !required {
		clear(secret)
		fail(w, r, 409, "already_initialized")
		return
	}
	if api.setup.pending != nil {
		clear(api.setup.pending.secret)
	}
	api.setup.pending = &setupPending{cookieHash: identity.TokenHash(cookie), username: input.Username, password: password, secret: secret, expires: api.dependencies.Identity.SetupDeadline(), attempts: 5, origin: origin}
	if api.setup.expiry != nil {
		api.setup.expiry.Stop()
	}
	pending := api.setup.pending
	api.setup.expiry = time.AfterFunc(15*time.Minute, func() {
		api.setup.mutex.Lock()
		defer api.setup.mutex.Unlock()
		if api.setup.pending == pending {
			clear(pending.secret)
			api.setup.pending = nil
		}
	})
	http.SetCookie(w, &http.Cookie{Name: "instadeck_setup", Value: cookie, Path: "/", Secure: strings.HasPrefix(origin, "https://"), HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 900})
	writeJSON(w, 200, map[string]string{"otpauthUri": identity.OTPAuthURI(secret, "InstaDeck", input.Username), "secret": base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)})
}

func (api *API) setupComplete(w http.ResponseWriter, r *http.Request) {
	origin, valid := api.validateSetup(w, r)
	if !valid {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input, 1024) {
		return
	}
	api.setup.mutex.Lock()
	defer api.setup.mutex.Unlock()
	required, err := api.setupRequired(r.Context())
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	if !required {
		fail(w, r, 409, "already_initialized")
		return
	}
	pending := api.setup.pending
	if pending == nil || pending.cookieHash != identity.TokenHash(api.token(r, "instadeck_setup")) || pending.origin != origin || api.dependencies.Identity.SetupExpired(pending.expires) {
		fail(w, r, 401, "authentication_failed")
		return
	}
	if pending.attempts <= 0 {
		fail(w, r, 429, "rate_limited")
		return
	}
	pending.attempts--
	storedOrigin := origin
	if api.dependencies.Config.Origin != "" {
		storedOrigin = api.dependencies.Config.Origin
	}
	session, token, codes, err := api.dependencies.Identity.CompleteSetup(r.Context(), api.dependencies.SetupStore, pending.username, pending.password, pending.secret, input.Code, storedOrigin, identity.SessionMetadata{Device: deviceSummary(r.UserAgent()), SourceAddress: api.clientIP(r)})
	if err != nil {
		if errors.Is(err, sqlite.ErrConflict) {
			fail(w, r, 409, "already_initialized")
		} else if err.Error() == "rate_limited" {
			fail(w, r, 429, "rate_limited")
		} else {
			fail(w, r, 401, "authentication_failed")
		}
		return
	}
	clear(pending.secret)
	api.setup.pending = nil
	if api.setup.expiry != nil {
		api.setup.expiry.Stop()
		api.setup.expiry = nil
	}
	clear(api.setup.code)
	api.setup.code = nil
	api.setOrigin(storedOrigin)
	api.cookie(w, api.policy.Load().sessionCookie, token, 0)
	http.SetCookie(w, &http.Cookie{Name: "instadeck_setup", Value: "", Path: "/", Secure: api.policy.Load().secure, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"administratorId": session.AdministratorID, "csrfToken": session.CSRFSecret, "expiresAt": session.CreatedAt.Add(api.dependencies.AbsoluteSessionTTL), "recentlyAuthenticated": true, "recoveryCodes": codes})
}
