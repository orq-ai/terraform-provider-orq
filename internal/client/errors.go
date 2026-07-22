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
		// A server error's message rides the Connect JSON error envelope, which
		// connect-go flags as a WIRE error (IsWireError == true). Surface it (not the
		// connect envelope wrapper "unauthenticated: <msg>", and never the RPC URL) —
		// the REST path surfaces the server message symmetrically.
		//
		// NOTE: IsWireError is protocol-SHAPE information, NOT authenticated
		// provenance — connect-go marks ANY JSON-shaped error response as a wire error,
		// so a reverse proxy returning {"message":"upstream https://internal-host
		// failed"} is surfaced as a "wire" message just the same. There is no reliable
		// client-side discriminator, so run the message through sanitizeMessage (the
		// same cap + control-strip as the REST envelope) — a hostile or misconfigured
		// proxy's message is then at least bounded and control-free; if it sanitizes to
		// empty, fall back to the neutral phrase.
		//
		// A client-SYNTHESIZED *connect.Error is NOT a wire error: connect-go produces
		// one when a NON-Connect HTTP response comes back (a proxy / gateway error, an
		// HTML error page), and its Message() embeds the raw HTTP status line
		// ("HTTP status 505 ...", "502 Bad Gateway") — a transport leak. For that case
		// emit the neutral phrase for the mapped code instead.
		msg := neutralMessage(code)
		if connect.IsWireError(err) {
			if s := sanitizeMessage(ce.Message()); s != "" {
				msg = s
			}
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
	return sanitizeMessage(env.Error)
}

// sanitizeMessage bounds and cleans an operator-facing server message before it
// crosses the client seam into a Terraform diagnostic. It is applied to BOTH the
// REST JSON envelope message and the Connect wire message — neither is trustworthy
// content: the Connect path accepts a wire message on protocol SHAPE, not
// authenticated provenance (see mapConnectError), and a reverse proxy can inject
// either — so every surfaced server message is first passed through here.
//
// It maps the common whitespace controls (\n, \r, \t) to a single space, DROPS every
// other C0 control (incl. ESC 0x1b, so an ANSI escape sequence cannot reach the
// terminal) and C1 control (0x7f–0x9f), collapses any run of whitespace to one
// space, trims, and truncates by RUNE count to 512 (appending an ellipsis when
// truncated) so a multibyte value is never split mid-rune. Inert markup such as
// <b>…</b> is deliberately left as-is: it renders literally in a terminal, and
// stripping HTML is out of scope here.
func sanitizeMessage(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '\n' || r == '\r' || r == '\t':
			// Whitespace (incl. the mapped control whitespace) collapses to one space.
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// Any other C0 control (incl. ESC) or C1 control: drop entirely.
			continue
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	msg := strings.TrimSpace(b.String())
	if runes := []rune(msg); len(runes) > 512 {
		msg = string(runes[:512]) + "…"
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
