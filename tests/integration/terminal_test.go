package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	"instadeck/internal/instabox"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	value, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func certificate(public ssh.PublicKey, authority ssh.Signer, kind uint32, principal string) (*ssh.Certificate, error) {
	value := &ssh.Certificate{Key: public, CertType: kind, ValidPrincipals: []string{principal}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Hour).Unix())}
	return value, value.SignCert(rand.Reader, authority)
}

func fakeSSH(t *testing.T, fixture *fixture) {
	t.Helper()
	authority, hostKey := signer(t), signer(t)
	hostCertificate, err := certificate(hostKey.PublicKey(), authority, ssh.HostCert, "gateway")
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewCertSigner(hostCertificate, hostKey)
	if err != nil {
		t.Fatal(err)
	}
	configuration := &ssh.ServerConfig{PublicKeyCallback: func(metadata ssh.ConnMetadata, public ssh.PublicKey) (*ssh.Permissions, error) {
		value, ok := public.(*ssh.Certificate)
		if !ok {
			return nil, errors.New("certificate required")
		}
		checker := ssh.CertChecker{}
		return nil, checker.CheckCert("machine", value)
	}}
	configuration.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var mutex sync.Mutex
	connections := []net.Conn{}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			network, err := listener.Accept()
			if err != nil {
				return
			}
			mutex.Lock()
			connections = append(connections, network)
			mutex.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer network.Close()
				connection, channels, requests, err := ssh.NewServerConn(network, configuration)
				if err != nil {
					return
				}
				defer connection.Close()
				go ssh.DiscardRequests(requests)
				var sessions sync.WaitGroup
				for channel := range channels {
					if channel.ChannelType() != "session" {
						_ = channel.Reject(ssh.Prohibited, "denied")
						continue
					}
					stream, requests, err := channel.Accept()
					if err != nil {
						continue
					}
					sessions.Add(1)
					go func() {
						defer sessions.Done()
						defer stream.Close()
						for request := range requests {
							_ = request.Reply(request.Type == "pty-req" || request.Type == "shell", nil)
							if request.Type == "shell" {
								_, _ = io.Copy(stream, stream)
								return
							}
						}
					}()
				}
				sessions.Wait()
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		mutex.Lock()
		for _, connection := range connections {
			_ = connection.Close()
		}
		mutex.Unlock()
		workers.Wait()
	})
	fixture.connection = func(w http.ResponseWriter, r *http.Request) {
		var input instabox.CreateConnectionRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			respond(w, 400, nil)
			return
		}
		public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(input.PublicKey))
		if err != nil {
			respond(w, 400, nil)
			return
		}
		value, err := certificate(public, authority, ssh.UserCert, "machine")
		if err != nil {
			respond(w, 500, nil)
			return
		}
		respond(w, 201, instabox.Connection{Certificate: string(ssh.MarshalAuthorizedKey(value)), ExpiresAt: time.Now().Add(time.Hour), Host: "127.0.0.1", Port: int64(listener.Addr().(*net.TCPAddr).Port), MachineID: "machine", Transport: "ssh", Username: "test-user", HostTrust: instabox.HostTrust{Type: "ca", PublicKey: string(ssh.MarshalAuthorizedKey(authority.PublicKey())), Principals: []string{"gateway"}}})
	}
}

