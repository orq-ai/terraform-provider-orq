package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// sentinelToken is the single management key threaded through BOTH transports.
// The Connect and REST tests assert this exact value arrives as `Bearer <token>`.
const sentinelToken = "sk-orq-test-0123456789abcdef"

// wantConnectPath is the exact on-wire path the Connect ProjectsService list call
// must hit: the provider base + /v3/rpc/platform prefix + the bare Connect route.
const wantConnectPath = "/v3/rpc/platform/orq.platform.v1.ProjectsService/ListProjects"

// fakeProjectsHandler is an in-process Connect ProjectsService implementation.
type fakeProjectsHandler struct {
	platformv1connect.UnimplementedProjectsServiceHandler
	resp *platformv1.ListProjectsResponse
}

func (f *fakeProjectsHandler) ListProjects(_ context.Context, _ *platformv1.ListProjectsRequest) (*platformv1.ListProjectsResponse, error) {
	return f.resp, nil
}

// newConnectServer mounts a real Connect ProjectsService handler under the
// /v3/rpc/platform prefix and records the path + Authorization header of the
// last request.
func newConnectServer(t *testing.T, resp *platformv1.ListProjectsResponse) (srv *httptest.Server, gotPath, gotAuth *string) {
	t.Helper()
	path, h := platformv1connect.NewProjectsServiceHandler(&fakeProjectsHandler{resp: resp})
	mux := http.NewServeMux()
	mux.Handle("/v3/rpc/platform"+path, http.StripPrefix("/v3/rpc/platform", h))

	var p, a string
	rec := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p = r.URL.Path
		a = r.Header.Get("Authorization")
		mux.ServeHTTP(w, r)
	})
	srv = httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return srv, &p, &a
}

func TestConnectTransport_PathAndBearer(t *testing.T) {
	resp := &platformv1.ListProjectsResponse{
		Object:  "list",
		HasMore: false,
		Data: []*platformv1.Project{
			{ProjectId: "p_1", Name: "alpha", Key: "alpha", Description: "d", IsArchived: false, IsDefault: true},
		},
	}
	srv, gotPath, gotAuth := newConnectServer(t, resp)

	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page, err := c.Projects().List(context.Background(), ListParams{Limit: 200})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if *gotPath != wantConnectPath {
		t.Errorf("connect path = %q, want %q", *gotPath, wantConnectPath)
	}
	if want := "Bearer " + sentinelToken; *gotAuth != want {
		t.Errorf("connect Authorization = %q, want %q", *gotAuth, want)
	}
	if len(page.Projects) != 1 || page.Projects[0].ID != "p_1" || !page.Projects[0].IsDefault {
		t.Errorf("unexpected projects: %+v", page.Projects)
	}
}

// newRESTServer returns an httptest server that records the path + Authorization
// header and serves whatever the handler func writes.
func newRESTServer(t *testing.T, handler http.HandlerFunc) (srv *httptest.Server, gotPath, gotAuth *string) {
	t.Helper()
	var p, a string
	rec := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p = r.URL.Path
		a = r.Header.Get("Authorization")
		handler(w, r)
	})
	srv = httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return srv, &p, &a
}

func TestRESTTransport_PathAndBearer(t *testing.T) {
	srv, gotPath, gotAuth := newRESTServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"_id": "pol_1", "display_name": "P1", "enabled": true, "project_id": "", "created_at": "2020-01-01T00:00:00Z", "updated_at": "2020-01-01T00:00:00Z", "created_by_id": "u", "updated_by_id": "u", "slug": "p1", "timeout": 0},
			},
			"has_more": false,
		})
	})

	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	page, err := c.Policies().List(context.Background(), ListParams{Limit: 200})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if *gotPath != "/v2/policies" {
		t.Errorf("REST path = %q, want /v2/policies", *gotPath)
	}
	if want := "Bearer " + sentinelToken; *gotAuth != want {
		t.Errorf("REST Authorization = %q, want %q", *gotAuth, want)
	}
	if len(page.Policies) != 1 || page.Policies[0].ID != "pol_1" || !page.Policies[0].Enabled {
		t.Errorf("unexpected policies: %+v", page.Policies)
	}
}

