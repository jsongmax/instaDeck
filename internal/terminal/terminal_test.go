package terminal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"instadeck/internal/instabox"
)

type fakeUpstream struct {
	info   instabox.ConnectionInfo
	create func(context.Context, Binding, instabox.CreateConnectionRequest, string) (instabox.Connection, error)
}

func (upstream *fakeUpstream) GetConnectionInfo(context.Context, Binding) (instabox.ConnectionInfo, error) {
	return upstream.info, nil
}

func (upstream *fakeUpstream) CreateConnection(ctx context.Context, binding Binding, request instabox.CreateConnectionRequest, key string) (instabox.Connection, error) {
	return upstream.create(ctx, binding, request, key)
}

func testBinding() Binding {
	return Binding{SessionHash: sha256.Sum256([]byte("session")), CredentialID: "credential", CredentialVersion: 1, MachineID: "machine", OrgID: "org"}
}

func newManager(t *testing.T, upstream Upstream, options Options) *Manager {
	t.Helper()
	manager, err := New(upstream, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Shutdown)
	return manager
}

func TestTickets(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	manager := newManager(t, &fakeUpstream{}, Options{Now: func() time.Time { return time.Unix(0, clock.Load()) }})
	binding := testBinding()
	ticket, err := manager.IssueTicket(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%s", "%q", "%v", "%+v", "%#v", "%x"} {
		if strings.Contains(fmt.Sprintf(format, ticket), ticket.Value()) {
			t.Fatal("ticket leaked")
		}
	}
	if _, err = manager.consume(ticket, [32]byte{}); err != InvalidTicket {
		t.Fatal(err)
	}
	if _, err = manager.consume(ticket, binding.SessionHash); err != InvalidTicket {
		t.Fatal(err)
	}
	ticket, _ = manager.IssueTicket(binding)
	clock.Add(int64(31 * time.Second))
	if _, err = manager.consume(ticket, binding.SessionHash); err != InvalidTicket {
		t.Fatal(err)
	}
	ticket, _ = manager.IssueTicket(binding)
	var successes atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < 32; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if _, err := manager.consume(ticket, binding.SessionHash); err == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	workers.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
	for index := 0; index < 100; index++ {
		_, _ = manager.IssueTicket(binding)
	}
	clock.Add(int64(time.Minute))
	manager.mu.Lock()
	manager.prune()
	remaining := len(manager.tickets)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatal(remaining)
	}
}

func TestAddressPolicy(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "fe80::1", "fc00::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "::ffff:127.0.0.1"} {
		if safeAddress(netip.MustParseAddr(address), false) {
			t.Fatal(address)
		}
	}
	if !safeAddress(netip.MustParseAddr("127.0.0.1"), true) || !safeAddress(netip.MustParseAddr("8.8.8.8"), false) {
		t.Fatal("address policy")
	}
}

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func signedCertificate(t *testing.T, key ssh.PublicKey, authority ssh.Signer, kind uint32, principal string) *ssh.Certificate {
	t.Helper()
	certificate := &ssh.Certificate{Key: key, CertType: kind, ValidPrincipals: []string{principal}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Hour).Unix())}
	if err := certificate.SignCert(rand.Reader, authority); err != nil {
		t.Fatal(err)
	}
	return certificate
}

type sshFixture struct {
	upstream *fakeUpstream
	resize   chan []byte
	done     chan struct{}
	finish   chan uint32
}

