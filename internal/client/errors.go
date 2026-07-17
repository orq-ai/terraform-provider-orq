package client

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	connect "connectrpc.com/connect"
)

// Code is a transport-neutral error classification. Both transports (Connect
// and REST) map their native failure signal — a connect.Code or an HTTP status
// — onto one of these before the error crosses the client seam, so the resource
// layer never has to know (or leak) which wire protocol a domain rides on.
type Code string

const (
	CodeUnauthenticated  Code = "unauthenticated"
	CodePermissionDenied Code = "permission_denied"
	CodeNotFound         Code = "not_found"
	CodeConflict         Code = "conflict"
	CodeInvalid          Code = "invalid"
	CodeUnavailable      Code = "unavailable"
	CodeInternal         Code = "internal"
)

// Error is the normalized error returned by every adapter method. It carries a
// stable Code plus a human message, and unwraps to the underlying transport
// error for callers that want the gory detail. Its Error() string is
// transport-neutral: it never names a Connect service/method or an HTTP route,
// so a resource diagnostic built from it does not leak the wire protocol.
type Error struct {
	Code    Code
	Message string
	err     error // wrapped transport error, for errors.Is/As and %w
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.err }

// CodeOf returns the normalized Code of err if it is (or wraps) an *Error, and
// CodeInternal otherwise. Handy for tests and for resource-layer branching
// (e.g. treating not_found as "resource gone, drop from state").
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// mapConnectError normalizes a connect-go client error. A nil error maps to nil.
func mapConnectError(err error) error {
	if err == nil {
		return nil
	}
	code := connectCodeToCode(connect.CodeOf(err))

	// Prefer the server-supplied message over the connect envelope wrapper
	// ("unauthenticated: <msg>") so we don't double-print the code.
	message := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		message = ce.Message()
	}
	return &Error{Code: code, Message: message, err: err}
}

func connectCodeToCode(c connect.Code) Code {
	switch c {
	case connect.CodeUnauthenticated:
		return CodeUnauthenticated
	case connect.CodePermissionDenied:
		return CodePermissionDenied
	case connect.CodeNotFound:
		return CodeNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted:
		return CodeConflict
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeOutOfRange:
		return CodeInvalid
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeResourceExhausted:
		return CodeUnavailable
	default:
		return CodeInternal
	}
}

// mapRESTStatus normalizes a non-2xx HTTP status from a REST adapter. body is
// the raw response body (may be nil) and is folded into the message on a
// best-effort basis. Only call this for status >= 300.
func mapRESTStatus(status int, body []byte) error {
	code := httpStatusToCode(status)
	msg := fmt.Sprintf("HTTP %d %s", status, http.StatusText(status))
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		// Cap the body so a large HTML error page can't flood a diagnostic.
		if len(trimmed) > 512 {
			trimmed = trimmed[:512] + "…"
		}
		msg = msg + ": " + trimmed
	}
	return &Error{Code: code, Message: msg}
}

func httpStatusToCode(status int) Code {
	switch status {
	case http.StatusUnauthorized: // 401
		return CodeUnauthenticated
	case http.StatusForbidden: // 403
		return CodePermissionDenied
	case http.StatusNotFound, http.StatusGone: // 404, 410
		return CodeNotFound
	case http.StatusConflict: // 409
		return CodeConflict
	case http.StatusBadRequest, http.StatusUnprocessableEntity: // 400, 422
		return CodeInvalid
	case http.StatusTooManyRequests, // 429
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return CodeUnavailable
	default:
		if status >= 500 {
			return CodeInternal
		}
		if status >= 400 {
			return CodeInvalid
		}
		return CodeInternal
	}
}

// mapRESTTransportError normalizes a transport-level REST error (connection
// refused, DNS failure, redirect rejection, context cancellation) that occurs
// before any HTTP status is available.
func mapRESTTransportError(err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: CodeUnavailable, Message: err.Error(), err: err}
}
