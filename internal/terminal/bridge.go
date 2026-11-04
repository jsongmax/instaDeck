package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

type controlFrame struct {
	Type     string `json:"type"`
	Cols     int    `json:"cols,omitempty"`
	Rows     int    `json:"rows,omitempty"`
	Code     Code   `json:"code,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

type outputFrame struct {
	binary   bool
	payload  []byte
	complete chan error
}

type outputWriter struct {
	ctx      context.Context
	frames   chan<- outputFrame
	activity *atomic.Int64
	now      func() time.Time
	chunk    int
}

func (writer outputWriter) Write(payload []byte) (int, error) {
	written := 0
	for len(payload) > 0 {
		size := min(len(payload), writer.chunk)
		frame := outputFrame{binary: true, payload: append([]byte(nil), payload[:size]...)}
		select {
		case <-writer.ctx.Done():
			return written, io.ErrClosedPipe
		case writer.frames <- frame:
		}
		writer.activity.Store(writer.now().UnixNano())
		written += size
		payload = payload[size:]
	}
	return written, nil
}

// Serve consumes a ticket and runs one terminal until completion. The adapter
// must authenticate cookies and Origin before calling Serve.
func (manager *Manager) Serve(ctx context.Context, ticket Ticket, sessionHash [32]byte, frames FrameConn) error {
	defer frames.Close()
	return manager.ServeUpgrade(ctx, ticket, sessionHash, func(Binding) (FrameConn, error) { return frames, nil })
}

// ServeUpgrade consumes and registers a ticket before allowing the HTTP upgrade.
func (manager *Manager) ServeUpgrade(ctx context.Context, ticket Ticket, sessionHash [32]byte, upgrade func(Binding) (FrameConn, error)) error {
	manager.mu.Lock()
	binding, err := manager.consumeLocked(ticket, sessionHash)
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, manager.options.AbsoluteTimeout)
	if ctx.Err() != nil {
		manager.mu.Unlock()
		cancel()
		return TimedOut
	}
	active := &activeSession{binding: binding, cancel: cancel, done: make(chan struct{})}
	count := 0
	for current := range manager.active {
		if current.binding.SessionHash == sessionHash {
			count++
		}
	}
	if manager.closed || count >= manager.options.PerSessionLimit || len(manager.active) >= manager.options.GlobalLimit {
		manager.mu.Unlock()
		cancel()
		return QuotaExceeded
	}
	manager.active[active] = struct{}{}
	manager.mu.Unlock()
	var frames FrameConn
	defer func() {
		cancel()
		if frames != nil {
			_ = frames.Close()
		}
		manager.mu.Lock()
		delete(manager.active, active)
		close(active.done)
		manager.mu.Unlock()
	}()
	frames, err = upgrade(binding)
	if err != nil {
		return err
	}
	stopFrame := watchCancellation(ctx, func() { _ = frames.Close() })
	defer stopFrame()
	client, err := manager.connect(ctx, binding)
	if err != nil {
		manager.sendControl(ctx, frames, controlFrame{Type: "error", Code: err.(Code)})
		return err
	}
	defer client.Close()
	stopClient := watchCancellation(ctx, func() { _ = client.Close() })
	defer stopClient()
	setupTimer := watchTimeout(manager.options.DialTimeout, func() { _ = client.Close() })
	defer setupTimer()
	session, err := client.NewSession()
	if err != nil {
		manager.note("ssh_session", err)
		_ = manager.sendControl(ctx, frames, controlFrame{Type: "error", Code: Unavailable})
		return Unavailable
	}
	defer session.Close()
	output := make(chan outputFrame, 8)
	var activity atomic.Int64
	activity.Store(manager.options.Now().UnixNano())
	writer := outputWriter{ctx: ctx, frames: output, activity: &activity, now: manager.options.Now, chunk: manager.options.MaxFrameBytes}
	session.Stdout = writer
	session.Stderr = writer
	input, err := manager.startSession(ctx, frames, session)
	if err != nil {
		return err
	}
	sessionResult := make(chan error, 1)
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		sessionResult <- session.Wait()
	}()
	defer func() {
		cancel()
		_ = client.Close()
		<-sessionDone
	}()
	if manager.sendControl(ctx, frames, controlFrame{Type: "ready"}) != nil {
		return Unavailable
	}
	setupTimer()
	var writeMu sync.Mutex
	var workers sync.WaitGroup
	results := make(chan error, 4)
	launch := func(work func() error) { workers.Add(1); go func() { defer workers.Done(); results <- work() }() }
	launch(func() error {
		for {
			select {
			case <-ctx.Done():
				return Closed
			case frame := <-output:
				writeMu.Lock()
				failure := frames.SetWriteDeadline(time.Now().Add(manager.options.WriteTimeout))
				if failure == nil {
					failure = frames.WriteFrame(ctx, frame.binary, frame.payload)
				}
				writeMu.Unlock()
				if frame.complete != nil {
					frame.complete <- failure
				}
				if failure != nil {
					return Unavailable
				}
			}
		}
	})
	launch(func() error {
		lastResize := time.Time{}
		window := manager.options.Now()
		used := 0
		for {
			binary, payload, err := frames.ReadFrame(ctx, manager.options.MaxFrameBytes)
			if err != nil {
				return Closed
			}
			now := manager.options.Now()
			if len(payload) > manager.options.MaxFrameBytes {
				return ProtocolError
			}
			activity.Store(now.UnixNano())
			for offset := 0; offset < len(payload); {
				now = manager.options.Now()
				if now.Sub(window) >= time.Second {
					window = now
					used = 0
				}
				if used == manager.options.InputBytesPerSecond {
					if manager.options.Wait(ctx, max(time.Second-now.Sub(window), 0)) != nil {
						return Closed
					}
					continue
				}
				size := min(len(payload)-offset, manager.options.InputBytesPerSecond-used)
				if binary {
					if _, err = input.Write(payload[offset : offset+size]); err != nil {
						return Unavailable
					}
				}
				used += size
				offset += size
			}
			if binary {
				continue
			}
			var control controlFrame
			if json.Unmarshal(payload, &control) != nil || control.Type != "resize" || control.Cols < 1 || control.Cols > 500 || control.Rows < 1 || control.Rows > 500 {
				return ProtocolError
			}
			if !lastResize.IsZero() && now.Sub(lastResize) < manager.options.ResizeInterval {
				continue
			}
			lastResize = now
			if session.WindowChange(control.Rows, control.Cols) != nil {
				return Unavailable
			}
		}
	})
	exitCode := make(chan int, 1)
	launch(func() error {
		err := <-sessionResult
		code := 0
		if err != nil {
			var exit *ssh.ExitError
			if !errors.As(err, &exit) {
				return Unavailable
			}
			code = exit.ExitStatus()
		}
		exitCode <- code
		return nil
	})
	launch(func() error {
		interval := min(manager.options.HeartbeatInterval, manager.options.IdleTimeout/2)
		ticker := time.NewTicker(max(interval, time.Millisecond))
		defer ticker.Stop()
		lastBeat := manager.options.Now()
		for {
			select {
			case <-ctx.Done():
				return TimedOut
			case <-ticker.C:
				now := manager.options.Now()
				if now.Sub(time.Unix(0, activity.Load())) >= manager.options.IdleTimeout {
					return TimedOut
				}
				if now.Sub(lastBeat) >= manager.options.HeartbeatInterval {
					lastBeat = now
					heartbeatTimer := watchTimeout(min(manager.options.DialTimeout, manager.options.IdleTimeout), func() { _ = client.Close() })
					_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
					stopped := heartbeatTimer()
					if !stopped || err != nil {
						return Unavailable
					}
				}
			}
		}
	})
	select {
	case err = <-results:
	case <-ctx.Done():
		err = TimedOut
	}
	if err == nil {
		code := <-exitCode
		payload, _ := json.Marshal(controlFrame{Type: "exit", ExitCode: &code})
		complete := make(chan error, 1)
		select {
		case output <- outputFrame{payload: payload, complete: complete}:
			select {
			case err = <-complete:
			case err = <-results:
			case <-ctx.Done():
				err = TimedOut
			}
		case err = <-results:
		case <-ctx.Done():
			err = TimedOut
		}
	} else if code, ok := err.(Code); ok && ctx.Err() == nil {
		writeMu.Lock()
		_ = manager.sendControl(ctx, frames, controlFrame{Type: "error", Code: code})
		writeMu.Unlock()
	}
	cancel()
	_ = frames.Close()
	_ = client.Close()
	_ = session.Close()
	workers.Wait()
	close(output)
	select {
	case <-exitCode:
	default:
	}
	return err
}

func (manager *Manager) sendControl(ctx context.Context, frames FrameConn, control controlFrame) error {
	payload, _ := json.Marshal(control)
	if err := frames.SetWriteDeadline(time.Now().Add(manager.options.WriteTimeout)); err != nil {
		return Unavailable
	}
	if frames.WriteFrame(ctx, false, payload) != nil {
		return Unavailable
	}
	return nil
}

type sessionStarter interface {
	StdinPipe() (io.WriteCloser, error)
	RequestPty(string, int, int, ssh.TerminalModes) error
	Shell() error
}

func (manager *Manager) startSession(ctx context.Context, frames FrameConn, session sessionStarter) (io.WriteCloser, error) {
	input, err := session.StdinPipe()
	if err == nil {
		err = session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{})
	}
	if err == nil {
		err = session.Shell()
	}
	if err != nil {
		manager.note("ssh_shell", err)
		_ = manager.sendControl(ctx, frames, controlFrame{Type: "error", Code: Unavailable})
		return nil, Unavailable
	}
	return input, nil
}
