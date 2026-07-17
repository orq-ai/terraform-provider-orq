package client

import (
	"context"
	"net/http"

	connect "connectrpc.com/connect"
)

const authHeader = "Authorization"

func bearerValue(token string) string { return "Bearer " + token }

// bearerInterceptor injects the management-key bearer token on every outbound
// Connect (connect-go) unary request. It shares the single provider token with
// the REST transport (see bearerRoundTripper).
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

// bearerRoundTripper injects the same bearer token on every REST request. This
// is the REST-transport counterpart of bearerInterceptor; both read the one
// opaque sk-orq-... management key.
type bearerRoundTripper struct {
	token string
	next  http.RoundTripper
}

func (rt *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so we never mutate the caller's request.
	r := req.Clone(req.Context())
	r.Header.Set(authHeader, bearerValue(rt.token))
	return rt.next.RoundTrip(r)
}
