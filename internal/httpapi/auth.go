package httpapi

import (
	"context"
	"instadeck/internal/identity"
	"net/http"
	"strings"
)

type authenticationRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func validAuthentication(input authenticationRequest) bool {
	return len(input.Username) <= 128 && len(input.Password) <= 1024 && len(input.Code) <= 64
}

func (api *API) password(w http.ResponseWriter, r *http.Request) {
	var input authenticationRequest
	if !decode(w, r, &input, 4096) {
		return
	}
	if !validAuthentication(input) || input.Username == "" || input.Password == "" {
		fail(w, r, 400, "invalid_request")
		return
	}
	token, err := api.dependencies.Identity.Password(r.Context(), input.Username, input.Password, ClientIP(r, api.dependencies.Config.TrustLocalProxy))
	result := "challenge"
	if err != nil {
		result = "failure"
	}
	if api.event(r, "login.password", result, "", "") != nil {
		fail(w, r, 503, "audit_unavailable")
		return
	}
	if err != nil {
		fail(w, r, 401, "authentication_failed")
		return
	}
	api.cookie(w, api.challengeCookie, token, 300)
	writeJSON(w, 200, map[string]bool{"challengeRequired": true})
}

func (api *API) complete(w http.ResponseWriter, r *http.Request) {
	var input authenticationRequest
	if !decode(w, r, &input, 4096) {
		return
	}
	if !validAuthentication(input) || input.Code == "" {
		fail(w, r, 400, "invalid_request")
		return
	}
	token, err := api.dependencies.Identity.Complete(r.Context(), api.token(r, api.challengeCookie), input.Code, strings.HasSuffix(r.URL.Path, "/recovery"))
	if err != nil {
		if api.event(r, "login", "failure", "", "") != nil {
			fail(w, r, 503, "audit_unavailable")
			return
		}
		fail(w, r, 401, "authentication_failed")
		return
	}
	session, err := api.dependencies.Identity.Session(r.Context(), token)
	if err != nil {
		_ = api.dependencies.Identity.Revoke(r.Context(), token)
		fail(w, r, 503, "unavailable")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), sessionKey, session))
	if api.event(r, "login", "success", "", "") != nil {
		_ = api.dependencies.Identity.Revoke(r.Context(), token)
		fail(w, r, 503, "audit_unavailable")
		return
	}
	api.cookie(w, api.challengeCookie, "", -1)
	api.cookie(w, api.sessionCookie, token, 0)
	api.session(w, r)
}

func (api *API) session(w http.ResponseWriter, r *http.Request) {
	session := currentSession(r)
	writeJSON(w, 200, map[string]any{"administratorId": session.AdministratorID, "csrfToken": session.CSRFSecret, "expiresAt": session.CreatedAt.Add(api.dependencies.AbsoluteSessionTTL), "recentlyAuthenticated": api.dependencies.Identity.RecentlyAuthenticated(session)})
}

func (api *API) logout(w http.ResponseWriter, r *http.Request) {
	api.authMutex.Lock()
	defer api.authMutex.Unlock()
	if !api.requireAudit(w, r, "logout", "", "") {
		return
	}
	if api.dependencies.Identity.Revoke(r.Context(), api.token(r, api.sessionCookie)) != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	api.dependencies.Terminal.CloseBySession(currentSession(r).Hash)
	api.cookie(w, api.sessionCookie, "", -1)
	api.cookie(w, api.challengeCookie, "", -1)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (api *API) reauthenticate(w http.ResponseWriter, r *http.Request) {
	var input authenticationRequest
	if !decode(w, r, &input, 4096) {
		return
	}
	if !validAuthentication(input) || input.Password == "" || len(input.Code) != 6 {
		fail(w, r, 400, "invalid_request")
		return
	}
	api.authMutex.Lock()
	defer api.authMutex.Unlock()
	if !api.requireAudit(w, r, "reauthentication", "", "") {
		return
	}
	token, err := api.dependencies.Identity.Reauthenticate(r.Context(), api.token(r, api.sessionCookie), input.Password, input.Code, ClientIP(r, api.dependencies.Config.TrustLocalProxy))
	if err != nil {
		_ = api.event(r, "reauthentication", "failure", "", "")
		fail(w, r, 401, "authentication_failed")
		return
	}
	api.dependencies.Terminal.RebindSession(currentSession(r).Hash, identity.TokenHash(token))
	session, err := api.dependencies.Identity.Session(r.Context(), token)
	if err != nil {
		_ = api.dependencies.Identity.Revoke(r.Context(), token)
		api.dependencies.Terminal.CloseBySession(identity.TokenHash(token))
		fail(w, r, 503, "unavailable")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), sessionKey, session))
	if api.event(r, "reauthentication", "success", "", "") != nil {
		_ = api.dependencies.Identity.Revoke(r.Context(), token)
		api.dependencies.Terminal.CloseBySession(session.Hash)
		fail(w, r, 503, "audit_unavailable")
		return
	}
	api.cookie(w, api.sessionCookie, token, 0)
	api.session(w, r)
}
