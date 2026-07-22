package client

import (
	"encoding/json"
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

	var ce *connect.Error
	if errors.As(err, &ce) {
		code := connectCodeToCode(ce.Code())
		// A genuine server error carries an operator-actionable, transport-neutral
		// message: connect-go parses it from the Connect JSON error envelope and
		// flags it as a WIRE error (IsWireError == true). Surface that message
		// (not the connect envelope wrapper "unauthenticated: <msg>", and never
		// the RPC URL) — the REST path surfaces the server message symmetrically.
		//
		// A client-SYNTHESIZED *connect.Error is NOT a wire error: connect-go
		// produces one when a NON-Connect HTTP response comes back (a proxy /
		// gateway error, an HTML error page), and its Message() embeds the raw
		// HTTP status line ("HTTP status 505 ...", "502 Bad Gateway") — a transport
		// leak. For that case emit the neutral phrase for the mapped code instead.
		msg := neutralMessage(code)
		if connect.IsWireError(err) {
			msg = ce.Message()
		}
		return &Error{Code: code, Message: msg, err: err}
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
// The rendered Message surfaces the platform's operator-actionable error text
// (parsed from its JSON `{"error": "..."}` envelope) when present, falling back
// to a transport-neutral phrase per code otherwise. It never embeds "HTTP", the
// numeric status, a route, or a raw response body/HTML, so a diagnostic built
// from it does not leak the wire protocol across the client seam (mirroring the
// Connect path, which surfaces the server's wire message). The status and
// (capped) body are preserved on a wrapped error that stays reachable via
// errors.Is/As for callers that want the gory detail — but that wrapped error is
// never folded into Message.
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
	msg := neutralMessage(code)
	if server := serverRESTMessage(body); server != "" {
		msg = server
	}
	return &Error{Code: code, Message: msg, err: underlying}
}

// serverRESTMessage extracts the operator-actionable message from the platform's
// JSON error envelope — {"error": "..."}, the shape the platform-api ErrorHandler
// always emits (see apps/platform-api/server/app.go). It returns "" when the body
// is not that envelope (e.g. an HTML proxy/gateway error page or an empty body),
// so mapRESTStatus falls back to the transport-neutral phrase and never surfaces
// a raw body. The `error` field is a domain message (the server masks its own 5xx
// internals to "internal server error" and never includes the HTTP status or
// route), so surfacing it does not leak transport internals — it mirrors the
// Connect path's server wire message.
func serverRESTMessage(body []byte) string {
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	msg := strings.TrimSpace(env.Error)
	if len(msg) > 512 {
		msg = msg[:512] + "…"
	}
	return msg
}

// neutralMessage renders a transport-neutral human phrase for a normalized code.
// It names neither the wire protocol nor the route/body, and is shared by both
// the REST fallback (no server envelope) and the Connect fallback (a client-
// synthesized, non-wire error).
func neutralMessage(code Code) string {
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
