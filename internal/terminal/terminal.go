// Package terminal implements single-use tickets and bounded SSH terminal sessions.
package terminal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"instadeck/internal/instabox"
)

// Code is a public, secret-free terminal failure code.
type Code string

// Error returns the fixed failure code.
func (code Code) Error() string { return string(code) }

const (
	// InvalidTicket indicates an expired, consumed or incorrectly bound ticket.
	InvalidTicket Code = "invalid_ticket"
	// Unavailable indicates an upstream or SSH failure.
	Unavailable Code = "terminal_unavailable"
	// InvalidConnection indicates an invalid certificate or connection response.
	InvalidConnection Code = "invalid_connection"
	// MachineNotRunning indicates that the machine cannot accept a connection.
	MachineNotRunning Code = "machine_not_running"
	// AddressBlocked indicates that the SSH host resolved to a refused address,
	// usually a local proxy's fake-IP DNS rather than a hostile response.
	AddressBlocked Code = "address_blocked"
	// PTYUnsupported indicates that the machine lacks PTY support.
	PTYUnsupported Code = "pty_unsupported"
	// QuotaExceeded indicates that terminal capacity is exhausted.
	QuotaExceeded Code = "terminal_limit"
	// ProtocolError indicates invalid or oversized frames.
	ProtocolError Code = "terminal_protocol"
	// TimedOut indicates an idle or absolute session deadline.
	TimedOut Code = "terminal_timeout"
	// Closed indicates shutdown or revocation.
	Closed Code = "terminal_closed"
)

// Binding identifies the authenticated session and exact credential version.
type Binding struct {
	SessionHash       [32]byte
	CredentialID      string
	CredentialVersion int64
	MachineID         string
	OrgID             string
}

// Ticket is an opaque bearer value. Only its hash is retained by Manager.
type Ticket string

// String redacts the ticket.
func (ticket Ticket) String() string { return "Ticket[redacted]" }

// GoString redacts the ticket in Go formatting.
func (ticket Ticket) GoString() string { return ticket.String() }

// Value returns the bearer value for delivery through the authenticated adapter.
func (ticket Ticket) Value() string { return string(ticket) }

// Upstream is adapted by the caller to the binding's exact credentials and org.
type Upstream interface {
	GetConnectionInfo(context.Context, Binding) (instabox.ConnectionInfo, error)
	CreateConnection(context.Context, Binding, instabox.CreateConnectionRequest, string) (instabox.Connection, error)
}

// FrameConn must bound reads before allocation, unblock reads/writes on Close,
// honor contexts and write deadlines, and permit one reader and one writer.
type FrameConn interface {
	ReadFrame(context.Context, int) (binary bool, payload []byte, err error)
	WriteFrame(context.Context, bool, []byte) error
	SetWriteDeadline(time.Time) error
	Close() error
}

// Options controls terminal resource bounds. Zero values select safe defaults.
type Options struct {
	TicketTTL             time.Duration
	CertificateTTL        int64
	DialTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	AbsoluteTimeout       time.Duration
	HeartbeatInterval     time.Duration
	ResizeInterval        time.Duration
	MaxFrameBytes         int
	InputBytesPerSecond   int
	PerSessionLimit       int
	GlobalLimit           int
	MaxTickets            int
	AllowLoopbackForTests bool
	// Logger receives connection failure steps. It never receives keys,
	// certificates or terminal content.
	Logger *slog.Logger
	Now    func() time.Time
	// Wait waits for input capacity and must return when the context is canceled.
	Wait func(context.Context, time.Duration) error
}

type ticketEntry struct {
	binding Binding
	expires time.Time
	desktop bool
}
type activeSession struct {
	binding Binding
	cancel  context.CancelFunc
	done    chan struct{}
}

// Manager owns all tickets and active terminals without package-level state.
type Manager struct {
	mu          sync.Mutex
	upstream    Upstream
	options     Options
	tickets     map[[32]byte]ticketEntry
	active      map[*activeSession]struct{}
	closed      bool
	stop        chan struct{}
	janitorDone chan struct{}
	// filterOff is inverted so the zero value keeps the protection on.
	filterOff atomic.Bool
}

