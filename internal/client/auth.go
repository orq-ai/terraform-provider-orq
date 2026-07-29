package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	connect "connectrpc.com/connect"
)

const authHeader = "Authorization"

func bearerValue(token string) string { return "Bearer " + token }

// bearerInterceptor injects the management-key bearer token on every outbound
// Connect unary request. The Connect client is built against one fixed origin,
// so cross-origin leakage is prevented by rejectCrossOriginRedirect rather than
// here.
func bearerInterceptor(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IsClient {
				req.Header().Set(authHeader, bearerValue(token))
			}
			return next(ctx, req)
		}
	}
}

// bearerRoundTripper is the REST counterpart of bearerInterceptor. Its origin
// check is defense in depth: even if a redirect slipped past CheckRedirect, the
// token only ever rides a request on the configured origin.
type bearerRoundTripper struct {
	token  string
	origin *url.URL // scheme+host the credential is scoped to
	next   http.RoundTripper
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so we never mutate the caller's request.
	r := req.Clone(req.Context())
	if sameOrigin(req.URL, rt.origin) {
		r.Header.Set(authHeader, bearerValue(rt.token))
	}
	return rt.next.RoundTrip(r)
}

// sameOrigin compares the (scheme, host, effective port) triple FIELD BY FIELD
// and never reassembles a "host:port" string. Reassembly is unsafe for an IPv6
// literal because url.URL.Hostname() strips the brackets: https://[2001:db8::1]:8443
// and https://[2001:db8::1:8443] would then collide, letting the bearer be
// attached to a different destination.
func sameOrigin(u, origin *url.URL) bool {
	if u == nil || origin == nil {
		return false
	}
	return strings.EqualFold(u.Scheme, origin.Scheme) &&
		strings.EqualFold(u.Hostname(), origin.Hostname()) &&
		effectivePort(u) == effectivePort(origin)
}

// effectivePort returns u's explicit port, or its scheme's default, so
// https://host and https://host:443 are the same origin.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

// rejectCrossOriginRedirect blocks the classic bearer-token leak: a redirect to
// a third-party host, or an https→http downgrade, would otherwise re-send the
// Authorization header. Same-origin redirects still work, up to a hop cap.
func rejectCrossOriginRedirect(origin *url.URL) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("orq client: stopped after 10 redirects")
		}
		if !sameOrigin(req.URL, origin) {
			return fmt.Errorf(
				"orq client: refusing redirect to %s://%s — a token-bearing request may only follow "+
					"same-origin redirects (configured origin is %s://%s)",
				req.URL.Scheme, req.URL.Host, origin.Scheme, origin.Host)
		}
		return nil
	}
}
