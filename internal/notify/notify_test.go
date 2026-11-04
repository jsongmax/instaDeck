package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"instadeck/internal/fleet"
	"instadeck/internal/instabox"
	"instadeck/internal/keyring"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryRepo struct {
	values []Channel
	mutex  sync.Mutex
}

func (r *memoryRepo) ListChannels(context.Context) ([]Channel, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return append([]Channel{}, r.values...), nil
}
func (r *memoryRepo) SaveChannel(_ context.Context, c Channel, create bool) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if create {
		r.values = append(r.values, c)
	} else {
		for i := range r.values {
			if r.values[i].ID == c.ID {
				r.values[i] = c
			}
		}
	}
	return nil
}
func (r *memoryRepo) DeleteChannel(context.Context, string) error { return nil }
func testService(t *testing.T) (*Service, *memoryRepo, *bytes.Buffer) {
	t.Helper()
	key, _ := keyring.GenerateKey(1)
	raw, _ := json.Marshal(keyring.File{Keys: []keyring.Key{key}})
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := &memoryRepo{}
	logs := &bytes.Buffer{}
	return New(repo, ring, slog.New(slog.NewTextHandler(logs, nil))), repo, logs
}
func saveTest(t *testing.T, s *Service, kind string, config Config) Channel {
	t.Helper()
	c, err := s.Save(context.Background(), Channel{Name: "test", Type: kind, Enabled: true, Events: Events}, config, true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func trustServer(s *Service, server *httptest.Server) {
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	s.tlsConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}
func TestEncryptedConfigAADAndRedaction(t *testing.T) {
	s, repo, _ := testService(t)
	c := saveTest(t, s, "telegram", Config{BotToken: "123456:secret", ChatID: "-123"})
	if bytes.Contains(repo.values[0].Envelope.Ciphertext, []byte("secret")) {
		t.Fatal("plaintext stored")
	}
	if _, err := s.ring.Decrypt(c.Envelope, AAD("other")); err == nil {
		t.Fatal("AAD not bound")
	}
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(list)
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("ciphertext")) {
		t.Fatal("secret serialized")
	}
	c.Name = "renamed"
	if _, err = s.Save(context.Background(), c, Config{}, false); err != nil {
		t.Fatal(err)
	}
	v, _ := s.config(repo.values[0])
	if v.BotToken != "123456:secret" {
		t.Fatal("empty edit erased secret")
	}
}
func TestWebhookGuardSignatureRedirectAndRetry(t *testing.T) {
	s, _, _ := testService(t)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get("X-InstaDeck-Timestamp")
		mac := hmac.New(sha256.New, []byte("signing"))
		mac.Write([]byte(timestamp + "."))
		mac.Write(body)
		if r.Header.Get("X-InstaDeck-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("bad signature")
		}
		w.Header().Set("Location", "/should-not-follow")
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(429)
	}))
	defer server.Close()
	trustServer(s, server)
	c := saveTest(t, s, "webhook", Config{URL: server.URL, SigningKey: "signing"})
	err := s.send(context.Background(), c, Message{})
	if err == nil || err.Error() != "notify_address_blocked" || calls != 0 {
		t.Fatal("private address allowed", err, calls)
	}
	s.SetAddressFilter(false)
	err = s.send(context.Background(), c, Message{})
	f, ok := err.(*Failure)
	if !ok || f.RetryAfter != 3*time.Second || calls != 1 {
		t.Fatal("retry after", err, calls)
	}
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "https://127.0.0.1:1/secret", 302)
	}))
	defer redirect.Close()
	trustServer(s, redirect)
	c = saveTest(t, s, "webhook", Config{URL: redirect.URL})
	err = s.send(context.Background(), c, Message{})
	if err == nil || err.Error() != "notify_rejected" || calls != 2 {
		t.Fatal("redirect followed", err, calls)
	}
	for _, u := range []string{"http://example.com", "https://user:pass@example.com", "file:///etc/passwd"} {
		if _, err = s.Save(context.Background(), Channel{Name: "bad", Type: "webhook"}, Config{URL: u}, true); err == nil {
			t.Fatal("invalid URL", u)
		}
	}
}
func TestTelegramNeverLeaksToken(t *testing.T) {
	s, _, logs := testService(t)
	token := "123456:super-secret-token"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["parse_mode"]; ok {
			t.Error("formatting enabled")
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(token))
	}))
	trustServer(s, server)
	s.telegramBase = server.URL
	s.SetAddressFilter(false)
	c := saveTest(t, s, "telegram", Config{BotToken: token, ChatID: "123"})
	err := s.send(context.Background(), c, Message{})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("unsafe error")
	}
	server.Close()
	err = s.send(context.Background(), c, Message{})
	if err == nil || err.Error() != "notify_unreachable" || strings.Contains(err.Error(), token) {
		t.Fatal("url error leaked")
	}
	s.Start(context.Background())
	s.Emit(Message{Event: "operation.failed"})
	time.Sleep(30 * time.Millisecond)
	s.Stop()
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "/bot") {
		t.Fatal("token in logs")
	}
}
func TestQueueBaselineDedupeAndSuppression(t *testing.T) {
	s, _, _ := testService(t)
	monitor := NewMonitor(s, nil)
	now := time.Now()
	machine := fleet.Machine{ID: "machine", Name: "box", State: "running"}
	monitor.Compare([]fleet.Machine{machine}, nil, now)
	if len(s.queue) != 0 {
		t.Fatal("baseline sent")
	}
	machine.State = "error"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(time.Second))
	if len(s.queue) != 1 {
		t.Fatal("error absent")
	}
	machine.State = "running"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(2*time.Second))
	machine.State = "error"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(3*time.Second))
	if len(s.queue) != 1 {
		t.Fatal("dedupe failed")
	}
	machine.State = "running"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(4*time.Second))
	monitor.Operation(machine.ID, now)
	machine.State = "stopped"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(5*time.Second))
	if len(s.queue) != 1 {
		t.Fatal("panel stop sent")
	}
	machine.State = "running"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(3*time.Minute))
	machine.State = "suspended"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(4*time.Minute))
	if len(s.queue) != 1 {
		t.Fatal("suspended sent")
	}
	machine.State = "running"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(5*time.Minute))
	machine.State = "stopped"
	monitor.Compare([]fleet.Machine{machine}, nil, now.Add(6*time.Minute))
	if len(s.queue) != 2 {
		t.Fatal("external stop missing")
	}
	for i := 0; i < 100; i++ {
		s.Emit(Message{})
	}
	if len(s.queue) != 64 {
		t.Fatal("queue bound")
	}
	done := make(chan struct{})
	go func() { s.Emit(Message{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue blocked")
	}
}
func TestMessageRedaction(t *testing.T) {
	s, _, _ := testService(t)
	s.Emit(Message{Account: "alice@example.com", Machine: "ibk_private-key", Message: "123456:telegram_token"})
	j := <-s.queue
	raw, _ := json.Marshal(j.message)
	for _, secret := range []string{"alice@example.com", "ibk_private-key", "telegram_token"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("message leaked", secret)
		}
	}
}