// SetAddressFilter turns the check on SSH target addresses on or off.
func (manager *Manager) SetAddressFilter(enabled bool) { manager.filterOff.Store(!enabled) }

func (manager *Manager) addressAllowed(address netip.Addr) bool {
	return manager.filterOff.Load() || safeAddress(address, manager.options.AllowLoopbackForTests)
}

// String hides all runtime state.
func (manager *Manager) String() string { return "terminal.Manager[redacted]" }

// GoString hides all runtime state in Go formatting.
func (manager *Manager) GoString() string { return manager.String() }

// New constructs a manager; Shutdown must be called by its owner.
func New(upstream Upstream, options Options) (*Manager, error) {
	if options.TicketTTL == 0 {
		options.TicketTTL = 30 * time.Second
	}
	if options.CertificateTTL == 0 {
		options.CertificateTTL = 300
	}
	if options.DialTimeout == 0 {
		options.DialTimeout = 10 * time.Second
	}
	if options.WriteTimeout == 0 {
		options.WriteTimeout = 5 * time.Second
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = 30 * time.Minute
	}
	if options.AbsoluteTimeout == 0 {
		options.AbsoluteTimeout = 8 * time.Hour
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = 30 * time.Second
	}
	if options.ResizeInterval == 0 {
		options.ResizeInterval = 100 * time.Millisecond
	}
	if options.MaxFrameBytes == 0 {
		options.MaxFrameBytes = 32768
	}
	if options.InputBytesPerSecond == 0 {
		options.InputBytesPerSecond = 1 << 20
	}
	if options.PerSessionLimit == 0 {
		options.PerSessionLimit = 2
	}
	if options.GlobalLimit == 0 {
		options.GlobalLimit = 4
	}
	if options.MaxTickets == 0 {
		options.MaxTickets = 1024
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Wait == nil {
		options.Wait = waitForInput
	}
	if upstream == nil || options.CertificateTTL < 1 || options.CertificateTTL > 86400 || options.TicketTTL <= 0 || options.DialTimeout <= 0 || options.WriteTimeout <= 0 || options.IdleTimeout <= 0 || options.AbsoluteTimeout <= 0 || options.HeartbeatInterval <= 0 || options.ResizeInterval <= 0 || options.MaxFrameBytes < 1 || options.InputBytesPerSecond < 1 || options.PerSessionLimit < 1 || options.GlobalLimit < 1 || options.MaxTickets < 1 {
		return nil, InvalidConnection
	}
	manager := &Manager{upstream: upstream, options: options, tickets: make(map[[32]byte]ticketEntry), active: make(map[*activeSession]struct{}), stop: make(chan struct{}), janitorDone: make(chan struct{})}
	go manager.cleanTickets()
	return manager, nil
}

func (manager *Manager) cleanTickets() {
	defer close(manager.janitorDone)
	interval := min(manager.options.TicketTTL, time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-manager.stop:
			return
		case <-ticker.C:
			manager.mu.Lock()
			manager.prune()
			manager.mu.Unlock()
		}
	}
}

func (manager *Manager) prune() {
	for hash, entry := range manager.tickets {
		if !manager.options.Now().Before(entry.expires) {
			delete(manager.tickets, hash)
		}
	}
}

// IssueTicket creates a short-lived, single-use ticket bound to trusted identity.
func (manager *Manager) IssueTicket(binding Binding) (Ticket, error) {
	return manager.issueTicket(binding, false)
}

func (manager *Manager) IssueDesktopTicket(binding Binding) (Ticket, error) {
	return manager.issueTicket(binding, true)
}

