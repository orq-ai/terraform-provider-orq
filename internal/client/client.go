// Package client is the transport-agnostic seam between the Terraform resource
// layer and the two generated API clients.
//
// A resource depends only on the narrow per-domain interfaces exposed here
// (e.g. ProjectsAPI). The concrete implementation dispatches each domain to its
// native transport: Connect (connect-go) for domains that have a platform-api
// Connect service, REST (oapi-codegen) for the router entities. The REST client
// is expected to disappear over time, so this interface is the seam that lets a
// domain move between transports without touching resource code.
package client

import (
	"fmt"
	"net/http"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// connectBasePath is appended to the provider URL for all Connect calls. The
// gateway strips it, leaving the bare /orq.platform.v1.<Service>/<Method> path
// the platform-api binary serves.
const connectBasePath = "/v3/rpc/platform"

// Config is the resolved provider configuration used to build a Client.
type Config struct {
	URL   string // e.g. https://api.orq.ai (no trailing /v3/rpc/platform)
	Token string // opaque sk-orq-... management key
}

// Client dispatches per-domain calls to the correct transport. Resources hold
// this and reach domains through the accessor methods (Projects(), ...), never
// through connect-go or restgen types directly.
type Client struct {
	projects ProjectsAPI

	// Wired for the phase-1 resource set that follows; unused by the current
	// smoke test. Kept here so the transport seam is established up front.
	//   Connect-backed: budgets, notifiers, managementKeys, modelSharing, apiKeys, identities
	//   REST-backed:    guardrailRules, routingRules, policies, models, workspaceModels
	rest *restgen.ClientWithResponses
}

// New builds a Client with both transports sharing the one management token.
// It performs only structural validation; credential validation is lazy (the
// first real API call surfaces auth errors — see the provider Configure docs).
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		return nil, fmt.Errorf("orq client: empty URL")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("orq client: empty token")
	}

	// Connect transport: plain HTTP client + bearer interceptor.
	connectHTTP := &http.Client{}
	connectOpts := connect.WithInterceptors(bearerInterceptor(cfg.Token))
	connectURL := base + connectBasePath

	projectsClient := platformv1connect.NewProjectsServiceClient(connectHTTP, connectURL, connectOpts)

	// REST transport: shared token injected via a bearer round-tripper.
	restHTTP := &http.Client{Transport: &bearerRoundTripper{token: cfg.Token, next: http.DefaultTransport}}
	rest, err := restgen.NewClientWithResponses(base, restgen.WithHTTPClient(restHTTP))
	if err != nil {
		return nil, fmt.Errorf("orq client: build REST client: %w", err)
	}

	return &Client{
		projects: &connectProjects{c: projectsClient},
		rest:     rest,
	}, nil
}

// Projects returns the transport-agnostic projects API (Connect-backed).
func (c *Client) Projects() ProjectsAPI { return c.projects }