type monitoredFleet struct{ calls atomic.Int32 }

func (f *monitoredFleet) GetFleet(context.Context) (fleet.Response, error) {
	state := "running"
	if f.calls.Add(1) > 1 {
		state = "stopped"
	}
	return fleet.Response{Machines: []fleet.Machine{{ID: "test", State: instabox.MachineState(state)}}}, nil
}
func TestMonitorOnlyPollsWithSubscribersAndExits(t *testing.T) {
	service, _, _ := testService(t)
	fleets := &monitoredFleet{}
	monitor := NewMonitor(service, fleets)
	monitor.interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); monitor.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	if fleets.calls.Load() != 0 {
		t.Fatal("polled with no channel")
	}
	saveTest(t, service, "telegram", Config{BotToken: "123456:test", ChatID: "1"})
	select {
	case j := <-service.queue:
		if j.message.Event != "machine.stopped" {
			t.Fatal(j.message.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not start monitoring")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor failed to exit")
	}
	calls := fleets.calls.Load()
	time.Sleep(30 * time.Millisecond)
	if fleets.calls.Load() != calls {
		t.Fatal("monitor leaked")
	}
}

func TestRetryDelayIsCapped(t *testing.T) {
	if maxRetryDelay > time.Minute {
		t.Fatalf("a Retry-After could stall every channel for %v", maxRetryDelay)
	}
}
