package client

import (
	"context"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

const (
	McpGatewayModeCode   = "CODE"
	McpGatewayModeDirect = "DIRECT"

	McpToolNamingPrefixWithServerKey = "PREFIX_WITH_SERVER_KEY"
	McpToolNamingPrefixOnCollision   = "PREFIX_ON_COLLISION"

	mcpGatewayModePrefix = "MCP_GATEWAY_MODE_"
	mcpToolNamingPrefix  = "MCP_TOOL_NAMING_"
)

type McpSkillExposure struct {
	Mode      string
	SkillURIs []string
}

type McpGatewayServerLink struct {
	McpServerID   string
	Alias         string
	Enabled       bool
	ToolExposure  McpToolExposure
	SkillExposure *McpSkillExposure
}

type McpGateway struct {
	ID          string
	Key         string
	DisplayName string
	Description string
	Mode        string
	ToolNaming  string
	WebsiteURL  string
	PublicURL   string
	ServerLinks []McpGatewayServerLink
	Guardrails  []GuardrailRef
	Sharing     *McpSharing
	CreatedAt   string
	UpdatedAt   string
}

type McpGatewayCreateInput struct {
	Key         string
	DisplayName string
	Description *string
	Mode        string
	ToolNaming  string
	WebsiteURL  string
	ServerLinks []McpGatewayServerLink
	Guardrails  []GuardrailRef
	Sharing     *McpSharing
}

// McpGatewayUpdateInput replaces every list it carries; an empty one clears it.
// ExpectedUpdatedAt is the last observed UpdatedAt, which the server requires
// before it re-enables a link that exposes resources.
type McpGatewayUpdateInput struct {
	ID                string
	DisplayName       string
	Description       string
	Mode              string
	ToolNaming        string
	WebsiteURL        string
	ServerLinks       []McpGatewayServerLink
	Guardrails        []GuardrailRef
	Sharing           *McpSharing
	ExpectedUpdatedAt string
}

type McpGatewaysAPI interface {
	Get(ctx context.Context, id string) (*McpGateway, error)
	Create(ctx context.Context, in McpGatewayCreateInput) (*McpGateway, error)
	Update(ctx context.Context, in McpGatewayUpdateInput) (*McpGateway, error)
	Delete(ctx context.Context, id string) error
}

type connectMcpGateways struct {
	c platformv1connect.McpGatewayServiceClient
}

func mcpGatewayModeToProto(short string) (platformv1.McpGatewayMode, error) {
	return mcpEnumToProto[platformv1.McpGatewayMode](platformv1.McpGatewayMode_value, mcpGatewayModePrefix, "gateway mode", short)
}

func mcpToolNamingToProto(short string) (platformv1.McpToolNaming, error) {
	return mcpEnumToProto[platformv1.McpToolNaming](platformv1.McpToolNaming_value, mcpToolNamingPrefix, "tool naming", short)
}

func mcpServerLinksToProto(links []McpGatewayServerLink) ([]*platformv1.McpGatewayServerLink, error) {
	out := make([]*platformv1.McpGatewayServerLink, 0, len(links))
	for _, link := range links {
		toolExposure, err := mcpToolExposureToProto(link.ToolExposure)
		if err != nil {
			return nil, err
		}
		skillExposure, err := mcpSkillExposureToProto(link.SkillExposure)
		if err != nil {
			return nil, err
		}
		out = append(out, &platformv1.McpGatewayServerLink{
			McpServerId:   link.McpServerID,
			Alias:         link.Alias,
			Enabled:       link.Enabled,
			ToolExposure:  toolExposure,
			SkillExposure: skillExposure,
		})
	}
	return out, nil
}

func mcpSkillExposureToProto(e *McpSkillExposure) (*platformv1.McpSkillExposure, error) {
	if e == nil {
		return nil, nil
	}
	mode, err := mcpExposureModeToProto(e.Mode)
	if err != nil {
		return nil, err
	}
	return &platformv1.McpSkillExposure{Mode: mode, SkillUris: e.SkillURIs}, nil
}

func mcpServerLinksFromProto(links []*platformv1.McpGatewayServerLink) []McpGatewayServerLink {
	var out []McpGatewayServerLink
	for _, link := range links {
		l := McpGatewayServerLink{
			McpServerID:  link.GetMcpServerId(),
			Alias:        link.GetAlias(),
			Enabled:      link.GetEnabled(),
			ToolExposure: mcpToolExposureFromProto(link.GetToolExposure()),
		}
		if skills := link.GetSkillExposure(); skills != nil {
			l.SkillExposure = &McpSkillExposure{
				Mode:      mcpEnumFromProto(skills.GetMode().String(), mcpExposureModePrefix),
				SkillURIs: skills.GetSkillUris(),
			}
		}
		out = append(out, l)
	}
	return out
}

func mcpGatewayFromProto(g *platformv1.McpGateway) *McpGateway {
	return &McpGateway{
		ID:          g.GetId(),
		Key:         g.GetKey(),
		DisplayName: g.GetDisplayName(),
		Description: g.GetDescription(),
		Mode:        mcpEnumFromProto(g.GetMode().String(), mcpGatewayModePrefix),
		ToolNaming:  mcpEnumFromProto(g.GetToolNaming().String(), mcpToolNamingPrefix),
		WebsiteURL:  g.GetWebsiteUrl(),
		PublicURL:   g.GetPublicUrl(),
		ServerLinks: mcpServerLinksFromProto(g.GetServerLinks()),
		Guardrails:  guardrailRefsFromProto(g.GetGuardrails()),
		Sharing:     mcpSharingFromProto(g.GetSharing()),
		CreatedAt:   g.GetCreated(),
		UpdatedAt:   g.GetUpdated(),
	}
}

func (c *connectMcpGateways) Get(ctx context.Context, id string) (*McpGateway, error) {
	resp, err := c.c.GetMcpGateway(ctx, &platformv1.GetMcpGatewayRequest{Id: id})
	if err != nil {
		return nil, mapConnectError("MCP gateway", err)
	}
	return mcpGatewayFromProto(resp.GetMcpGateway()), nil
}

func (c *connectMcpGateways) Create(ctx context.Context, in McpGatewayCreateInput) (*McpGateway, error) {
	mode, err := mcpGatewayModeToProto(in.Mode)
	if err != nil {
		return nil, err
	}
	toolNaming, err := mcpToolNamingToProto(in.ToolNaming)
	if err != nil {
		return nil, err
	}
	links, err := mcpServerLinksToProto(in.ServerLinks)
	if err != nil {
		return nil, err
	}
	guardrails, err := guardrailRefsToProto(in.Guardrails)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.CreateMcpGateway(ctx, &platformv1.CreateMcpGatewayRequest{
		Key:         in.Key,
		DisplayName: in.DisplayName,
		Description: in.Description,
		ServerLinks: links,
		ToolNaming:  toolNaming,
		Mode:        mode,
		Sharing:     mcpSharingToProto(in.Sharing),
		WebsiteUrl:  in.WebsiteURL,
		Guardrails:  guardrails,
	})
	if err != nil {
		return nil, mapConnectError("MCP gateway", err)
	}
	return mcpGatewayFromProto(resp.GetMcpGateway()), nil
}

func (c *connectMcpGateways) Update(ctx context.Context, in McpGatewayUpdateInput) (*McpGateway, error) {
	mode, err := mcpGatewayModeToProto(in.Mode)
	if err != nil {
		return nil, err
	}
	toolNaming, err := mcpToolNamingToProto(in.ToolNaming)
	if err != nil {
		return nil, err
	}
	links, err := mcpServerLinksToProto(in.ServerLinks)
	if err != nil {
		return nil, err
	}
	guardrails, err := guardrailRefsToProto(in.Guardrails)
	if err != nil {
		return nil, err
	}
	replaceGuardrails := true
	req := &platformv1.UpdateMcpGatewayRequest{
		Id:                in.ID,
		DisplayName:       &in.DisplayName,
		Description:       &in.Description,
		ServerLinks:       links,
		ClearServerLinks:  len(links) == 0,
		ToolNaming:        &toolNaming,
		Mode:              &mode,
		Sharing:           mcpSharingToProto(in.Sharing),
		WebsiteUrl:        &in.WebsiteURL,
		Guardrails:        guardrails,
		ReplaceGuardrails: &replaceGuardrails,
	}
	if in.ExpectedUpdatedAt != "" {
		req.ExpectedUpdated = &in.ExpectedUpdatedAt
	}
	resp, err := c.c.UpdateMcpGateway(ctx, req)
	if err != nil {
		return nil, mapConnectError("MCP gateway", err)
	}
	return mcpGatewayFromProto(resp.GetMcpGateway()), nil
}

func (c *connectMcpGateways) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteMcpGateway(ctx, &platformv1.DeleteMcpGatewayRequest{Id: id})
	return mapConnectError("MCP gateway", err)
}
