// Package client is the transport-agnostic seam between the Terraform resource
// layer and the two generated API clients.
//
// A resource depends only on the narrow per-domain interfaces exposed here
// (e.g. ProjectsAPI). The concrete implementation dispatches each domain to its
// native transport: Connect (connect-go) for domains that have a platform-api
// Connect service, REST (oapi-codegen) for the router entities. The REST client
// is expected to disappear over time, so this interface is the seam that lets a
// domain move between transports without touching resource code.
//
// Errors are normalized at the seam: every adapter maps its native failure
// (a connect.Code or an HTTP status) onto a transport-neutral *Error (see
// errors.go) before returning, so resource diagnostics never leak the wire
// protocol.
package client

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// connectBasePath is appended to the provider URL for all Connect calls. The
// gateway strips it, leaving the bare /orq.platform.v1.<Service>/<Method> path
// the platform-api binary serves.
const connectBasePath = "/v3/rpc/platform"

// requestTimeout bounds a single API exchange (connect + read of the response).
// It is in addition to — not a replacement for — context cancellation, which is
// still honored. Pagination performs one request per page, each under this bound.
const requestTimeout = 60 * time.Second

// Config is the resolved provider configuration used to build a Client.
type Config struct {
	URL   string // e.g. https://my.orq.ai (no trailing /v3/rpc/platform)
	Token string // opaque sk-orq-... management key
}

// Client dispatches per-domain calls to the correct transport. Resources hold
// this and reach domains through the accessor methods (Projects(), ...), never
// through connect-go or restgen types directly.
type Client struct {
	projects        ProjectsAPI
	budgets         BudgetsAPI
	notifiers       NotifiersAPI
	guardrailRules  GuardrailRulesAPI
	workspaceModels WorkspaceModelsAPI
	policies        PoliciesAPI
	routingRules    RoutingRulesAPI
	apiKeys         APIKeysAPI
	managementKeys  ManagementKeysAPI
	models          ModelsAPI

	rest *restgen.ClientWithResponses
}

// ParseBaseURL validates and normalizes a provider base URL. It requires an
// absolute http(s) URL with a non-empty host and rejects embedded userinfo,
// query, and fragment (a base URL is an origin + optional path prefix; those
// components have no meaning here and usually signal a copy-paste mistake). The
// returned URL has any trailing slash trimmed from its path.
func ParseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("not a valid URL: %w", err)
	}
	if !u.IsAbs() {
		return nil, fmt.Errorf("must be an absolute URL including scheme (got %q)", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("URL has no host (got %q)", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL must not embed userinfo (credentials go in the token attribute / ORQ_TOKEN)")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return nil, fmt.Errorf("URL must not include a query string")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return nil, fmt.Errorf("URL must not include a fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// originOf returns a scheme+host-only copy of u (path/query/fragment stripped).
func originOf(u *url.URL) *url.URL {
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}

// newTransport builds an http.Transport with bounded dial/handshake/response
// timeouts. Context cancellation is preserved by the caller's http.Client.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// New builds a Client with both transports sharing the one management token.
// It performs only structural validation; credential validation is lazy (the
// first real API call surfaces auth errors — see the provider Configure docs).
func New(cfg Config) (*Client, error) {
	base, err := ParseBaseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("orq client: %w", err)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("orq client: empty token")
	}
	origin := originOf(base)
	baseStr := strings.TrimRight(base.String(), "/")

	// Connect transport: bounded-timeout client + bearer interceptor. Redirects
	// off the configured origin (or an https→http downgrade) are refused so the
	// bearer token can never be re-sent to another host.
	connectHTTP := &http.Client{
		Timeout:       requestTimeout,
		Transport:     newTransport(),
		CheckRedirect: rejectCrossOriginRedirect(origin),
	}
	connectOpts := connect.WithInterceptors(bearerInterceptor(cfg.Token))
	connectURL := baseStr + connectBasePath

	projectsClient := platformv1connect.NewProjectsServiceClient(connectHTTP, connectURL, connectOpts)
	budgetsClient := platformv1connect.NewBudgetsServiceClient(connectHTTP, connectURL, connectOpts)
	notifiersClient := platformv1connect.NewNotifiersServiceClient(connectHTTP, connectURL, connectOpts)
	modelSharingClient := platformv1connect.NewModelSharingServiceClient(connectHTTP, connectURL, connectOpts)
	apiKeysClient := platformv1connect.NewApiKeysServiceClient(connectHTTP, connectURL, connectOpts)
	managementKeysClient := platformv1connect.NewManagementKeysServiceClient(connectHTTP, connectURL, connectOpts)

	// REST transport: shared token injected via an origin-scoped bearer
	// round-tripper, with the same redirect policy as the Connect client.
	restHTTP := &http.Client{
		Timeout:       requestTimeout,
		Transport:     &bearerRoundTripper{token: cfg.Token, origin: origin, next: newTransport()},
		CheckRedirect: rejectCrossOriginRedirect(origin),
	}
	rest, err := restgen.NewClientWithResponses(baseStr, restgen.WithHTTPClient(restHTTP))
	if err != nil {
		return nil, fmt.Errorf("orq client: build REST client: %w", err)
	}

	return &Client{
		projects:        &connectProjects{c: projectsClient},
		budgets:         &connectBudgets{c: budgetsClient},
		notifiers:       &connectNotifiers{c: notifiersClient},
		guardrailRules:  &restGuardrailRules{c: rest},
		workspaceModels: &workspaceModels{rest: rest, sharing: modelSharingClient},
		policies:        &restPolicies{c: rest},
		routingRules:    &restRoutingRules{c: rest},
		apiKeys:         &connectAPIKeys{c: apiKeysClient},
		managementKeys:  &connectManagementKeys{c: managementKeysClient},
		models:          &restModels{c: rest},
		rest:            rest,
	}, nil
}

// Projects returns the transport-agnostic projects API (Connect-backed).
func (c *Client) Projects() ProjectsAPI { return c.projects }

// Budgets returns the transport-agnostic budgets API (Connect-backed).
func (c *Client) Budgets() BudgetsAPI { return c.budgets }

// Notifiers returns the transport-agnostic notifiers API (Connect-backed).
func (c *Client) Notifiers() NotifiersAPI { return c.notifiers }

// GuardrailRules returns the transport-agnostic guardrail-rules API (REST-backed).
func (c *Client) GuardrailRules() GuardrailRulesAPI { return c.guardrailRules }

// WorkspaceModels returns the transport-agnostic workspace-models API (REST
// enable/disable + Connect sharing write + inline sharing read).
func (c *Client) WorkspaceModels() WorkspaceModelsAPI { return c.workspaceModels }

// Policies returns the transport-agnostic policies API (REST-backed).
func (c *Client) Policies() PoliciesAPI { return c.policies }

// RoutingRules returns the transport-agnostic routing-rules API (REST-backed).
func (c *Client) RoutingRules() RoutingRulesAPI { return c.routingRules }

// APIKeys returns the transport-agnostic api-keys API (Connect-backed).
func (c *Client) APIKeys() APIKeysAPI { return c.apiKeys }

// ManagementKeys returns the transport-agnostic management-keys API (Connect-backed).
func (c *Client) ManagementKeys() ManagementKeysAPI { return c.managementKeys }

// Models returns the transport-agnostic custom-models API (REST-backed).
func (c *Client) Models() ModelsAPI { return c.models }
