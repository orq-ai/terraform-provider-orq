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
// Connect (connect-go) unary request. It shares the single provider token with
// the REST transport (see bearerRoundTripper).
//
// The Connect client is built against one fixed origin (base + connectBasePath),
// so cross-origin leakage is prevented at the redirect layer by the http.Client's
// CheckRedirect (rejectCrossOriginRedirect) rather than here.
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

// bearerRoundTripper injects the same bearer token on every REST request whose
// URL is on the configured origin. This is the REST-transport counterpart of
// bearerInterceptor; both read the one opaque sk-orq-... management key.
//
// The origin check is defense-in-depth against token leakage: even if a redirect
// slipped past CheckRedirect, the token is only ever attached to a request whose
// scheme+host exactly matches the configured base origin.
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

// sameOrigin reports whether u has the same scheme and host as origin. Scheme
// comparison is case-insensitive; host comparison ignores a default port spelled
// out explicitly (https://host:443 == https://host, http://host:80 == http://host)
// so an origin that names the standard port for its scheme still matches, while a
// non-default port stays a distinct origin. An https→http downgrade fails this
// check (schemes differ, and each scheme's default port normalizes independently).
func sameOrigin(u, origin *url.URL) bool {
	if u == nil || origin == nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, origin.Scheme) {
		return false
	}
	return normalizedHostPort(u) == normalizedHostPort(origin)
}

// normalizedHostPort returns u.Host with an explicit default port for its scheme
// stripped (443 for https, 80 for http), so the same origin written with or
// without its standard port compares equal. Host case is preserved (DNS hosts are
// conventionally lowercase already, and the framework does not case-fold them).
func normalizedHostPort(u *url.URL) string {
	host, port := u.Hostname(), u.Port()
	if port == "" {
		return host
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return host
	}
	return host + ":" + port
}

// rejectCrossOriginRedirect is an http.Client CheckRedirect that refuses to
// follow any redirect whose target is not the exact configured origin
// (scheme+host). This blocks the classic bearer-token leak: a redirect to a
// third-party host, or an https→http downgrade on the same host, would otherwise
// re-send the Authorization header. Same-origin redirects are allowed (up to a
// small hop cap) so a trailing-slash / path canonicalization redirect still works.
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
