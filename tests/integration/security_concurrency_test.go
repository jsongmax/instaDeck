package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"instadeck/internal/httpapi"
	"instadeck/internal/identity"
	"instadeck/internal/store/sqlite"
	"instadeck/internal/terminal"
)

type blockedChallengeRepository struct {
	*sqlite.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (repository *blockedChallengeRepository) CreateVerifiedChallenge(ctx context.Context, challenge identity.Challenge, revision int64) error {
	repository.once.Do(func() { close(repository.entered); <-repository.release })
	return repository.Store.CreateVerifiedChallenge(ctx, challenge, revision)
}

func TestSecurityChangeRejectsConcurrentOldPasswordVerification(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	repository := &blockedChallengeRepository{Store: fixture.store, entered: make(chan struct{}), release: make(chan struct{})}
	service, err := identity.New(repository, fixture.ring, func() time.Time { return time.Unix(fixture.clock.Load(), 0) }, identity.Options{ChallengeTTL: 5 * time.Minute, IdleTTL: 30 * time.Minute, AbsoluteTTL: 8 * time.Hour, RecentTTL: 5 * time.Minute, ChallengeAttempts: 5}, identity.NewLimiter(time.Minute, 100, 100))
	if err != nil {
		t.Fatal(err)
	}
	fixture.dependencies.Identity = service
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		finished <- securityRequest(fixture, "/auth/password", map[string]string{"username": "admin", "password": testPassword}, nil, "")
	}()
	<-repository.entered
	response := securityRequest(fixture, "/security/password", map[string]string{"currentPassword": testPassword, "newPassword": "replacement-password-123"}, cookie, csrf)
	close(repository.release)
	if response.Code != 200 {
		t.Fatal("password change blocked by login password work", response.Body)
	}
	if late := <-finished; late.Code != 401 {
		t.Fatal("old password verification created a challenge after change", late.Body)
	}
}

func TestSecurityChangeRejectsPreparedReauthentication(t *testing.T) {
	for _, mode := range []string{"password", "totp"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newFixture(t, false)
			cookie, csrf := fixture.login(t)
			fixture.clock.Add(61)
			preparation, err := fixture.dependencies.Identity.PrepareSecurity(context.Background(), cookie.Value, testPassword, "", "203.0.113.20", "reauthenticate")
			if err != nil {
				t.Fatal(err)
			}
			prepared := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan error, 1)
			go func() {
				close(prepared)
				<-release
				_, err := fixture.dependencies.Identity.CompleteReauthentication(context.Background(), preparation, totpCode(fixture.clock.Load()/30))
				finished <- err
			}()
			<-prepared
			var response *httptest.ResponseRecorder
			if mode == "password" {
				response = securityRequest(fixture, "/security/password", map[string]string{"currentPassword": testPassword, "newPassword": "replacement-password-123"}, cookie, csrf)
			} else {
				secret := startAuthenticator(t, fixture, cookie, csrf)
				response = securityRequest(fixture, "/security/totp/confirm", map[string]string{"code": codeForSecret(t, secret, fixture.clock.Load()/30)}, cookie, csrf)
			}
			close(release)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body)
			}
			if err := <-finished; err == nil {
				t.Fatal("prepared reauthentication survived credential change")
			}
		})
	}
}

func TestSessionRevocationClosesTerminalsAndTickets(t *testing.T) {
	for _, mode := range []string{"single", "others"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newFixture(t, false)
			fakeSSH(t, fixture)
			cookie, csrf := fixture.login(t)
			credential := fixture.addAccount(t, cookie, csrf)
			fixture.clock.Add(61)
			otherCookie, otherCSRF := fixture.login(t)
			otherSession, err := fixture.dependencies.Identity.Session(context.Background(), otherCookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			ticket := terminalTicket(t, fixture, otherCookie, otherCSRF, credential.CredentialID)
			spare := terminalTicket(t, fixture, otherCookie, otherCSRF, credential.CredentialID)
			server := httptest.NewServer(fixture.api)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			connection, _, err := dialTerminal(ctx, server, otherCookie, fixture.dependencies.Config.Origin, ticket)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.CloseNow()
			if _, _, err = connection.Read(ctx); err != nil {
				t.Fatal(err)
			}
			var response *httptest.ResponseRecorder
			if mode == "single" {
				response = fixture.request(http.MethodDelete, "/api/v1/sessions/"+otherSession.PublicID, "", cookie, csrf, fixture.dependencies.Config.Origin)
			} else {
				response = securityRequest(fixture, "/sessions/revoke-others", struct{}{}, cookie, csrf)
			}
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body)
			}
			if fixture.request("GET", "/api/v1/session", "", otherCookie, "", "").Code != 401 {
				t.Fatal("revoked session still usable")
			}
			if _, _, err = connection.Read(ctx); err == nil {
				t.Fatal("revoked session terminal survived")
			}
			if err = fixture.dependencies.Terminal.ServeUpgrade(ctx, terminal.Ticket(spare), otherSession.Hash, func(terminal.Binding) (terminal.FrameConn, error) {
				t.Error("revoked ticket reached upgrade")
				return nil, terminal.InvalidTicket
			}); err != terminal.InvalidTicket {
				t.Fatal("revoked ticket survived", err)
			}
			if fixture.request("GET", "/api/v1/session", "", cookie, "", "").Code != 200 {
				t.Fatal("current session revoked")
			}
		})
	}
}

func TestSecurityRotationRejectsTicketIssuanceAlreadyInFlight(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	entered, release := make(chan struct{}), make(chan struct{})
	fixture.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/v1/machines/machine" {
			close(entered)
			<-release
		}
		fixture.upstreamHTTP(w, r)
	})
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		finished <- securityRequest(fixture, "/machines/machine/terminal-tickets", map[string]string{"credentialId": credential.CredentialID}, cookie, csrf)
	}()
	<-entered
	fixture.clock.Add(61)
	response := securityRequest(fixture, "/security/password", map[string]string{"currentPassword": testPassword, "newPassword": "replacement-password-123"}, cookie, csrf)
	close(release)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if late := <-finished; late.Code != 401 {
		t.Fatal("in-flight request issued a ticket for a revoked token", late.Body)
	}
}
