package identity

import (
	"net/netip"
	"sync"
	"time"
)

type bucket struct {
	start time.Time
	count int
}
type Limiter struct {
	mu                 sync.Mutex
	window             time.Duration
	perIP, globalLimit int
	ips                map[string]bucket
	global             bucket
}

func NewLimiter(window time.Duration, perIP, global int) *Limiter {
	return &Limiter{window: window, perIP: perIP, globalLimit: global, ips: make(map[string]bucket)}
}
func (l *Limiter) Allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.window <= 0 || l.perIP < 1 || l.globalLimit < 1 {
		return false
	}
	addr, e := netip.ParseAddr(ip)
	if e != nil {
		return false
	}
	ip = addr.Unmap().String()
	for k, b := range l.ips {
		if !now.Before(b.start.Add(l.window)) {
			delete(l.ips, k)
		}
	}
	if !now.Before(l.global.start.Add(l.window)) {
		l.global = bucket{start: now}
	}
	b := l.ips[ip]
	if b.start.IsZero() {
		b.start = now
	}
	allowed := b.count < l.perIP && l.global.count < l.globalLimit
	// A blocked address must not consume the global budget, or one address
	// could lock out every other client.
	if allowed {
		b.count++
		l.global.count++
	}
	l.ips[ip] = b
	return allowed
}
