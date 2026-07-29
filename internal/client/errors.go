package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	connect "connectrpc.com/connect"
)

// Code is a transport-neutral error classification. Both transports map their
// native failure signal onto one of these before the error crosses the client
// seam, so the resource layer never has to know which wire protocol a domain
// rides on.
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

// Error is the normalized error returned by every adapter method. Its Error()
// string is transport-neutral — it never names a Connect service/method or an
// HTTP route — while the wrapped transport error stays reachable via errors.As.
type Error struct {
	Code    Code
	Message string
	err     error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.err }

// CodeOf returns the normalized Code of err if it is (or wraps) an *Error, and
// CodeInternal otherwise.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// mapConnectError normalizes a connect-go client error. domain is a static,
// transport-neutral noun (e.g. "budget") used on the transport-error path.
func mapConnectError(domain string, err error) error {
	if err == nil {
		return nil
	}

	var ce *connect.Error
	if errors.As(err, &ce) {
		code := connectCodeToCode(ce.Code())
		return &Error{Code: code, Message: connectMessage(code, err, ce), err: err}
	}

	// Transport-level failure before any RPC response. err.Error() renders the
	// full RPC URL here, so the message is built from the domain noun instead.
	return &Error{Code: CodeUnavailable, Message: "the " + domain + " service is unavailable", err: err}
}

// connectMessage picks what to surface for a Connect failure.
//
// connect-go flags any JSON-shaped error response as a WIRE error, but that is
// protocol SHAPE, not authenticated provenance — a reverse proxy's body counts
// too — so a wire message is only surfaced after sanitizeMessage. A
// client-SYNTHESIZED *connect.Error (a non-Connect HTTP response) is NOT a wire
// error and its Message() embeds the raw HTTP status line, so it is dropped for
// the neutral phrase.
func connectMessage(code Code, err error, ce *connect.Error) string {
	if connect.IsWireError(err) {
		if msg := sanitizeMessage(ce.Message()); msg != "" {
			return msg
		}
	}
	return neutralMessage(code)
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

// mapRESTStatus normalizes a non-2xx HTTP status. Message never embeds "HTTP",
// the numeric status, a route or a raw body; those survive only on the wrapped
// error, which is never folded into Message.
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
// {"error": "..."} envelope, or "" for anything else (an HTML proxy error page,
// an empty body) so the caller falls back to the neutral phrase.
func serverRESTMessage(body []byte) string {
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return ""
	}
	return sanitizeMessage(env.Error)
}

// sanitizeMessage bounds and cleans an untrusted server message before it
// crosses the seam into a diagnostic: whitespace collapses to one space, every
// other C0/C1 control is dropped (so an ANSI escape cannot reach the terminal),
// and the result is truncated by RUNE count so a multibyte value is never split
// mid-rune. Inert markup such as <b>…</b> is left as-is; it renders literally.
func sanitizeMessage(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '\n' || r == '\r' || r == '\t':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
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

// neutralMessage renders a transport-neutral phrase for a normalized code.
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

// mapRESTTransportError normalizes a REST failure that occurs before any HTTP
// status is available. The raw error is NOT folded into the message: a Go
// *url.Error renders the full route.
func mapRESTTransportError(domain string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: CodeUnavailable, Message: "the " + domain + " service is unavailable", err: err}
}
