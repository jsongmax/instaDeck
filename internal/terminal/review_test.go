package terminal

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/netip"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRebindSession(t *testing.T) {
	fixture := startSSH(t, "")
	manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true, PerSessionLimit: 1})
	binding := testBinding()
	frames := newFrames()
	done := runTerminal(t, manager, binding, frames)
	ready(t, frames)
	ticket, _ := manager.IssueTicket(binding)
	oldTicket, _ := manager.IssueTicket(binding)
	rebinder, ok := any(manager).(interface{ RebindSession([32]byte, [32]byte) })
	if !ok {
		t.Fatal("RebindSession is missing; session rotation cannot migrate terminals")
	}
	newHash := sha256.Sum256([]byte("rotated"))
	rebinder.RebindSession(binding.SessionHash, newHash)
	manager.CloseBySession(binding.SessionHash)
	select {
	case <-done:
		t.Fatal("old hash still closes terminal")
	default:
	}
	rebound, err := manager.consume(ticket, newHash)
	if err != nil || rebound.SessionHash != newHash {
		t.Fatal("ticket did not migrate", err)
	}
	if _, err = manager.consume(oldTicket, binding.SessionHash); err != InvalidTicket {
		t.Fatal("old hash accepted")
	}
	updated := binding
	updated.SessionHash = newHash
	if err = receive(t, runTerminal(t, manager, updated, newFrames())); err != QuotaExceeded {
		t.Fatal("quota did not migrate", err)
	}
	pending, _ := manager.IssueTicket(updated)
	manager.CloseBySession(newHash)
	receive(t, done)
	if _, err = manager.consume(pending, newHash); err != InvalidTicket {
		t.Fatal("logout did not revoke ticket")
	}
}

func TestInputBackpressure(t *testing.T) {
	for _, cancelWaiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "cancel"}[cancelWaiting], func(t *testing.T) {
			fixture := startSSH(t, "")
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			entered := make(chan time.Duration, 4)
			release := make(chan struct{})
			options := Options{AllowLoopbackForTests: true, InputBytesPerSecond: 4, Now: func() time.Time { return time.Unix(0, clock.Load()) }}
			wait := func(ctx context.Context, duration time.Duration) error {
				entered <- duration
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
					clock.Add(int64(duration))
					return nil
				}
			}
			field := reflect.ValueOf(&options).Elem().FieldByName("Wait")
			if field.IsValid() {
				field.Set(reflect.ValueOf(wait))
			}
			manager := newManager(t, fixture.upstream, options)
			frames := newFrames()
			done := runTerminal(t, manager, testBinding(), frames)
			ready(t, frames)
			frames.incoming <- outputFrame{binary: true, payload: []byte("abcdefghij")}
			select {
			case duration := <-entered:
				if duration != time.Second {
					t.Fatal(duration)
				}
			case err := <-done:
				t.Fatalf("paste disconnected instead of waiting: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("limiter did not wait")
			}
			if cancelWaiting {
				manager.CloseBySession(testBinding().SessionHash)
				receive(t, done)
				return
			}
			close(release)
			var echoed []byte
			for len(echoed) < 10 {
				frame := receive(t, frames.outgoing)
				if !frame.binary {
					t.Fatalf("unexpected control: %s", frame.payload)
				}
				echoed = append(echoed, frame.payload...)
			}
			if string(echoed) != "abcdefghij" {
				t.Fatal(string(echoed))
			}
			receive(t, entered)
			manager.Shutdown()
			receive(t, done)
		})
	}
}

func TestDefaultInputRate(t *testing.T) {
	manager := newManager(t, &fakeUpstream{}, Options{})
	if manager.options.InputBytesPerSecond != 1<<20 {
		t.Fatalf("default rate = %d, want 1048576", manager.options.InputBytesPerSecond)
	}
}

func TestReservedAddresses(t *testing.T) {
	for _, address := range []string{"100.64.0.0", "100.100.100.200", "100.127.255.255", "192.0.0.1", "192.0.0.255", "198.18.0.0", "198.19.255.255", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1", "3fff::1"} {
		t.Run(address, func(t *testing.T) {
			parsed := netip.MustParseAddr(address)
			if safeAddress(parsed, false) {
				t.Errorf("reserved address accepted: %s", address)
			}
			if parsed.Is4() {
				mapped := netip.AddrFrom16(parsed.As16())
				if safeAddress(mapped, false) {
					t.Errorf("mapped reserved address accepted: %s", mapped)
				}
			}
		})
	}
	for _, address := range []string{"100.63.255.255", "100.128.0.0", "192.0.1.1", "198.17.255.255", "198.20.0.0", "8.8.8.8", "2606:4700:4700::1111"} {
		if !safeAddress(netip.MustParseAddr(address), false) {
			t.Errorf("public address rejected: %s", address)
		}
	}
}

func TestSessionSetupErrorFrame(t *testing.T) {
	for _, mode := range []string{"reject-session", "reject-pty", "reject-shell"} {
		t.Run(mode, func(t *testing.T) {
			fixture := startSSH(t, mode)
			manager := newManager(t, fixture.upstream, Options{AllowLoopbackForTests: true})
			frames := newFrames()
			done := runTerminal(t, manager, testBinding(), frames)
			if err := receive(t, done); err != Unavailable {
				t.Fatal(err)
			}
			select {
			case frame := <-frames.outgoing:
				if frame.binary || string(frame.payload) != `{"type":"error","code":"terminal_unavailable"}` {
					t.Fatalf("unexpected error frame: %s", frame.payload)
				}
			default:
				t.Fatal("session setup failed without browser error frame")
			}
		})
	}
}

type failedStdinSession struct{}

func (session failedStdinSession) StdinPipe() (io.WriteCloser, error) {
	return nil, errors.New("private-pipe-detail")
}

func (session failedStdinSession) RequestPty(string, int, int, ssh.TerminalModes) error {
	panic("PTY requested after failed pipe")
}

func (session failedStdinSession) Shell() error { panic("shell requested after failed pipe") }

func TestStdinPipeErrorFrame(t *testing.T) {
	manager := newManager(t, &fakeUpstream{}, Options{})
	frames := newFrames()
	if _, err := manager.startSession(context.Background(), frames, failedStdinSession{}); err != Unavailable {
		t.Fatal(err)
	}
	frame := receive(t, frames.outgoing)
	if frame.binary || string(frame.payload) != `{"type":"error","code":"terminal_unavailable"}` {
		t.Fatalf("unexpected error frame: %s", frame.payload)
	}
}

func TestAddressFilterSwitch(t *testing.T) {
	manager := &Manager{}
	fakeIP := netip.MustParseAddr("198.18.0.216")
	public := netip.MustParseAddr("1.1.1.1")
	if manager.addressAllowed(fakeIP) || !manager.addressAllowed(public) {
		t.Fatal("the filter must be on by default")
	}
	manager.SetAddressFilter(false)
	if !manager.addressAllowed(fakeIP) {
		t.Fatal("the switch did not turn the filter off")
	}
	manager.SetAddressFilter(true)
	if manager.addressAllowed(fakeIP) {
		t.Fatal("the switch did not turn the filter back on")
	}
}
