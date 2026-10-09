package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Sandbox creation limits. Creating a sandbox seeds a whole demo world, so
// it is the one expensive thing an anonymous visitor can trigger at will.
const (
	// createBurst sandboxes per client may be created back to back.
	createBurst = 5
	// createRefill adds one more allowance per client every interval.
	createRefill = time.Minute
	// globalCreatesPerHour caps creation across all clients.
	globalCreatesPerHour = 600
	// maxLimiterClients triggers a sweep of idle client entries.
	maxLimiterClients = 10_000
)

// bucket is a token bucket.
type bucket struct {
	tokens float64
	last   time.Time
}

// take refills b for the time elapsed since its last use and spends one
// token if there is one.
func (b *bucket) take(now time.Time, capacity float64, every time.Duration) bool {
	b.tokens = min(capacity, b.tokens+float64(now.Sub(b.last))/float64(every))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// createLimiter rate-limits sandbox creation per client IP and globally.
type createLimiter struct {
	now     func() time.Time
	mu      sync.Mutex
	clients map[string]*bucket
	global  bucket
}

func newCreateLimiter(now func() time.Time) *createLimiter {
	return &createLimiter{
		now:     now,
		clients: map[string]*bucket{},
		global:  bucket{tokens: globalCreatesPerHour, last: now()},
	}
}

// allow reports whether client may create a sandbox now.
func (l *createLimiter) allow(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.clients[client]
	if !ok {
		if len(l.clients) >= maxLimiterClients {
			l.sweep(now)
		}
		b = &bucket{tokens: createBurst, last: now}
		l.clients[client] = b
	}
	// Check the client's own budget first, so one noisy client can't drain
	// the global budget with requests it would be refused anyway.
	probe := *b
	if !probe.take(now, createBurst, createRefill) {
		*b = probe
		return false
	}
	if !l.global.take(now, globalCreatesPerHour, time.Hour/globalCreatesPerHour) {
		return false
	}
	*b = probe
	return true
}

// sweep drops clients whose buckets have refilled completely.
func (l *createLimiter) sweep(now time.Time) {
	full := createRefill * createBurst
	for k, b := range l.clients {
		if now.Sub(b.last) >= full {
			delete(l.clients, k)
		}
	}
}

// clientIP identifies the client for rate limiting. Behind a trusted proxy
// (Render), the last X-Forwarded-For entry is the address the proxy saw;
// earlier entries are supplied by the client and can be forged.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