func TestRESTTransport_ErrorNormalization(t *testing.T) {
	cases := []struct {
		status int
		want   Code
	}{
		{http.StatusUnauthorized, CodeUnauthenticated},
		{http.StatusForbidden, CodePermissionDenied},
		{http.StatusNotFound, CodeNotFound},
		{http.StatusConflict, CodeConflict},
		{http.StatusBadRequest, CodeInvalid},
		{http.StatusUnprocessableEntity, CodeInvalid},
		{http.StatusTooManyRequests, CodeUnavailable},
		{http.StatusServiceUnavailable, CodeUnavailable},
		{http.StatusInternalServerError, CodeInternal},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv, _, _ := newRESTServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
			})
			c, err := New(Config{URL: srv.URL, Token: sentinelToken})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.Policies().List(context.Background(), ListParams{})
			if err == nil {
				t.Fatalf("expected error for status %d", tc.status)
			}
			if got := CodeOf(err); got != tc.want {
				t.Errorf("status %d normalized to %q, want %q", tc.status, got, tc.want)
			}
			// The normalized message must not leak a transport route name.
			if strings.Contains(err.Error(), "/v2/policies") {
				t.Errorf("error leaks REST route: %q", err.Error())
			}
		})
	}
}

func TestConnectTransport_ErrorNormalization(t *testing.T) {
	// A NON-Connect HTTP response (a plain status, not a Connect JSON envelope —
	// what a proxy/gateway returns) is wrapped by connect-go as a client-
	// synthesized *connect.Error whose Message() is the raw HTTP status line
	// ("401 Unauthorized", "502 Bad Gateway"). The seam must normalize the CODE
	// but replace that status-line message with a transport-neutral phrase, so the
	// diagnostic never leaks the HTTP status, the status text, or the RPC service.
	cases := []struct {
		name   string
		status int
		want   Code
		// tokens that must NOT appear in the rendered message (status line + route).
		noLeak []string
	}{
		{"plain 401", http.StatusUnauthorized, CodeUnauthenticated, []string{"401", "Unauthorized", "HTTP status", "ProjectsService"}},
		{"proxy 502", http.StatusBadGateway, CodeUnavailable, []string{"502", "Bad Gateway", "HTTP status", "ProjectsService"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := newRESTServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			})
			c, err := New(Config{URL: srv.URL, Token: sentinelToken})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.Projects().List(context.Background(), ListParams{})
			if err == nil {
				t.Fatal("expected error")
			}
			if got := CodeOf(err); got != tc.want {
				t.Errorf("connect %d normalized to %q, want %q", tc.status, got, tc.want)
			}
			for _, leak := range tc.noLeak {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error leaks %q: %q", leak, err.Error())
				}
			}
		})
	}
}

// TestNew_NoNetwork proves lazy validation: building the client (all Configure
// does after structural checks) performs ZERO network requests. Auth is only
// exercised on the first real API call.
func TestNew_NoNetwork(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	if _, err := New(Config{URL: srv.URL, Token: sentinelToken}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("New made %d network request(s); want 0 (lazy validation)", n)
	}
}

func TestParseBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"https ok", "https://my.orq.ai", false},
		{"http ok", "http://localhost:8080", false},
		{"path prefix ok", "https://my.orq.ai/prefix/", false},
		{"trailing slash trimmed", "https://my.orq.ai/", false},
		{"empty", "", true},
		{"scheme only", "https://", true},
		{"no scheme", "my.orq.ai", true},
		{"bad scheme", "ftp://my.orq.ai", true},
		{"ws scheme", "ws://my.orq.ai", true},
		{"userinfo rejected", "https://user:pass@my.orq.ai", true},
		{"query rejected", "https://my.orq.ai?x=1", true},
		{"fragment rejected", "https://my.orq.ai#frag", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBaseURL(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("ParseBaseURL(%q) = nil error, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ParseBaseURL(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}
