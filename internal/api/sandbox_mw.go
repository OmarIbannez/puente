package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
)

// Sandbox cookie settings.
const (
	// SandboxCookie maps a browser to its sandbox.
	SandboxCookie = "puente_sandbox"
	// SandboxCookieMaxAge is the cookie lifetime; it slides on activity.
	SandboxCookieMaxAge = 7 * 24 * time.Hour
	// touchInterval is the most often a sandbox's last_seen_at is updated.
	touchInterval = time.Minute
	// seedTimeout bounds creating and seeding a sandbox. Seeding runs
	// detached from the request so a client that hangs up mid-seed does not
	// leave a half-seeded sandbox behind.
	seedTimeout = 60 * time.Second
)

type sandboxKey struct{}

// sandboxID returns the request's sandbox, set by withSandbox.
func sandboxID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(sandboxKey{}).(uuid.UUID)
	return id
}

func withSandboxID(ctx context.Context, id uuid.UUID) context.Context {
	if ri := requestInfo(ctx); ri != nil {
		ri.sandbox = id
	}
	return context.WithValue(ctx, sandboxKey{}, id)
}

// touchThrottle remembers when each sandbox was last touched, so a busy
// visitor costs one UPDATE per touchInterval rather than one per request.
// An entry also means "this sandbox existed at that time".
type touchThrottle struct {
	interval time.Duration
	now      func() time.Time
	mu       sync.Mutex
	last     map[uuid.UUID]time.Time
}

// maxThrottleEntries triggers a sweep of stale entries.
const maxThrottleEntries = 10_000

func newTouchThrottle(interval time.Duration, now func() time.Time) *touchThrottle {
	return &touchThrottle{interval: interval, now: now, last: map[uuid.UUID]time.Time{}}
}

// fresh reports whether id was touched within the interval.
func (t *touchThrottle) fresh(id uuid.UUID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.last[id]
	return ok && t.now().Sub(at) < t.interval
}

// mark records a touch of id now.
func (t *touchThrottle) mark(id uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if len(t.last) >= maxThrottleEntries {
		for k, at := range t.last {
			if now.Sub(at) >= t.interval {
				delete(t.last, k)
			}
		}
	}
	t.last[id] = now
}

// forget drops id (after the sandbox was deleted).
func (t *touchThrottle) forget(id uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.last, id)
}

// withSandbox resolves the visitor's sandbox from the puente_sandbox
// cookie, creating and seeding a new one when the cookie is missing,
// malformed or names a sandbox that no longer exists, and puts its id in
// the request context.
//
// Two concurrent first requests from one browser each create their own
// sandbox (the browser keeps whichever cookie arrives last); each request
// is served only after its sandbox is fully seeded.
func (s *server) withSandbox(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, readOnly, err := s.resolveSandbox(w, r)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if readOnly && r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.writeError(w, r, errorf(http.StatusForbidden, CodeReadOnly,
				"the demo is busy, so you are browsing a shared read-only sandbox; reload in a few minutes for your own"))
			return
		}
		next(w, r.WithContext(withSandboxID(r.Context(), id)))
	}
}

// resolveSandbox returns the visitor's sandbox from the cookie, or creates
// and seeds a new one. When creation is rate-limited, it falls back to the
// shared showcase sandbox, read-only and without setting a cookie, so the
// visitor gets their own sandbox once the budget refills.
func (s *server) resolveSandbox(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool, error) {
	if c, err := r.Cookie(SandboxCookie); err == nil {
		if id, err := uuid.Parse(c.Value); err == nil {
			if s.touches.fresh(id) {
				return id, false, nil
			}
			sb, err := store.TouchSandbox(r.Context(), s.pool, id)
			switch {
			case err == nil && sb.Kind != store.KindVisitor:
				// A cookie naming a shared sandbox is forged; never let a
				// visitor act on it. Fall through to a fresh sandbox.
			case err == nil:
				s.touches.mark(id)
				s.setSandboxCookie(w, id) // slide the expiry
				return id, false, nil
			case !errors.Is(err, store.ErrNotFound):
				return uuid.Nil, false, fmt.Errorf("api: touch sandbox: %w", err)
			}
		}
	}
	id, err := s.newSandbox(r)
	var apiErr *httpError
	if errors.As(err, &apiErr) && apiErr.Code == CodeRateLimited {
		sb, err := s.showcase(r.Context())
		if err != nil {
			return uuid.Nil, false, err
		}
		return sb.ID, true, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	s.setSandboxCookie(w, id)
	return id, false, nil
}

// showcase returns the shared read-only sandbox, creating and seeding it on
// first use.
func (s *server) showcase(ctx context.Context) (store.Sandbox, error) {
	s.showcaseMu.Lock()
	defer s.showcaseMu.Unlock()
	sb, err := store.ShowcaseSandbox(ctx, s.pool)
	if !errors.Is(err, store.ErrNotFound) {
		return sb, err
	}
	id, err := s.createSandbox(ctx)
	if err != nil {
		return store.Sandbox{}, err
	}
	return store.PromoteShowcase(ctx, s.pool, id)
}

// Creation is rate-limited per client IP (see createLimiter): a client
// over its budget gets 429 instead of another seeded world.
func (s *server) newSandbox(r *http.Request) (uuid.UUID, error) {
	if err := s.allowCreate(r); err != nil {
		return uuid.Nil, err
	}
	id, err := s.createSandbox(r.Context())
	if err != nil {
		return uuid.Nil, err
	}
	// Only visitor sandboxes may enter the touch cache: a cached id skips
	// the kind check in resolveSandbox.
	s.touches.mark(id)
	return id, nil
}

// allowCreate spends one sandbox creation from the client's budget, or
// returns the 429 error.
func (s *server) allowCreate(r *http.Request) error {
	if !s.creates.allow(clientIP(r, s.proxied)) {
		return errorf(http.StatusTooManyRequests, CodeRateLimited,
			"too many new sandboxes from this address; keep your cookie or retry in a minute")
	}
	return nil
}

// createSandbox creates a sandbox and, with a seed function, seeds it. A
// sandbox whose seeding fails is deleted.
func (s *server) createSandbox(parent context.Context) (uuid.UUID, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), seedTimeout)
	defer cancel()
	sb, err := store.CreateSandbox(ctx, s.pool)
	if err != nil {
		return uuid.Nil, fmt.Errorf("api: create sandbox: %w", err)
	}
	if s.seed != nil {
		if err := s.seed(ctx, s.pool, sb.ID); err != nil {
			if delErr := store.DeleteSandbox(ctx, s.pool, sb.ID); delErr != nil {
				err = errors.Join(err, delErr)
			}
			return uuid.Nil, fmt.Errorf("api: seed sandbox: %w", err)
		}
	}
	s.log.InfoContext(ctx, "sandbox created", "sandbox", sb.ID.String(), "seeded", s.seed != nil)
	return sb.ID, nil
}

// setSandboxCookie sets the sandbox cookie: HttpOnly, SameSite=Lax and
// always Secure. Browsers and curl accept Secure cookies from
// http://localhost, so local development works; plain HTTP on any other
// host does not keep the cookie (each request would get a new sandbox), so
// deploy behind HTTPS.
func (s *server) setSandboxCookie(w http.ResponseWriter, id uuid.UUID) {
	http.SetCookie(w, &http.Cookie{
		Name:     SandboxCookie,
		Value:    id.String(),
		Path:     "/",
		MaxAge:   int(SandboxCookieMaxAge / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