func startSSH(t *testing.T, mode string) *sshFixture {
	t.Helper()
	authority := testSigner(t)
	hostKey := testSigner(t)
	hostCertificate := signedCertificate(t, hostKey.PublicKey(), authority, ssh.HostCert, "gateway")
	switch mode {
	case "principal":
		hostCertificate.ValidPrincipals = []string{"other"}
	case "expired":
		hostCertificate.ValidBefore = uint64(time.Now().Add(-time.Minute).Unix())
	}
	if err := hostCertificate.SignCert(rand.Reader, authority); err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewCertSigner(hostCertificate, hostKey)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "plain" {
		hostSigner = hostKey
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &sshFixture{resize: make(chan []byte, 8), done: make(chan struct{}), finish: make(chan uint32, 1)}
	configuration := &ssh.ServerConfig{PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		certificate, ok := key.(*ssh.Certificate)
		if !ok || metadata.User() != "from-response" {
			return nil, errors.New("denied")
		}
		checker := ssh.CertChecker{IsUserAuthority: func(key ssh.PublicKey) bool { return bytes.Equal(key.Marshal(), authority.PublicKey().Marshal()) }}
		return nil, checker.CheckCert("machine", certificate)
	}}
	configuration.AddHostKey(hostSigner)
	var connections sync.WaitGroup
	var connectionMu sync.Mutex
	var active []net.Conn
	go func() {
		defer close(fixture.done)
		for {
			network, err := listener.Accept()
			if err != nil {
				return
			}
			connectionMu.Lock()
			active = append(active, network)
			connectionMu.Unlock()
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer network.Close()
				server, channels, requests, err := ssh.NewServerConn(network, configuration)
				if err != nil {
					return
				}
				defer server.Close()
				var workers sync.WaitGroup
				workers.Add(1)
				go func() {
					defer workers.Done()
					for request := range requests {
						_ = request.Reply(true, nil)
					}
				}()
				for channel := range channels {
					if mode == "reject-session" {
						_ = channel.Reject(ssh.Prohibited, "private-server-detail")
						continue
					}
					if channel.ChannelType() != "session" {
						_ = channel.Reject(ssh.UnknownChannelType, "denied")
						continue
					}
					stream, requests, err := channel.Accept()
					if err != nil {
						continue
					}
					workers.Add(1)
					go func() {
						defer workers.Done()
						defer stream.Close()
						var echo sync.WaitGroup
						defer echo.Wait()
						for request := range requests {
							switch request.Type {
							case "pty-req":
								if mode == "reject-pty" {
									_ = request.Reply(false, nil)
									continue
								}
								_ = request.Reply(true, nil)
							case "shell":
								if mode == "reject-shell" {
									_ = request.Reply(false, nil)
									continue
								}
								_ = request.Reply(true, nil)
								echo.Add(1)
								go func() {
									defer echo.Done()
									if mode == "flood" {
										payload := bytes.Repeat([]byte("x"), 32768)
										for {
											if _, err := stream.Write(payload); err != nil {
												return
											}
										}
									}
									_, _ = io.Copy(stream, stream)
								}()
							case "window-change":
								fixture.resize <- append([]byte(nil), request.Payload...)
							default:
								_ = request.Reply(false, nil)
							}
							select {
							case code := <-fixture.finish:
								_, _ = stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: code}))
								_ = stream.Close()
								return
							default:
							}
						}
					}()
				}
				workers.Wait()
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-fixture.done
		connectionMu.Lock()
		for _, network := range active {
			_ = network.Close()
		}
		connectionMu.Unlock()
		connections.Wait()
	})
	trustAuthority := authority
	if mode == "ca" {
		trustAuthority = testSigner(t)
	}
	fixture.upstream = &fakeUpstream{info: instabox.ConnectionInfo{Capabilities: []string{"pty"}}}
	fixture.upstream.create = func(_ context.Context, binding Binding, request instabox.CreateConnectionRequest, _ string) (instabox.Connection, error) {
		public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(request.PublicKey))
		if err != nil {
			return instabox.Connection{}, err
		}
		if mode == "user-key" {
			public = testSigner(t).PublicKey()
		}
		certificate := signedCertificate(t, public, authority, ssh.UserCert, binding.MachineID)
		connection := instabox.Connection{Certificate: string(ssh.MarshalAuthorizedKey(certificate)), ExpiresAt: time.Now().Add(time.Hour), Host: "127.0.0.1", Port: int64(listener.Addr().(*net.TCPAddr).Port), MachineID: binding.MachineID, Transport: "ssh", Username: "from-response", HostTrust: instabox.HostTrust{Type: "ca", PublicKey: string(ssh.MarshalAuthorizedKey(trustAuthority.PublicKey())), Principals: []string{"gateway"}}}
		if mode == "machine" {
			connection.MachineID = "other"
		}
		if mode == "transport" {
			connection.Transport = "other"
		}
		return connection, nil
	}
	return fixture
}

type memoryFrames struct {
	incoming chan outputFrame
	outgoing chan outputFrame
	closed   chan struct{}
	once     sync.Once
	mu       sync.Mutex
	deadline time.Time
	slow     bool
}

