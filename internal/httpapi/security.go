package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"instadeck/internal/identity"
)

func (api *API) registerSecurity(router chi.Router) {
	router.Get("/security", api.security)
	router.Post("/security/password", api.changeSecurity)
	router.Post("/security/totp/start", api.changeSecurity)
	router.Post("/security/totp/confirm", api.confirmTOTP)
	router.Post("/security/recovery-codes", api.changeSecurity)
	router.Get("/sessions", api.sessions)
	router.Delete("/sessions/{publicId}", api.revokeSessions)
	router.Post("/sessions/revoke-others", api.revokeSessions)
}

func (api *API) security(w http.ResponseWriter, r *http.Request) {
	boundAt, remaining, err := api.dependencies.Identity.Security(r.Context(), currentSession(r).AdministratorID)
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"totpBoundAt": boundAt, "recoveryCodesLeft": remaining})
}

func securityFailure(w http.ResponseWriter, r *http.Request, err error) {
	code := err.Error()
	status := 400
	switch code {
	case "unauthorized", "authentication_failed":
		status = 401
	case "reauthentication_required":
		status = 403
	case "weak_password", "password_unchanged", "totp_pending_missing", "cannot_revoke_current":
	default:
		code = "unavailable"
		status = 503
	}
	fail(w, r, status, code)
}

func (api *API) changeSecurity(w http.ResponseWriter, r *http.Request) {
	mode := "recovery"
	var password, newPassword, code string
	switch {
	case strings.HasSuffix(r.URL.Path, "/password"):
		mode = "password"
		var input struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if !decode(w, r, &input, 16384) {
			return
		}
		password, newPassword = input.CurrentPassword, input.NewPassword
	default:
		if strings.HasSuffix(r.URL.Path, "/start") {
			mode = "start"
		}
		var input struct {
			Password string `json:"password"`
			Code     string `json:"code"`
		}
		if !decode(w, r, &input, 8192) {
			return
		}
		password, code = input.Password, input.Code
	}
	if len(password) > 1024 || len(code) > 64 {
		fail(w, r, 400, "invalid_request")
		return
	}
	preparation, err := api.dependencies.Identity.PrepareSecurity(r.Context(), api.token(r, api.policy.Load().sessionCookie), password, newPassword, api.clientIP(r), mode)
	if err != nil {
		_ = api.event(r, "security."+mode, "failure", "", "")
		securityFailure(w, r, err)
		return
	}
	api.authMutex.Lock()
	defer api.authMutex.Unlock()
	if !api.requireAudit(w, r, "security."+mode, "", "") {
		return
	}
	result, err := api.dependencies.Identity.ApplySecurity(r.Context(), preparation, mode, code)
	if err != nil {
		_ = api.event(r, "security."+mode, "failure", "", "")
		securityFailure(w, r, err)
		return
	}
	_ = api.event(r, "security."+mode, "success", "", "")
	if result.Token != "" {
		api.finishSecurityRotation(w, r, result)
		return
	}
	if mode == "start" {
		writeJSON(w, 200, map[string]string{"otpauthUri": result.OTPAuthURI, "secret": result.Secret})
		return
	}
	writeJSON(w, 200, map[string]any{"recoveryCodes": result.RecoveryCodes})
}

func (api *API) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input, 1024) {
		return
	}
	if len(input.Code) > 64 {
		fail(w, r, 400, "invalid_request")
		return
	}
	api.authMutex.Lock()
	defer api.authMutex.Unlock()
	if !api.requireAudit(w, r, "security.totp", "", "") {
		return
	}
	result, err := api.dependencies.Identity.ConfirmTOTP(r.Context(), api.token(r, api.policy.Load().sessionCookie), input.Code)
	if err != nil {
		_ = api.event(r, "security.totp", "failure", "", "")
		securityFailure(w, r, err)
		return
	}
	_ = api.event(r, "security.totp", "success", "", "")
	api.finishSecurityRotation(w, r, result)
}

func (api *API) finishSecurityRotation(w http.ResponseWriter, r *http.Request, result identity.SecurityResult) {
	for _, hash := range result.Revoked {
		api.dependencies.Terminal.CloseBySession(hash)
	}
	api.dependencies.Terminal.RebindSession(currentSession(r).Hash, identity.TokenHash(result.Token))
	session, err := api.dependencies.Identity.Session(r.Context(), result.Token)
	if err != nil {
		_ = api.dependencies.Identity.Revoke(r.Context(), result.Token)
		api.dependencies.Terminal.CloseBySession(identity.TokenHash(result.Token))
		fail(w, r, 503, "unavailable")
		return
	}
	api.cookie(w, api.policy.Load().sessionCookie, result.Token, 0)
	response := map[string]any{"administratorId": session.AdministratorID, "csrfToken": session.CSRFSecret, "expiresAt": session.CreatedAt.Add(api.dependencies.AbsoluteSessionTTL), "recentlyAuthenticated": api.dependencies.Identity.RecentlyAuthenticated(session)}
	if result.RecoveryCodes != nil {
		response["recoveryCodes"] = result.RecoveryCodes
	}
	writeJSON(w, 200, response)
}

func (api *API) sessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := api.dependencies.Identity.Sessions(r.Context(), currentSession(r).AdministratorID)
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	values := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		values = append(values, map[string]any{"publicId": session.PublicID, "device": session.Device, "sourceAddress": session.SourceAddress, "createdAt": session.CreatedAt, "lastSeen": session.LastSeen, "current": session.Hash == currentSession(r).Hash})
	}
	writeJSON(w, 200, map[string]any{"sessions": values})
}

func (api *API) revokeSessions(w http.ResponseWriter, r *http.Request) {
	api.authMutex.Lock()
	defer api.authMutex.Unlock()
	session, err := api.dependencies.Identity.Session(r.Context(), api.token(r, api.policy.Load().sessionCookie))
	if err != nil {
		fail(w, r, 401, "unauthorized")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), sessionKey, session))
	if !api.recent(w, r) {
		return
	}
	if !api.requireAudit(w, r, "sessions.revoke", "", "") {
		return
	}
	hashes, err := api.dependencies.Identity.RevokeSessions(r.Context(), api.token(r, api.policy.Load().sessionCookie), chi.URLParam(r, "publicId"))
	if err != nil {
		securityFailure(w, r, err)
		return
	}
	_ = api.event(r, "sessions.revoke", "success", "", "")
	for _, hash := range hashes {
		api.dependencies.Terminal.CloseBySession(hash)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func deviceSummary(userAgent string) string {
	browser, platform := "", ""
	for _, candidate := range []struct{ marker, name string }{{marker: "Edg/", name: "Edge"}, {marker: "OPR/", name: "Opera"}, {marker: "Chrome/", name: "Chrome"}, {marker: "Firefox/", name: "Firefox"}, {marker: "Safari/", name: "Safari"}} {
		if strings.Contains(userAgent, candidate.marker) {
			browser = candidate.name
			break
		}
	}
	for _, candidate := range []struct{ marker, name string }{{marker: "Android", name: "Android"}, {marker: "iPhone", name: "iOS"}, {marker: "iPad", name: "iOS"}, {marker: "Windows", name: "Windows"}, {marker: "Macintosh", name: "macOS"}, {marker: "Linux", name: "Linux"}} {
		if strings.Contains(userAgent, candidate.marker) {
			platform = candidate.name
			break
		}
	}
	if browser == "" || platform == "" {
		return "未知设备"
	}
	return browser + " · " + platform
}
