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
		{"downgrade https->http", "http://my.orq.ai", false},
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
