// Package api is Puente's HTTP layer: the JSON API under /api (sandbox
// cookie, idempotency keys, error envelope) and the single-page app with an
// index.html fallback for every other GET. Handlers are thin: they decode,
// call the engine or a domain package, and encode.
package api

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/OmarIbannez/puente/internal/engine"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxBodyBytes bounds every request body.
const MaxBodyBytes = 64 << 10

// Deps are the server's collaborators.
type Deps struct {
	Pool   *pgxpool.Pool
	Engine *engine.Engine
	// Static is the built SPA (index.html at its root). Nil serves a small
	// built-in placeholder page.
	Static fs.FS
	// Now is the real clock; nil means time.Now.
	Now func() time.Time
	// Logger receives request logs; nil means slog.Default().
	Logger *slog.Logger
	// Seed fills every new visitor sandbox with the demo world. Nil leaves
	// new sandboxes empty.
	Seed SeedFunc
	// Version is reported by /api/health.
	Version string
}

// SeedFunc fills a freshly created sandbox.
type SeedFunc func(ctx context.Context, pool *pgxpool.Pool, sandbox uuid.UUID) error

// server holds the dependencies shared by the handlers.
type server struct {
	pool    *pgxpool.Pool
	engine  *engine.Engine
	now     func() time.Time
	log     *slog.Logger
	seed    SeedFunc
	version string
	touches *touchThrottle
	mux     *http.ServeMux
}

// New returns the HTTP handler for the API and the SPA.
func New(d Deps) http.Handler {
	s := &server{
		pool:    d.Pool,
		engine:  d.Engine,
		now:     d.Now,
		log:     d.Logger,
		seed:    d.Seed,
		version: d.Version,
		mux:     http.NewServeMux(),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.touches = newTouchThrottle(touchInterval, s.now)
	static := d.Static
	if static == nil {
		static = placeholderFS()
	}
	s.routes(newStaticHandler(static))
	return s.recoverer(s.logRequests(limitBody(s.mux)))
}

// reqInfo is filled in while a request is served, for the request log.
type reqInfo struct {
	sandbox uuid.UUID
}

type reqInfoKey struct{}

func requestInfo(ctx context.Context) *reqInfo {
	ri, _ := ctx.Value(reqInfoKey{}).(*reqInfo)
	return ri
}

// statusWriter records the response status.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests logs one structured line per request: method, path, status,
// duration and sandbox.
func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ri := &reqInfo{}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, ri)))
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
		}
		if ri.sandbox != uuid.Nil {
			attrs = append(attrs, "sandbox", ri.sandbox.String())
		}
		level := slog.LevelInfo
		if r.URL.Path == "/api/health" || !strings.HasPrefix(r.URL.Path, "/api/") {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "http request", attrs...)
	})
}

// recoverer turns a handler panic into a 500 JSON error.
func (s *server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer s.recoverPanic(w, r)
		next.ServeHTTP(w, r)
	})
}

// recoverPanic must be deferred directly so recover sees the panic.
func (s *server) recoverPanic(w http.ResponseWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v) // net/http's signal to abort the response silently
	}
	s.log.ErrorContext(r.Context(), "panic serving request", "method", r.Method, "path", r.URL.Path,
		"panic", v, "stack", string(debug.Stack()))
	writeJSON(w, http.StatusInternalServerError,
		errorBody{Error: errorDetail{Code: CodeInternal, Message: "internal server error"}})
}

// limitBody caps every request body at MaxBodyBytes.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
