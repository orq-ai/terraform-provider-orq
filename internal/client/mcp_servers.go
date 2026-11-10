package client

import (
	"context"
	"slices"
	"strings"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

const (
	McpConnectionTypeHTTP    = "HTTP"
	McpConnectionTypeSSE     = "SSE"
	McpConnectionTypeOpenAPI = "OPENAPI"

	McpAuthTypeNone                   = "NONE"
	McpAuthTypeStaticHeaders          = "STATIC_HEADERS"
	McpAuthTypeOAuthClientCredentials = "OAUTH_CLIENT_CREDENTIALS"
	McpAuthTypePerUserOAuth           = "PER_USER_OAUTH"
	McpAuthTypeEnterpriseManaged      = "ENTERPRISE_MANAGED"

	McpExposureModeAll      = "ALL"
	McpExposureModeSelected = "SELECTED"
	McpExposureModeNone     = "NONE"

	mcpConnectionTypePrefix = "MCP_CONNECTION_TYPE_"
	mcpAuthTypePrefix       = "MCP_AUTH_TYPE_"
	mcpExposureModePrefix   = "MCP_TOOL_EXPOSURE_MODE_"
)

type McpConnection struct {
	Type string
	URL  string
}

// McpAuth secrets are write-only: a read returns neither StaticHeaders nor OAuth.ClientSecret.
type McpAuth struct {
	Type          string
	StaticHeaders map[string]string
	OAuth         *McpOAuth
}

type McpOAuth struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	Scopes       []string
	Issuer       string
	Resource     string
}

type McpToolExposure struct {
	Mode            string
	ReadOnly        bool
	ToolIDs         []string
	ExcludedToolIDs []string
}

// McpOpenAPI.Document is write-only: the server compiles it and never returns it.
type McpOpenAPI struct {
	Document    string
	DocumentURL string
	Operations  []string
}

type McpSharing struct {
	AllProjects bool
	ProjectIDs  []string
}

type McpServer struct {
	ID                  string
	Key                 string
	DisplayName         string
	Description         string
	Connection          McpConnection
	Auth                McpAuth
	DefaultToolExposure McpToolExposure
	OpenAPI             *McpOpenAPI
	Sharing             *McpSharing
	CreatedAt           string
	UpdatedAt           string
}

type McpServerCreateInput struct {
	Key                 string
	DisplayName         string
	Description         *string
	Connection          McpConnection
	Auth                McpAuth
	DefaultToolExposure McpToolExposure
	OpenAPI             *McpOpenAPI
	Sharing             *McpSharing
}

// McpServerUpdateInput leaves Connection, Auth and OpenAPI nil when unchanged:
// the server dials the upstream whenever connection or auth is sent, and
// recompiles the tools whenever openapi is sent.
type McpServerUpdateInput struct {
	ID                  string
	DisplayName         string
	Description         string
	Connection          *McpConnection
	Auth                *McpAuth
	DefaultToolExposure McpToolExposure
	OpenAPI             *McpOpenAPI
	Sharing             *McpSharing
}

type McpServersAPI interface {
	Get(ctx context.Context, id string) (*McpServer, error)
	Create(ctx context.Context, in McpServerCreateInput) (*McpServer, error)
	Update(ctx context.Context, in McpServerUpdateInput) (*McpServer, error)
	Delete(ctx context.Context, id string) error
}

type connectMcpServers struct {
	c platformv1connect.McpGatewayServiceClient
}

func mcpEnumToProto[E ~int32](values map[string]int32, prefix, field, short string) (E, error) {
	v, ok := values[prefix+short]
	if !ok || v == 0 {
		return 0, &Error{Code: CodeInvalid, Message: "unknown " + field + " " + short}
	}
	return E(v), nil
}

func mcpEnumFromProto(name, prefix string) string {
	short := strings.TrimPrefix(name, prefix)
	if short == "UNSPECIFIED" {
		return ""
	}
	return short
}