func (manager *Manager) issueTicket(binding Binding, desktop bool) (Ticket, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.prune()
	if manager.closed {
		return "", Closed
	}
	if binding.SessionHash == [32]byte{} || binding.CredentialID == "" || binding.CredentialVersion < 1 || binding.MachineID == "" || binding.OrgID == "" {
		return "", InvalidTicket
	}
	if len(manager.tickets) >= manager.options.MaxTickets {
		return "", QuotaExceeded
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", Unavailable
	}
	ticket := Ticket(base64.RawURLEncoding.EncodeToString(random[:]))
	ttl := manager.options.TicketTTL
	if desktop {
		ttl = 30 * time.Second
	}
	manager.tickets[sha256.Sum256([]byte(ticket))] = ticketEntry{binding: binding, expires: manager.options.Now().Add(ttl), desktop: desktop}
	return ticket, nil
}

func (manager *Manager) consume(ticket Ticket, sessionHash [32]byte) (Binding, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.consumeLocked(ticket, sessionHash)
}

func (manager *Manager) consumeLocked(ticket Ticket, sessionHash [32]byte) (Binding, error) {
	return manager.consumePurposeLocked(ticket, sessionHash, false)
}

func (manager *Manager) consumePurposeLocked(ticket Ticket, sessionHash [32]byte, desktop bool) (Binding, error) {
	hash := sha256.Sum256([]byte(ticket))
	entry, found := manager.tickets[hash]
	delete(manager.tickets, hash)
	if manager.closed {
		return Binding{}, Closed
	}
	if !found || entry.desktop != desktop || entry.binding.SessionHash != sessionHash || !manager.options.Now().Before(entry.expires) {
		return Binding{}, InvalidTicket
	}
	return entry.binding, nil
}

func (manager *Manager) revoke(matches func(Binding) bool, shutdown bool) {
	manager.mu.Lock()
	if shutdown && !manager.closed {
		manager.closed = true
		close(manager.stop)
	}
	for hash, entry := range manager.tickets {
		if matches(entry.binding) {
			delete(manager.tickets, hash)
		}
	}
	var pending []*activeSession
	for session := range manager.active {
		if matches(session.binding) {
			session.cancel()
			pending = append(pending, session)
		}
	}
	manager.mu.Unlock()
	for _, session := range pending {
		<-session.done
	}
}

// RebindSession atomically migrates active terminals and unused tickets after token rotation.
func (manager *Manager) RebindSession(oldHash, newHash [32]byte) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for hash, entry := range manager.tickets {
		if entry.binding.SessionHash == oldHash {
			entry.binding.SessionHash = newHash
			manager.tickets[hash] = entry
		}
	}
	for session := range manager.active {
		if session.binding.SessionHash == oldHash {
			session.binding.SessionHash = newHash
		}
	}
}

// CloseBySession revokes tickets and waits for matching terminals to finish.
func (manager *Manager) CloseBySession(hash [32]byte) {
	manager.revoke(func(binding Binding) bool { return binding.SessionHash == hash }, false)
}

// CloseByCredential revokes all versions of a credential and their terminals.
func (manager *Manager) CloseByCredential(id string) {
	manager.revoke(func(binding Binding) bool { return binding.CredentialID == id }, false)
}

// Shutdown revokes all tickets and waits for every owned goroutine.
func (manager *Manager) Shutdown() {
	manager.revoke(func(Binding) bool { return true }, true)
	<-manager.janitorDone
}

// String hides binding identifiers.
func (binding Binding) String() string { return "Binding[redacted]" }

// GoString hides binding identifiers.
func (binding Binding) GoString() string { return binding.String() }

// Format prevents ticket disclosure with every fmt verb, including %s and %q.
func (ticket Ticket) Format(state fmt.State, verb rune) { _, _ = state.Write([]byte(ticket.String())) }

func waitForCallback(stop func() bool, finished <-chan struct{}) func() bool {
	var once sync.Once
	var stopped bool
	return func() bool {
		once.Do(func() {
			stopped = stop()
			if !stopped {
				<-finished
			}
		})
		return stopped
	}
}

func watchCancellation(ctx context.Context, cleanup func()) func() bool {
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(finished); cleanup() })
	return waitForCallback(stop, finished)
}

func watchTimeout(duration time.Duration, cleanup func()) func() bool {
	finished := make(chan struct{})
	timer := time.AfterFunc(duration, func() { defer close(finished); cleanup() })
	return waitForCallback(timer.Stop, finished)
}

func waitForInput(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
