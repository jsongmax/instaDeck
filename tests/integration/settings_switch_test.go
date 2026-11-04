package integration_test

import (
	"encoding/json"
	"instadeck/internal/httpapi"
	"strings"
	"testing"
)

func TestSecuritySwitches(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	origin := fixture.dependencies.Config.Origin
	patch := func(body string) (int, string) {
		response := fixture.request("PATCH", "/api/v1/settings", body, cookie, csrf, origin)
		return response.Code, response.Body.String()
	}
	response := fixture.request("GET", "/api/v1/settings", "", cookie, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"blockInternalSsh":true`) || !strings.Contains(response.Body.String(), `"requireReauth":true`) {
		t.Fatal("protections must default to on", response.Code, response.Body)
	}
	if response = fixture.request("GET", "/api/v1/settings", "", nil, "", ""); response.Code != 401 {
		t.Fatal("settings need a session", response.Code)
	}
	if code, _ := patch(`{}`); code != 400 {
		t.Fatal("empty change accepted", code)
	}
	fixture.clock.Add(301)
	if response = fixture.request("POST", "/api/v1/accounts", `{}`, cookie, csrf, origin); response.Code != 403 {
		t.Fatal("reauthentication must apply by default", response.Code)
	}
	for _, body := range []string{`{"requireReauth":false}`, `{"blockInternalSsh":false}`} {
		if code, text := patch(body); code != 403 || !strings.Contains(text, "reauthentication_required") {
			t.Fatal("turning a protection off needs fresh credentials", code, text)
		}
	}
	reauthenticate := fixture.request("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`","code":"`+totpCode(fixture.clock.Load()/30)+`"}`, cookie, csrf, origin)
	if reauthenticate.Code != 200 {
		t.Fatal(reauthenticate.Code, reauthenticate.Body)
	}
	// Reauthentication rotates the session token.
	for _, candidate := range reauthenticate.Result().Cookies() {
		if strings.Contains(candidate.Name, "session") {
			cookie = candidate
		}
	}
	var rotated struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.Unmarshal(reauthenticate.Body.Bytes(), &rotated); err != nil || rotated.CSRF == "" {
		t.Fatal("missing rotated session", err)
	}
	csrf = rotated.CSRF
	if code, text := patch(`{"requireReauth":false}`); code != 200 || !strings.Contains(text, `"requireReauth":false`) {
		t.Fatal(code, text)
	}
	fixture.clock.Add(301)
	if response = fixture.request("POST", "/api/v1/accounts", `{}`, cookie, csrf, origin); response.Code == 403 {
		t.Fatal("switch off must skip the second confirmation", response.Body)
	}
	if code, _ := patch(`{"blockInternalSsh":false}`); code != 403 {
		t.Fatal("the other protection must still need fresh credentials", code)
	}
	if response = fixture.request("POST", "/api/v1/sessions/revoke-others", "", cookie, csrf, origin); response.Code != 403 {
		t.Fatal("session revocation must keep its own check", response.Code)
	}
	restarted, err := httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	fixture.api = restarted
	response = fixture.request("GET", "/api/v1/settings", "", cookie, "", "")
	if !strings.Contains(response.Body.String(), `"requireReauth":false`) {
		t.Fatal("switch not persisted", response.Body)
	}
	if response = fixture.request("POST", "/api/v1/accounts", `{}`, cookie, csrf, origin); response.Code == 403 {
		t.Fatal("switch not applied after restart", response.Body)
	}
	if code, text := patch(`{"requireReauth":true}`); code != 200 || !strings.Contains(text, `"requireReauth":true`) {
		t.Fatal("turning a protection back on must not need credentials", code, text)
	}
	if response = fixture.request("POST", "/api/v1/accounts", `{}`, cookie, csrf, origin); response.Code != 403 {
		t.Fatal("protection did not come back", response.Code)
	}
}
