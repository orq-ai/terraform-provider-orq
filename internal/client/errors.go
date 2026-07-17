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
// domain is a static, transport-neutral noun for the affected resource domain
// (e.g. "budget") used only on the transport-error path — see below.
func mapConnectError(domain string, err error) error {
	if err == nil {
		return nil
	}

	// An RPC that reached the server carries a *connect.Error whose Message()
	// is server-supplied (not the wire route); prefer it over the connect
	// envelope wrapper ("unauthenticated: <msg>") so we don't double-print the
	// code — and so it never contains the RPC URL.
	var ce *connect.Error
	if errors.As(err, &ce) {
		return &Error{Code: connectCodeToCode(ce.Code()), Message: ce.Message(), err: err}
	}

	// Transport-level failure (dial/DNS/redirect rejection/context cancel)
	// before any RPC response. err.Error() renders the full RPC URL here, which
	// would leak the protocol + route across the seam, so build the message
	// from the static per-domain noun instead of the raw error.
	return &Error{Code: CodeUnavailable, Message: "the " + domain + " service is unavailable", err: err}
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
// the raw response body (may be nil). Only call this for status >= 300.
//
// The rendered Message is transport-neutral: it never embeds "HTTP", the numeric
// status, a route, or the raw response body/HTML, so a diagnostic built from it
// does not leak the wire protocol across the client seam (mirroring the Connect
// path). The status and (capped) body are preserved on a wrapped error that stays
// reachable via errors.Is/As for callers that want the gory detail — but that
// wrapped error is never folded into Message.
func mapRESTStatus(status int, body []byte) error {
	code := httpStatusToCode(status)
	var underlying error
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		// Cap the body so a large HTML error page can't flood a wrapped detail.
		if len(trimmed) > 512 {
			trimmed = trimmed[:512] + "…"
		}
		underlying = fmt.Errorf("status %d: %s", status, trimmed)
	} else {
		underlying = fmt.Errorf("status %d", status)
	}
	return &Error{Code: code, Message: neutralRESTMessage(code), err: underlying}
}

// neutralRESTMessage renders a transport-neutral human phrase for a normalized
// code. It names neither the wire protocol nor the route/body.
func neutralRESTMessage(code Code) string {
	switch code {
	case CodeUnauthenticated:
		return "the request was not authenticated"
	case CodePermissionDenied:
		return "the request was not authorized"
	case CodeNotFound:
		return "the requested resource was not found"
	case CodeConflict:
		return "the request conflicts with the current state of the resource"
	case CodeInvalid:
		return "the request was rejected as invalid"
	case CodeUnavailable:
		return "the service is temporarily unavailable"
	default:
		return "the request failed"
	}
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
// before any HTTP status is available. domain is a static, transport-neutral
// noun for the affected resource domain (e.g. "guardrail rule"). The raw error
// is NOT folded into the message: a Go *url.Error renders as
// `Delete "https://host/v2/workspace-models/…": …`, leaking the REST route and
// protocol across the seam; the wrapped err stays reachable via errors.Is/As.
func mapRESTTransportError(domain string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: CodeUnavailable, Message: "the " + domain + " service is unavailable", err: err}
}