func mcpConnectionToProto(c McpConnection) (*platformv1.McpConnection, error) {
	t, err := mcpEnumToProto[platformv1.McpConnectionType](platformv1.McpConnectionType_value, mcpConnectionTypePrefix, "connection type", c.Type)
	if err != nil {
		return nil, err
	}
	return &platformv1.McpConnection{Type: t, Url: c.URL}, nil
}

func mcpAuthToProto(a McpAuth) (*platformv1.McpAuthConfig, error) {
	t, err := mcpEnumToProto[platformv1.McpAuthType](platformv1.McpAuthType_value, mcpAuthTypePrefix, "auth type", a.Type)
	if err != nil {
		return nil, err
	}
	out := &platformv1.McpAuthConfig{Type: t}
	for _, key := range sortedKeys(a.StaticHeaders) {
		out.StaticHeaders = append(out.StaticHeaders, &platformv1.McpHeaderSecret{Key: key, Value: a.StaticHeaders[key]})
	}
	if a.OAuth != nil {
		out.Oauth = &platformv1.McpOAuthConfig{
			ClientId:     a.OAuth.ClientID,
			ClientSecret: a.OAuth.ClientSecret,
			TokenUrl:     a.OAuth.TokenURL,
			Scopes:       a.OAuth.Scopes,
			Issuer:       a.OAuth.Issuer,
			Resource:     a.OAuth.Resource,
		}
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func mcpAuthFromProto(a *platformv1.McpAuthConfig) McpAuth {
	out := McpAuth{Type: mcpEnumFromProto(a.GetType().String(), mcpAuthTypePrefix)}
	if oauth := a.GetOauth(); oauth != nil {
		out.OAuth = &McpOAuth{
			ClientID: oauth.GetClientId(),
			TokenURL: oauth.GetTokenUrl(),
			Scopes:   oauth.GetScopes(),
			Issuer:   oauth.GetIssuer(),
			Resource: oauth.GetResource(),
		}
	}
	return out
}

func mcpToolExposureToProto(e McpToolExposure) (*platformv1.McpToolExposure, error) {
	mode, err := mcpExposureModeToProto(e.Mode)
	if err != nil {
		return nil, err
	}
	return &platformv1.McpToolExposure{
		Mode:            mode,
		ReadOnly:        e.ReadOnly,
		ToolIds:         e.ToolIDs,
		ExcludedToolIds: e.ExcludedToolIDs,
	}, nil
}

func mcpExposureModeToProto(short string) (platformv1.McpToolExposureMode, error) {
	return mcpEnumToProto[platformv1.McpToolExposureMode](platformv1.McpToolExposureMode_value, mcpExposureModePrefix, "exposure mode", short)
}

func mcpToolExposureFromProto(e *platformv1.McpToolExposure) McpToolExposure {
	return McpToolExposure{
		Mode:            mcpEnumFromProto(e.GetMode().String(), mcpExposureModePrefix),
		ReadOnly:        e.GetReadOnly(),
		ToolIDs:         e.GetToolIds(),
		ExcludedToolIDs: e.GetExcludedToolIds(),
	}
}

func mcpOpenAPIToProto(o *McpOpenAPI) *platformv1.McpOpenApiConfig {
	if o == nil {
		return nil
	}
	return &platformv1.McpOpenApiConfig{
		Document:    o.Document,
		DocumentUrl: o.DocumentURL,
		Operations:  o.Operations,
	}
}

func mcpOpenAPIFromProto(o *platformv1.McpOpenApiConfig) *McpOpenAPI {
	if o == nil {
		return nil
	}
	return &McpOpenAPI{DocumentURL: o.GetDocumentUrl(), Operations: o.GetOperations()}
}

func mcpSharingToProto(s *McpSharing) *platformv1.Sharing {
	if s == nil {
		return nil
	}
	if s.AllProjects {
		return &platformv1.Sharing{Mode: &platformv1.Sharing_AllProjects{AllProjects: &platformv1.SharingAllProjects{}}}
	}
	return &platformv1.Sharing{Mode: &platformv1.Sharing_Selected{
		Selected: &platformv1.SharingSelectedProjects{ProjectIds: s.ProjectIDs},
	}}
}

func mcpSharingFromProto(s *platformv1.Sharing) *McpSharing {
	if s == nil {
		return nil
	}
	if s.GetSelected() == nil {
		return &McpSharing{AllProjects: true}
	}
	return &McpSharing{ProjectIDs: s.GetSelected().GetProjectIds()}
}

func mcpServerFromProto(s *platformv1.McpServer) *McpServer {
	return &McpServer{
		ID:          s.GetId(),
		Key:         s.GetKey(),
		DisplayName: s.GetDisplayName(),
		Description: s.GetDescription(),
		Connection: McpConnection{
			Type: mcpEnumFromProto(s.GetConnection().GetType().String(), mcpConnectionTypePrefix),
			URL:  s.GetConnection().GetUrl(),
		},
		Auth:                mcpAuthFromProto(s.GetAuth()),
		DefaultToolExposure: mcpToolExposureFromProto(s.GetDefaultToolExposure()),
		OpenAPI:             mcpOpenAPIFromProto(s.GetOpenapi()),
		Sharing:             mcpSharingFromProto(s.GetSharing()),
		CreatedAt:           s.GetCreated(),
		UpdatedAt:           s.GetUpdated(),
	}
}

func (c *connectMcpServers) Get(ctx context.Context, id string) (*McpServer, error) {
	resp, err := c.c.GetMcpServer(ctx, &platformv1.GetMcpServerRequest{Id: id})
	if err != nil {
		return nil, mapConnectError("MCP server", err)
	}
	return mcpServerFromProto(resp.GetMcpServer()), nil
}

func (c *connectMcpServers) Create(ctx context.Context, in McpServerCreateInput) (*McpServer, error) {
	connection, err := mcpConnectionToProto(in.Connection)
	if err != nil {
		return nil, err
	}
	auth, err := mcpAuthToProto(in.Auth)
	if err != nil {
		return nil, err
	}
	exposure, err := mcpToolExposureToProto(in.DefaultToolExposure)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.CreateMcpServer(ctx, &platformv1.CreateMcpServerRequest{
		Key:                 in.Key,
		DisplayName:         in.DisplayName,
		Description:         in.Description,
		Connection:          connection,
		Auth:                auth,
		DefaultToolExposure: exposure,
		Sharing:             mcpSharingToProto(in.Sharing),
		Openapi:             mcpOpenAPIToProto(in.OpenAPI),
	})
	if err != nil {
		return nil, mapConnectError("MCP server", err)
	}
	return mcpServerFromProto(resp.GetMcpServer()), nil
}

func (c *connectMcpServers) Update(ctx context.Context, in McpServerUpdateInput) (*McpServer, error) {
	exposure, err := mcpToolExposureToProto(in.DefaultToolExposure)
	if err != nil {
		return nil, err
	}
	req := &platformv1.UpdateMcpServerRequest{
		Id:                  in.ID,
		DisplayName:         &in.DisplayName,
		Description:         &in.Description,
		DefaultToolExposure: exposure,
		Sharing:             mcpSharingToProto(in.Sharing),
		Openapi:             mcpOpenAPIToProto(in.OpenAPI),
	}
	if in.Connection != nil {
		if req.Connection, err = mcpConnectionToProto(*in.Connection); err != nil {
			return nil, err
		}
	}
	if in.Auth != nil {
		if req.Auth, err = mcpAuthToProto(*in.Auth); err != nil {
			return nil, err
		}
	}
	resp, err := c.c.UpdateMcpServer(ctx, req)
	if err != nil {
		return nil, mapConnectError("MCP server", err)
	}
	return mcpServerFromProto(resp.GetMcpServer()), nil
}

func (c *connectMcpServers) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteMcpServer(ctx, &platformv1.DeleteMcpServerRequest{Id: id})
	return mapConnectError("MCP server", err)
}