func terminalTicket(t *testing.T, fixture *fixture, cookie *http.Cookie, csrf, credentialID string) string {
	t.Helper()
	response := fixture.request("POST", "/api/v1/machines/machine/terminal-tickets", `{"credentialId":"`+credentialID+`"}`, cookie, csrf, fixture.dependencies.Config.Origin)
	var value struct {
		Ticket string `json:"ticket"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil || value.Ticket == "" {
		t.Fatal("ticket issuance failed", response.Code, response.Body)
	}
	return value.Ticket
}

func dialTerminal(ctx context.Context, server *httptest.Server, cookie *http.Cookie, origin, ticket string) (*websocket.Conn, *http.Response, error) {
	headers := http.Header{}
	if cookie != nil {
		headers.Set("Cookie", cookie.String())
	}
	if origin != "" {
		headers.Set("Origin", origin)
	}
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/terminal/ws", &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"instadeck.terminal.v1", "instadeck.ticket." + ticket}})
}

func TestWebSocketBoundariesRevocationAndRotation(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "logout", true: "reauthenticate-then-logout"}[rotate], func(t *testing.T) {
			fixture := newFixture(t, false)
			fakeSSH(t, fixture)
			cookie, csrf := fixture.login(t)
			credential := fixture.addAccount(t, cookie, csrf)
			server := httptest.NewServer(fixture.api)
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for _, test := range []struct {
				cookie         *http.Cookie
				origin, ticket string
				status         int
			}{
				{origin: fixture.dependencies.Config.Origin, ticket: strings.Repeat("x", 43), status: 401},
				{cookie: cookie, origin: "https://evil.invalid", ticket: strings.Repeat("x", 43), status: 403},
				{cookie: cookie, ticket: strings.Repeat("x", 43), status: 403},
				{cookie: cookie, origin: fixture.dependencies.Config.Origin, ticket: strings.Repeat("x", 43), status: 403},
			} {
				connection, response, err := dialTerminal(ctx, server, test.cookie, test.origin, test.ticket)
				if connection != nil {
					_ = connection.CloseNow()
				}
				if err == nil || response == nil || response.StatusCode != test.status {
					t.Fatal("unauthorized WebSocket established", err, response)
				}
				_ = response.Body.Close()
			}
			ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
			connection, response, err := dialTerminal(ctx, server, cookie, fixture.dependencies.Config.Origin, ticket)
			if err != nil {
				t.Fatal("WebSocket failed", err)
			}
			defer connection.CloseNow()
			if response.Header.Get("Sec-WebSocket-Protocol") != "instadeck.terminal.v1" {
				t.Fatal("ticket negotiated")
			}
			_, ready, err := connection.Read(ctx)
			if err != nil || !strings.Contains(string(ready), `"type":"ready"`) {
				t.Fatal("SSH not ready", err, string(ready))
			}
			if connection.Write(ctx, websocket.MessageBinary, []byte("echo-test")) != nil {
				t.Fatal("terminal input failed")
			}
			kind, echo, err := connection.Read(ctx)
			if err != nil || kind != websocket.MessageBinary || string(echo) != "echo-test" {
				t.Fatal("SSH echo failed", err)
			}
			replayed, replayResponse, replayErr := dialTerminal(ctx, server, cookie, fixture.dependencies.Config.Origin, ticket)
			if replayed != nil {
				_ = replayed.CloseNow()
			}
			if replayErr == nil || replayResponse.StatusCode != 403 {
				t.Fatal("ticket replay accepted")
			}
			_ = replayResponse.Body.Close()
			if rotate {
				fixture.clock.Add(61)
				response := fixture.request("POST", "/api/v1/auth/reauthenticate", `{"password":"`+testPassword+`","code":"`+totpCode(fixture.clock.Load()/30)+`"}`, cookie, csrf, fixture.dependencies.Config.Origin)
				if response.Code != 200 {
					t.Fatal("reauthentication failed", response.Body)
				}
				previous := cookie
				cookie = response.Result().Cookies()[0]
				var session struct {
					CSRF string `json:"csrfToken"`
				}
				_ = json.Unmarshal(response.Body.Bytes(), &session)
				csrf = session.CSRF
				if fixture.request("GET", "/api/v1/session", "", previous, "", "").Code != 401 {
					t.Fatal("old session remained valid")
				}
			}
			if fixture.request("POST", "/api/v1/auth/logout", `{}`, cookie, csrf, fixture.dependencies.Config.Origin).Code != 200 {
				t.Fatal("logout failed")
			}
			if _, _, err = connection.Read(ctx); err == nil {
				t.Fatal("terminal outlived logout")
			}
			if strings.Contains(fixture.logs.String(), ticket) || strings.Contains(fixture.logs.String(), "instadeck.ticket") {
				t.Fatal("ticket logged")
			}
		})
	}
}

func TestTicketCannotMoveBetweenSessions(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
	fixture.clock.Add(30)
	otherCookie, _ := fixture.login(t)
	server := httptest.NewServer(fixture.api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, response, err := dialTerminal(ctx, server, otherCookie, fixture.dependencies.Config.Origin, ticket)
	if connection != nil {
		_ = connection.CloseNow()
	}
	if err == nil || response.StatusCode != 403 {
		t.Fatal("ticket moved to another session")
	}
	_ = response.Body.Close()
}
