package terminal

import "context"

func (manager *Manager) ServeDesktop(ctx context.Context, ticket Ticket, hash [32]byte, serve func(context.Context, Binding) error) error {
	manager.mu.Lock()
	binding, err := manager.consumePurposeLocked(ticket, hash, true)
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	count := 0
	for current := range manager.active {
		if current.binding.SessionHash == hash {
			count++
		}
	}
	if manager.closed || count >= manager.options.PerSessionLimit || len(manager.active) >= manager.options.GlobalLimit {
		manager.mu.Unlock()
		return QuotaExceeded
	}
	ctx, cancel := context.WithTimeout(ctx, manager.options.AbsoluteTimeout)
	active := &activeSession{binding: binding, cancel: cancel, done: make(chan struct{})}
	manager.active[active] = struct{}{}
	manager.mu.Unlock()
	defer func() {
		cancel()
		manager.mu.Lock()
		delete(manager.active, active)
		close(active.done)
		manager.mu.Unlock()
	}()
	return serve(ctx, binding)
}