func newFrames() *memoryFrames {
	return &memoryFrames{incoming: make(chan outputFrame, 16), outgoing: make(chan outputFrame, 16), closed: make(chan struct{})}
}

func (frames *memoryFrames) ReadFrame(ctx context.Context, limit int) (bool, []byte, error) {
	select {
	case <-ctx.Done():
		return false, nil, ctx.Err()
	case <-frames.closed:
		return false, nil, io.EOF
	case frame := <-frames.incoming:
		return frame.binary, frame.payload, nil
	}
}

func (frames *memoryFrames) WriteFrame(ctx context.Context, binary bool, payload []byte) error {
	frames.mu.Lock()
	deadline := frames.deadline
	frames.mu.Unlock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	if frames.slow && binary {
		select {
		case <-timer.C:
			return context.DeadlineExceeded
		case <-ctx.Done():
			return ctx.Err()
		case <-frames.closed:
			return io.ErrClosedPipe
		}
	}
	select {
	case frames.outgoing <- outputFrame{binary: binary, payload: append([]byte(nil), payload...)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-frames.closed:
		return io.ErrClosedPipe
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func (frames *memoryFrames) SetWriteDeadline(deadline time.Time) error {
	frames.mu.Lock()
	frames.deadline = deadline
	frames.mu.Unlock()
	return nil
}

func (frames *memoryFrames) Close() error {
	frames.once.Do(func() { close(frames.closed) })
	return nil
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}

func runTerminal(t *testing.T, manager *Manager, binding Binding, frames *memoryFrames) <-chan error {
	t.Helper()
	ticket, err := manager.IssueTicket(binding)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Serve(context.Background(), ticket, binding.SessionHash, frames) }()
	return done
}

func ready(t *testing.T, frames *memoryFrames) {
	t.Helper()
	frame := receive(t, frames.outgoing)
	var control controlFrame
	if frame.binary || json.Unmarshal(frame.payload, &control) != nil || control.Type != "ready" {
		t.Fatalf("unexpected ready: %s", frame.payload)
	}
}

func TestSSHValidation(t *testing.T) {
	for _, mode := range []string{"ca", "principal", "expired", "plain", "user-key", "machine", "transport"} {
		t.Run(mode, func(t *testing.T) {
			fixture := startSSH(t, mode)
			manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true})
			client, err := manager.connect(context.Background(), testBinding())
			if client != nil {
				_ = client.Close()
			}
			if err == nil {
				t.Fatal("accepted invalid identity")
			}
		})
	}
	t.Run("default-ssrf", func(t *testing.T) {
		fixture := startSSH(t, "")
		manager := newManager(t, fixture.upstream, Options{})
		client, err := manager.connect(context.Background(), testBinding())
		if client != nil {
			_ = client.Close()
		}
		if err == nil {
			t.Fatal("accepted loopback")
		}
	})
}

func TestBridge(t *testing.T) {
	fixture := startSSH(t, "")
	manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true})
	frames := newFrames()
	done := runTerminal(t, manager, testBinding(), frames)
	ready(t, frames)
	frames.incoming <- outputFrame{binary: true, payload: []byte("hello")}
	frame := receive(t, frames.outgoing)
	if !frame.binary || string(frame.payload) != "hello" {
		t.Fatal("echo mismatch")
	}
	frames.incoming <- outputFrame{payload: []byte(`{"type":"resize","cols":100,"rows":40}`)}
	var dimensions struct {
		Cols   uint32
		Rows   uint32
		Width  uint32
		Height uint32
	}
	if ssh.Unmarshal(receive(t, fixture.resize), &dimensions) != nil || dimensions.Cols != 100 || dimensions.Rows != 40 {
		t.Fatal(dimensions)
	}
	frames.incoming <- outputFrame{payload: []byte(`{"type":"resize","cols":0,"rows":40}`)}
	if err := receive(t, done); err != ProtocolError {
		t.Fatal(err)
	}
	manager.mu.Lock()
	remaining := len(manager.active)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatal("quota leaked")
	}
}

