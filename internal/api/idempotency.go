package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Idempotency headers.
const (
	// IdempotencyKeyHeader carries the client's key on money-moving POSTs.
	IdempotencyKeyHeader = "Idempotency-Key"
	// IdempotentReplayHeader is "true" on a response replayed from a key.
	IdempotentReplayHeader = "Idempotent-Replay"
	// maxIdempotencyKeyLen bounds the key.
	maxIdempotencyKeyLen = 255
	// pendingStatus marks an idempotency row whose request is still
	// executing (status_code and response are NOT NULL in the schema). A
	// finished row stores the response body verbatim as a JSON string
	// (jsonb would reorder keys), so a replay is byte-identical.
	pendingStatus = 0
	// finishTimeout bounds recording the outcome, which runs detached from
	// a client that may have hung up.
	finishTimeout = 5 * time.Second
)

// idempotent requires an Idempotency-Key and makes the handler execute at
// most once per (sandbox, key):
//
//   - a pending row is inserted first (ON CONFLICT DO NOTHING), so a
//     concurrent duplicate sees it and gets 409 IDEMPOTENCY_IN_PROGRESS;
//   - a 2xx or 4xx outcome is stored and replayed for later requests with
//     the same method, path and body (header Idempotent-Replay: true);
//   - the same key with a different request gets 422 IDEMPOTENCY_KEY_REUSED;
//   - a 5xx (or panic) deletes the pending row so the client can retry.
//
// A process crash between the handler's commit and the outcome being
// stored leaves the row pending: that key keeps answering 409 rather than
// risking a second execution.
func (s *server) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(IdempotencyKeyHeader)
		switch {
		case key == "":
			s.writeError(w, r, errorf(http.StatusBadRequest, CodeIdempotencyKeyRequired,
				"the %s header is required", IdempotencyKeyHeader))
			return
		case len(key) > maxIdempotencyKeyLen:
			s.writeError(w, r, badRequest("the %s header must be at most %d bytes", IdempotencyKeyHeader, maxIdempotencyKeyLen))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		hash := requestHash(r.Method, r.URL.Path, body)
		sb := sandboxID(r.Context())

		claimed, err := s.claimKey(r.Context(), sb, key, hash)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !claimed {
			s.replay(w, r, sb, key, hash)
			return
		}
		rec := newRecorder()
		finished := false
		defer s.releaseIfPanicked(r.Context(), sb, key, &finished)
		next(rec, r)
		finished = true
		if err := s.finishKey(r.Context(), sb, key, rec); err != nil {
			s.writeError(w, r, err)
			return
		}
		rec.copyTo(w)
	}
}

// requestHash fingerprints a request: SHA-256 of method, path and body.
func requestHash(method, path string, body []byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, method+"\n"+path+"\n")
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// claimKey inserts the pending row and reports whether this request owns
// the key.
func (s *server) claimKey(ctx context.Context, sb uuid.UUID, key, hash string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO idempotency_keys (sandbox_id, key, request_hash, status_code, response)
		VALUES ($1, $2, $3, $4, 'null'::jsonb) ON CONFLICT (sandbox_id, key) DO NOTHING`,
		sb, key, hash, pendingStatus)
	if err != nil {
		return false, fmt.Errorf("api: claim idempotency key: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// replay answers a request whose key is already taken.
func (s *server) replay(w http.ResponseWriter, r *http.Request, sb uuid.UUID, key, hash string) {
	var (
		storedHash string
		status     int
		response   []byte
	)
	err := s.pool.QueryRow(r.Context(), `SELECT request_hash, status_code, response #>> '{}'
		FROM idempotency_keys WHERE sandbox_id = $1 AND key = $2`, sb, key).Scan(&storedHash, &status, &response)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The first request just failed with a 5xx and released the key.
		s.writeError(w, r, errorf(http.StatusConflict, CodeIdempotencyInProgress,
			"a request with this %s was still being processed; retry", IdempotencyKeyHeader))
		return
	case err != nil:
		s.writeError(w, r, fmt.Errorf("api: load idempotency key: %w", err))
		return
	}
	if storedHash != hash {
		s.writeError(w, r, errorf(http.StatusUnprocessableEntity, CodeIdempotencyKeyReused,
			"this %s was already used for a different request", IdempotencyKeyHeader))
		return
	}
	if status == pendingStatus {
		s.writeError(w, r, errorf(http.StatusConflict, CodeIdempotencyInProgress,
			"a request with this %s is still being processed", IdempotencyKeyHeader))
		return
	}
	w.Header().Set(IdempotentReplayHeader, "true")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(response)
}

// finishKey stores a 2xx/4xx outcome or releases the key on anything else.
func (s *server) finishKey(ctx context.Context, sb uuid.UUID, key string, rec *recorder) error {
	if !storable(rec.status) {
		s.releaseKey(ctx, sb, key)
		return nil
	}
	if !json.Valid(rec.body.Bytes()) {
		s.releaseKey(ctx, sb, key)
		return errors.New("api: idempotent handler wrote a non-JSON body")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `UPDATE idempotency_keys SET status_code = $3, response = to_jsonb($4::text)
		WHERE sandbox_id = $1 AND key = $2`, sb, key, rec.status, rec.body.String()); err != nil {
		return fmt.Errorf("api: store idempotent response: %w", err)
	}
	return nil
}

// releaseIfPanicked frees the key when the handler did not return (it
// panicked); the panic then continues to the recoverer.
func (s *server) releaseIfPanicked(ctx context.Context, sb uuid.UUID, key string, finished *bool) {
	if !*finished {
		s.releaseKey(ctx, sb, key)
	}
}

// releaseKey deletes a pending row so the key can be retried.
func (s *server) releaseKey(ctx context.Context, sb uuid.UUID, key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys
		WHERE sandbox_id = $1 AND key = $2 AND status_code = $3`, sb, key, pendingStatus); err != nil {
		s.log.ErrorContext(ctx, "release idempotency key", "sandbox", sb.String(), "err", err)
	}
}

// storable reports whether a response status is kept for replay.
func storable(status int) bool {
	return (status >= 200 && status < 300) || (status >= 400 && status < 500)
}

// recorder buffers a handler's response.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func newRecorder() *recorder {
	return &recorder{header: http.Header{}, status: http.StatusOK}
}

func (rec *recorder) Header() http.Header { return rec.header }

func (rec *recorder) WriteHeader(code int) {
	if !rec.wrote {
		rec.status, rec.wrote = code, true
	}
}

func (rec *recorder) Write(b []byte) (int, error) {
	rec.wrote = true
	return rec.body.Write(b)
}

// copyTo sends the buffered response.
func (rec *recorder) copyTo(w http.ResponseWriter) {
	maps.Copy(w.Header(), rec.header)
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes())
}
