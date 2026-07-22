package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func TestSameOrigin(t *testing.T) {
	origin := mustURL(t, "https://my.orq.ai")
	cases := []struct {
		name string
		u    string
		want bool
	}{
		{"identical", "https://my.orq.ai/v2/x", true},
		{"scheme case-insensitive", "HTTPS://my.orq.ai", true},
		{"explicit default https port", "https://my.orq.ai:443/v2/x", true},
		{"downgrade https->http", "http://my.orq.ai", false},
		{"http explicit port 80 vs https default", "http://my.orq.ai:80", false},
		{"different host", "https://evil.example.com", false},
		{"different port", "https://my.orq.ai:8443", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameOrigin(mustURL(t, tc.u), origin); got != tc.want {
				t.Errorf("sameOrigin(%q) = %v, want %v", tc.u, got, tc.want)
			}
		})
	}

	// Default-port normalization is symmetric: an origin that spells :443
	// explicitly still matches a portless request URL on the same host.
	t.Run("origin spells default port", func(t *testing.T) {
		originWithPort := mustURL(t, "https://my.orq.ai:443")
		if !sameOrigin(mustURL(t, "https://my.orq.ai/v2/x"), originWithPort) {
			t.Errorf("portless URL should match origin that names :443")
		}
	})
}

// TestSameOrigin_IPv6NoCollision proves the origin comparison treats (scheme, host,
// port) as a field-by-field tuple rather than reassembling a "host:port" string: an
// IPv6 literal whose brackets url.URL.Hostname() strips must never collide with a
// different destination. https://[2001:db8::1]:8443 (host 2001:db8::1, port 8443) is
// NOT the same origin as https://[2001:db8::1:8443] (host 2001:db8::1:8443, default
// port), even though a naive host+":"+port renders both as "2001:db8::1:8443" —
// attaching the bearer to the latter would leak it cross-origin.
func TestSameOrigin_IPv6NoCollision(t *testing.T) {
	withPort := mustURL(t, "https://[2001:db8::1]:8443")
	embedded := mustURL(t, "https://[2001:db8::1:8443]")
	if sameOrigin(withPort, embedded) || sameOrigin(embedded, withPort) {
		t.Error("IPv6 host:port must not collide with a host that embeds the port digits")
	}
	// The same IPv6 address with and without its explicit default port IS one origin.
	bare := mustURL(t, "https://[2001:db8::1]")
	withDefault := mustURL(t, "https://[2001:db8::1]:443")
	if !sameOrigin(bare, withDefault) || !sameOrigin(withDefault, bare) {
		t.Error("same IPv6 address with/without explicit :443 must be the same origin")
	}
	// A different explicit port on the same IPv6 host stays a distinct origin.
	if sameOrigin(mustURL(t, "https://[2001:db8::1]:8443"), mustURL(t, "https://[2001:db8::1]:9443")) {
		t.Error("different explicit ports on the same IPv6 host must not be same-origin")
	}
}

// TestSameOrigin_HostCaseInsensitive proves DNS host comparison is case-insensitive
// (DNS is case-insensitive), so a redirect to https://MY.ORQ.AI from origin
// https://my.orq.ai is same-origin and not spuriously rejected.
func TestSameOrigin_HostCaseInsensitive(t *testing.T) {
	origin := mustURL(t, "https://my.orq.ai")
	if !sameOrigin(mustURL(t, "https://MY.ORQ.AI/v2/x"), origin) {
		t.Error("host comparison must be case-insensitive")
	}
}

func TestRejectCrossOriginRedirect(t *testing.T) {
	origin := mustURL(t, "https://my.orq.ai")
	check := rejectCrossOriginRedirect(origin)

	// Same-origin redirect is allowed.
	if err := check(&http.Request{URL: mustURL(t, "https://my.orq.ai/other")}, nil); err != nil {
		t.Errorf("same-origin redirect rejected: %v", err)
	}
	// Cross-host and downgrade are rejected.
	for _, target := range []string{"https://evil.example.com/x", "http://my.orq.ai/x"} {
		if err := check(&http.Request{URL: mustURL(t, target)}, nil); err == nil {
			t.Errorf("redirect to %q was allowed, want rejected", target)
		}
	}
}

// TestBearerRoundTripper_OriginScoped verifies the token is attached only to
// requests on the configured origin, never to a foreign origin.
func TestBearerRoundTripper_OriginScoped(t *testing.T) {
	origin := mustURL(t, "https://my.orq.ai")
	var gotAuth string
	rt := &bearerRoundTripper{
		token:  sentinelToken,
		origin: origin,
		next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotAuth = r.Header.Get(authHeader)
			return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
		}),
	}

	// On-origin: token attached.
	_, _ = rt.RoundTrip(&http.Request{URL: mustURL(t, "https://my.orq.ai/v2/policies"), Header: make(http.Header)})
	if want := "Bearer " + sentinelToken; gotAuth != want {
		t.Errorf("on-origin Authorization = %q, want %q", gotAuth, want)
	}

	// Off-origin (e.g. a leaked redirect target): token NOT attached.
	gotAuth = ""
	_, _ = rt.RoundTrip(&http.Request{URL: mustURL(t, "https://evil.example.com/steal"), Header: make(http.Header)})
	if gotAuth != "" {
		t.Errorf("token leaked cross-origin: Authorization = %q", gotAuth)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRedirectLeak_CrossOrigin is the end-to-end guard: the origin server 302s
// to a foreign origin; the client must refuse to follow, so the foreign server
// never receives the request (and thus never the bearer token).
func TestRedirectLeak_CrossOrigin(t *testing.T) {
	var foreignHits, foreignSawToken int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		if r.Header.Get(authHeader) != "" {
			atomic.AddInt32(&foreignSawToken, 1)
		}
		w.WriteHeader(200)
	}))
	defer foreign.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/v2/policies", http.StatusFound)
	}))
	defer origin.Close()

	c, err := New(Config{URL: origin.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Policies().List(context.Background(), ListParams{})
	if err == nil {
		t.Fatal("expected redirect to be rejected, got nil error")
	}
	if n := atomic.LoadInt32(&foreignHits); n != 0 {
		t.Errorf("foreign origin was hit %d time(s); redirect should have been refused", n)
	}
	if n := atomic.LoadInt32(&foreignSawToken); n != 0 {
		t.Errorf("bearer token leaked to foreign origin %d time(s)", n)
	}
}