func TestLimitsAndRevocation(t *testing.T) {
	for _, mode := range []string{"oversize", "slow", "idle", "absolute", "session", "credential", "shutdown", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			serverMode := ""
			if mode == "slow" {
				serverMode = "flood"
			}
			fixture := startSSH(t, serverMode)
			options := Options{AllowLoopbackForTests: true, WriteTimeout: 20 * time.Millisecond}
			if mode == "idle" {
				options.IdleTimeout = 30 * time.Millisecond
			}
			if mode == "absolute" {
				options.AbsoluteTimeout = 200 * time.Millisecond
			}
			manager := newManager(t, fixture.upstream, options)
			frames := newFrames()
			frames.slow = mode == "slow"
			binding := testBinding()
			done := runTerminal(t, manager, binding, frames)
			ready(t, frames)
			switch mode {
			case "oversize":
				frames.incoming <- outputFrame{binary: true, payload: make([]byte, 32769)}
			case "session":
				manager.CloseBySession(binding.SessionHash)
			case "credential":
				manager.CloseByCredential(binding.CredentialID)
			case "shutdown":
				manager.Shutdown()
			case "disconnect":
				_ = frames.Close()
			}
			err := receive(t, done)
			if err == nil {
				t.Fatal("expected closure")
			}
			if mode == "oversize" && err != ProtocolError {
				t.Fatal(err)
			}
			manager.mu.Lock()
			remaining := len(manager.active)
			manager.mu.Unlock()
			if remaining != 0 {
				t.Fatal("active session leaked")
			}
		})
	}
}

func TestUpstreamFailuresAndRetry(t *testing.T) {
	fixture := startSSH(t, "")
	original := fixture.upstream.create
	var firstBody []byte
	var firstKey string
	attempts := 0
	fixture.upstream.create = func(ctx context.Context, binding Binding, request instabox.CreateConnectionRequest, key string) (instabox.Connection, error) {
		attempts++
		body, _ := json.Marshal(request)
		if attempts == 1 {
			firstBody = body
			firstKey = key
			return instabox.Connection{}, io.ErrUnexpectedEOF
		}
		if !bytes.Equal(body, firstBody) || key != firstKey || key == "" {
			t.Error("retry changed identity")
		}
		return original(ctx, binding, request, key)
	}
	manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true})
	client, err := manager.connect(context.Background(), testBinding())
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if attempts != 2 {
		t.Fatal(attempts)
	}
	fixture.upstream.create = func(context.Context, Binding, instabox.CreateConnectionRequest, string) (instabox.Connection, error) {
		return instabox.Connection{}, &instabox.APIError{StatusCode: 409, Code: "machine_not_running"}
	}
	if _, err = manager.connect(context.Background(), testBinding()); err != MachineNotRunning {
		t.Fatal(err)
	}
	fixture.upstream.info.Capabilities = nil
	if _, err = manager.connect(context.Background(), testBinding()); err != PTYUnsupported {
		t.Fatal(err)
	}
	for _, ttl := range []int64{-1, 86401} {
		if _, err = New(fixture.upstream, Options{CertificateTTL: ttl}); err == nil {
			t.Fatal("invalid TTL")
		}
	}
}

func TestQuota(t *testing.T) {
	fixture := startSSH(t, "")
	manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true})
	binding := testBinding()
	var running []<-chan error
	for index := 0; index < 4; index++ {
		current := binding
		if index >= 2 {
			current.SessionHash = sha256.Sum256([]byte("second"))
		}
		frames := newFrames()
		running = append(running, runTerminal(t, manager, current, frames))
		ready(t, frames)
		if index == 1 {
			extra := newFrames()
			if err := receive(t, runTerminal(t, manager, binding, extra)); err != QuotaExceeded {
				t.Fatal(err)
			}
		}
	}
	third := binding
	third.SessionHash = sha256.Sum256([]byte("third"))
	if err := receive(t, runTerminal(t, manager, third, newFrames())); err != QuotaExceeded {
		t.Fatal(err)
	}
	manager.Shutdown()
	for _, done := range running {
		receive(t, done)
	}
}

