package client

import (
	"context"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// RoutingRule is the transport-agnostic projection of a routing rule (router
// entity, Connect-backed). ProjectID is empty for a workspace-global rule.
//
// ExpressionCEL is the writable part of the rule's match expression (the server
// enriches reads with a derived `config`, which the provider does not surface).
// A nil ModelsConfig means the rule has none.
type RoutingRule struct {
	ID            string
	DisplayName   string
	Description   string
	Enabled       bool
	ProjectID     string
	Priority      int64
	ExpressionCEL string
	ModelsConfig  *RoutingRuleModelsConfig
	CreatedAt     string
	UpdatedAt     string
}

// RoutingRuleModelsConfig is a rule's model-selection block. Mode is one of
// fallback / latency_based / weighted / round_robin and Models must be
// non-empty; the server rejects anything else.
type RoutingRuleModelsConfig struct {
	Mode   string
	Models []RoutingRuleModelRef
}

// RoutingRuleModelRef is one candidate model. DisplayName and IntegrationID
// read back as "" when the server stored none. A nil Weight on a write lets the
// server apply its 0.5 default; on a read it means the stored weight was zero
// (only reachable for a legacy document).
type RoutingRuleModelRef struct {
	Model         string
	DisplayName   string
	Weight        *float64
	IntegrationID string
}

// RoutingRulePage is one page of a cursor-paginated list.
type RoutingRulePage struct {
	Rules   []RoutingRule
	HasMore bool
}

// RoutingRuleCreateInput carries the fields for a create.
type RoutingRuleCreateInput struct {
	DisplayName   string
	Description   *string
	Enabled       *bool
	ProjectID     *string
	Priority      *int64
	ExpressionCEL *string
	ModelsConfig  *RoutingRuleModelsConfig
}

// RoutingRuleUpdateInput is the update patch. There is no ProjectID: the update
// API omits project_id, so the resource marks it RequiresReplace.
//
// A nil ModelsConfig leaves the stored one untouched; ClearModelsConfig removes
// it.
type RoutingRuleUpdateInput struct {
	ID                string
	DisplayName       *string
	Description       *string
	Enabled           *bool
	Priority          *int64
	ExpressionCEL     *string
	ModelsConfig      *RoutingRuleModelsConfig
	ClearModelsConfig bool
}

// RoutingRulesAPI is the per-resource seam for the routing-rules domain
// (Connect-backed).
type RoutingRulesAPI interface {
	List(ctx context.Context, params ListParams) (*RoutingRulePage, error)
	Get(ctx context.Context, id string) (*RoutingRule, error)
	Create(ctx context.Context, in RoutingRuleCreateInput) (*RoutingRule, error)
	Update(ctx context.Context, in RoutingRuleUpdateInput) (*RoutingRule, error)
	Delete(ctx context.Context, id string) error
}

type connectRoutingRules struct {
	c platformv1connect.RoutingRulesServiceClient
}

func routingRuleFromProto(g *platformv1.RoutingRule) RoutingRule {
	r := RoutingRule{
		ID:            g.GetId(),
		DisplayName:   g.GetDisplayName(),
		Description:   g.GetDescription(),
		Enabled:       g.GetEnabled(),
		ProjectID:     g.GetProjectId(),
		Priority:      int64(g.GetPriority()),
		ExpressionCEL: g.GetExpression().GetCel(),
		CreatedAt:     formatTimestamp(g.GetCreatedAt()),
		UpdatedAt:     formatTimestamp(g.GetUpdatedAt()),
	}
	if cfg := g.GetModelsConfig(); cfg != nil {
		r.ModelsConfig = &RoutingRuleModelsConfig{Mode: cfg.GetMode(), Models: make([]RoutingRuleModelRef, 0, len(cfg.GetModels()))}
		for _, m := range cfg.GetModels() {
			r.ModelsConfig.Models = append(r.ModelsConfig.Models, RoutingRuleModelRef{
				Model:         m.GetModel(),
				DisplayName:   m.GetDisplayName(),
				Weight:        m.Weight,
				IntegrationID: m.GetIntegrationId(),
			})
		}
	}
	return r
}

func modelsConfigToProto(c *RoutingRuleModelsConfig) *platformv1.RoutingRuleModelsConfig {
	if c == nil {
		return nil
	}
	out := &platformv1.RoutingRuleModelsConfig{Mode: c.Mode, Models: make([]*platformv1.RoutingRuleModel, 0, len(c.Models))}
	for _, m := range c.Models {
		out.Models = append(out.Models, &platformv1.RoutingRuleModel{
			Model:         m.Model,
			DisplayName:   m.DisplayName,
			Weight:        m.Weight,
			IntegrationId: m.IntegrationID,
		})
	}
	return out
}

func routingRulePriority(priority *int64) *int32 {
	if priority == nil {
		return nil
	}
	p := int32(*priority)
	return &p
}

func routingRuleExpression(cel *string) *platformv1.RoutingRuleExpression {
	if cel == nil {
		return nil
	}
	return &platformv1.RoutingRuleExpression{Cel: *cel}
}

func (c *connectRoutingRules) List(ctx context.Context, params ListParams) (*RoutingRulePage, error) {
	req := &platformv1.ListRoutingRulesRequest{StartingAfter: params.StartingAfter}
	if params.Limit > 0 {
		req.Limit = &params.Limit
	}
	resp, err := c.c.ListRoutingRules(ctx, req)
	if err != nil {
		return nil, mapConnectError("routing rule", err)
	}
	out := &RoutingRulePage{HasMore: resp.GetHasMore()}
	for _, g := range resp.GetData() {
		out.Rules = append(out.Rules, routingRuleFromProto(g))
	}
	return out, nil
}

func (c *connectRoutingRules) Get(ctx context.Context, id string) (*RoutingRule, error) {
	resp, err := c.c.GetRoutingRule(ctx, &platformv1.GetRoutingRuleRequest{RoutingRuleId: id})
	if err != nil {
		return nil, mapConnectError("routing rule", err)
	}
	r := routingRuleFromProto(resp.GetRoutingRule())
	return &r, nil
}

func (c *connectRoutingRules) Create(ctx context.Context, in RoutingRuleCreateInput) (*RoutingRule, error) {
	resp, err := c.c.CreateRoutingRule(ctx, &platformv1.CreateRoutingRuleRequest{
		DisplayName:  in.DisplayName,
		Description:  in.Description,
		Enabled:      in.Enabled,
		ProjectId:    in.ProjectID,
		Priority:     routingRulePriority(in.Priority),
		Expression:   routingRuleExpression(in.ExpressionCEL),
		ModelsConfig: modelsConfigToProto(in.ModelsConfig),
	})
	if err != nil {
		return nil, mapConnectError("routing rule", err)
	}
	r := routingRuleFromProto(resp.GetRoutingRule())
	return &r, nil
}

func (c *connectRoutingRules) Update(ctx context.Context, in RoutingRuleUpdateInput) (*RoutingRule, error) {
	req := &platformv1.UpdateRoutingRuleRequest{
		RoutingRuleId: in.ID,
		DisplayName:   in.DisplayName,
		Description:   in.Description,
		Enabled:       in.Enabled,
		Priority:      routingRulePriority(in.Priority),
		Expression:    routingRuleExpression(in.ExpressionCEL),
		ModelsConfig:  modelsConfigToProto(in.ModelsConfig),
	}
	if in.ClearModelsConfig && in.ModelsConfig == nil {
		clearModelsConfig := true
		req.ClearModelsConfig = &clearModelsConfig
	}
	resp, err := c.c.UpdateRoutingRule(ctx, req)
	if err != nil {
		return nil, mapConnectError("routing rule", err)
	}
	r := routingRuleFromProto(resp.GetRoutingRule())
	return &r, nil
}

func (c *connectRoutingRules) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteRoutingRule(ctx, &platformv1.DeleteRoutingRuleRequest{RoutingRuleId: id})
	return mapConnectError("routing rule", err)
}
