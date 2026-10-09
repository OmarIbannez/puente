package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/deposit"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
)

// Error codes returned in {"error": {"code", "message"}}.
const (
	CodeBadRequest             = "BAD_REQUEST"
	CodeNotFound               = "NOT_FOUND"
	CodeMethodNotAllowed       = "METHOD_NOT_ALLOWED"
	CodeConflict               = "CONFLICT"
	CodeValidation             = "VALIDATION"
	CodePayloadTooLarge        = "PAYLOAD_TOO_LARGE"
	CodeIdempotencyKeyRequired = "IDEMPOTENCY_KEY_REQUIRED"
	CodeIdempotencyKeyReused   = "IDEMPOTENCY_KEY_REUSED"
	CodeIdempotencyInProgress  = "IDEMPOTENCY_IN_PROGRESS"
	CodeInsufficientFunds      = "INSUFFICIENT_FUNDS"
	CodeReadOnly               = "READ_ONLY"
	CodeInternal               = "INTERNAL"
	CodeUnavailable            = "UNAVAILABLE"
)

// httpError is a request error with an HTTP status and a stable code. Its
// message is shown to the client.
type httpError struct {
	Status  int
	Code    string
	Message string
}

// Error returns the message.
func (e *httpError) Error() string { return e.Message }

func errorf(status int, code, format string, args ...any) *httpError {
	return &httpError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) *httpError {
	return errorf(http.StatusBadRequest, CodeBadRequest, format, args...)
}

func validation(format string, args ...any) *httpError {
	return errorf(http.StatusUnprocessableEntity, CodeValidation, format, args...)
}

func notFound(format string, args ...any) *httpError {
	return errorf(http.StatusNotFound, CodeNotFound, format, args...)
}

// errorBody is the JSON error envelope.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// classify maps err to its HTTP status, code and client message. Unknown
// errors are 500 INTERNAL with a generic message.
func classify(err error) (status int, code, msg string) {
	var (
		apiErr *httpError
		maxErr *http.MaxBytesError
	)
	switch {
	case errors.As(err, &apiErr):
		return apiErr.Status, apiErr.Code, apiErr.Message
	case errors.As(err, &maxErr):
		return http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, recon.ErrNoRuns):
		return http.StatusNotFound, CodeNotFound, err.Error()
	case errors.Is(err, payout.ErrInsufficientFunds):
		return http.StatusUnprocessableEntity, CodeInsufficientFunds, err.Error()
	case errors.Is(err, engine.ErrLiveReadOnly):
		return http.StatusForbidden, CodeReadOnly, err.Error()
	case errors.Is(err, payout.ErrNotCancellable), errors.Is(err, payout.ErrIllegalTransition):
		return http.StatusConflict, CodeConflict, err.Error()
	case isValidation(err):
		return http.StatusUnprocessableEntity, CodeValidation, err.Error()
	default:
		return http.StatusInternalServerError, CodeInternal, "internal server error"
	}
}

// validationErrors are domain errors caused by the request's content.
var validationErrors = []error{
	payout.ErrInvalidCLABE, payout.ErrUnsupportedAsset, payout.ErrInvalidAmount, payout.ErrInvalidRequest,
	money.ErrUnknownAsset, money.ErrBadAmount, money.ErrPrecision, money.ErrOverflow,
	customer.ErrBadAddress, deposit.ErrUnknownNetwork, engine.ErrNoSimNetwork, spei.ErrBadInjection,
}

func isValidation(err error) bool {
	for _, target := range validationErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// writeError writes err as the JSON error envelope, logging 5xx errors.
func (s *server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg := classify(err)
	if status >= http.StatusInternalServerError {
		s.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

// writeJSON encodes v with status. Encoding errors (a broken connection)
// are dropped: the status line is already sent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"code":"INTERNAL","message":"internal server error"}}`+"\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// decodeJSON reads the request body into dst, rejecting unknown fields and
// trailing data. An empty body decodes as {} so handlers report missing
// fields as validation errors.
func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return err
		}
		return badRequest("reading request body: %v", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	if dec.More() {
		return badRequest("invalid JSON body: unexpected data after the object")
	}
	return nil
}