func TestExitAndTicketRevocation(t *testing.T) {
	fixture := startSSH(t, "")
	manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true, MaxTickets: 2})
	binding := testBinding()
	first, _ := manager.IssueTicket(binding)
	second, _ := manager.IssueTicket(binding)
	if _, err := manager.IssueTicket(binding); err != QuotaExceeded {
		t.Fatal(err)
	}
	manager.CloseBySession(binding.SessionHash)
	if _, err := manager.consume(first, binding.SessionHash); err != InvalidTicket {
		t.Fatal(err)
	}
	if _, err := manager.consume(second, binding.SessionHash); err != InvalidTicket {
		t.Fatal(err)
	}
	ticket, _ := manager.IssueTicket(binding)
	manager.CloseByCredential(binding.CredentialID)
	if _, err := manager.consume(ticket, binding.SessionHash); err != InvalidTicket {
		t.Fatal(err)
	}
	frames := newFrames()
	done := runTerminal(t, manager, binding, frames)
	ready(t, frames)
	fixture.finish <- 7
	frames.incoming <- outputFrame{payload: []byte(`{"type":"resize","cols":80,"rows":24}`)}
	receive(t, fixture.resize)
	for {
		frame := receive(t, frames.outgoing)
		if frame.binary {
			continue
		}
		var control controlFrame
		if json.Unmarshal(frame.payload, &control) != nil || control.Type != "exit" || control.ExitCode == nil || *control.ExitCode != 7 {
			t.Fatalf("bad exit: %s", frame.payload)
		}
		break
	}
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	manager.Shutdown()
	if _, err := manager.IssueTicket(binding); err != Closed {
		t.Fatal(err)
	}
}

func TestOutputBufferBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan outputFrame, 8)
	var activity atomic.Int64
	writer := outputWriter{ctx: ctx, frames: frames, activity: &activity, now: time.Now, chunk: 1024}
	done := make(chan error, 1)
	go func() { _, err := writer.Write(make([]byte, 1<<20)); done <- err }()
	for index := 0; index < 8; index++ {
		frame := receive(t, frames)
		if len(frame.payload) > 1024 {
			t.Fatal("unbounded frame")
		}
	}
	cancel()
	if err := receive(t, done); err == nil {
		t.Fatal("writer ignored cancellation")
	}
	if len(frames) > 8 {
		t.Fatal("unbounded buffer")
	}
}

func TestResizeThrottle(t *testing.T) {
	fixture := startSSH(t, "")
	manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true, ResizeInterval: time.Hour})
	frames := newFrames()
	done := runTerminal(t, manager, testBinding(), frames)
	ready(t, frames)
	resize := outputFrame{payload: []byte(`{"type":"resize","cols":90,"rows":30}`)}
	frames.incoming <- resize
	receive(t, fixture.resize)
	frames.incoming <- resize
	frames.incoming <- outputFrame{binary: true, payload: []byte("barrier")}
	if string(receive(t, frames.outgoing).payload) != "barrier" {
		t.Fatal("barrier")
	}
	select {
	case <-fixture.resize:
		t.Fatal("resize was not throttled")
	default:
	}
	manager.Shutdown()
	receive(t, done)
}

func TestSafeFailureFormatting(t *testing.T) {
	secret := "private-upstream-secret"
	upstream := &fakeUpstream{info: instabox.ConnectionInfo{Capabilities: []string{"pty"}}, create: func(context.Context, Binding, instabox.CreateConnectionRequest, string) (instabox.Connection, error) {
		return instabox.Connection{}, errors.New(secret)
	}}
	manager := newManager(t, upstream, Options{})
	frames := newFrames()
	err := receive(t, runTerminal(t, manager, testBinding(), frames))
	if err != Unavailable {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(format, err), secret) || strings.Contains(fmt.Sprintf(format, manager), secret) {
			t.Fatal("secret leaked")
		}
	}
	control := receive(t, frames.outgoing)
	if strings.Contains(string(control.payload), secret) {
		t.Fatal("secret in control")
	}
}

func TestOwnedGoroutinesFinish(t *testing.T) {
	t.Run("termination-paths", TestLimitsAndRevocation)
	t.Run("identity-failures", TestSSHValidation)
	t.Run("normal-exit", TestExitAndTicketRevocation)
	stack := make([]byte, 1<<20)
	stack = stack[:runtime.Stack(stack, true)]
	for _, function := range []string{
		"terminal.(*Manager).cleanTickets(",
		"terminal.(*Manager).Serve.func",
		"terminal.outputWriter.Write(",
		"terminal.watchCancellation.func",
		"terminal.watchTimeout.func",
	} {
		if bytes.Contains(stack, []byte(function)) {
			t.Fatalf("terminal goroutine still running: %s", function)
		}
	}
}
