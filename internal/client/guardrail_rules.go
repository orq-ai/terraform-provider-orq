package client

import (
	"context"

	"google.golang.org/protobuf/types/known/structpb"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// GuardrailRule is the transport-agnostic projection of a guardrail rule
// (Connect-backed). ProjectID is empty for a workspace-global rule.
type GuardrailRule struct {
	ID          string
	DisplayName string
	Description string
	Enabled     bool
	ProjectID   string
	Guardrails  []GuardrailRef
	CreatedAt   string
	UpdatedAt   string
}

// GuardrailRef is a reference to a guardrail evaluator from a rule. Options is
// arbitrary per-guardrail config (PII language/threshold/entities, etc.); it is
// carried transport-neutrally as a decoded JSON object so an update never drops
// it. A nil map means "no options" (omitted from the write).
type GuardrailRef struct {
	ID          string
	ExecuteOn   string // input / output / both
	SampleRate  *float64
	IsGuardrail *bool
	Options     map[string]any
}

// GuardrailRulePage is one page of a cursor-paginated list.
type GuardrailRulePage struct {
	Rules   []GuardrailRule
	HasMore bool
}

// GuardrailRuleCreateInput carries the fields for a create.
type GuardrailRuleCreateInput struct {
	DisplayName string
	Description *string
	Enabled     *bool
	ProjectID   *string
	Guardrails  []GuardrailRef
}

// GuardrailRuleUpdateInput is the update patch. There is no ProjectID: the
// update API omits project_id, so the resource marks it RequiresReplace.
type GuardrailRuleUpdateInput struct {
	ID          string
	DisplayName *string
	Description *string
	Enabled     *bool
	Guardrails  []GuardrailRef
}

// GuardrailRulesAPI is the per-resource seam for the guardrail-rules domain
// (Connect-backed).
type GuardrailRulesAPI interface {
	List(ctx context.Context, params ListParams) (*GuardrailRulePage, error)
	Get(ctx context.Context, id string) (*GuardrailRule, error)
	Create(ctx context.Context, in GuardrailRuleCreateInput) (*GuardrailRule, error)
	Update(ctx context.Context, in GuardrailRuleUpdateInput) (*GuardrailRule, error)
	Delete(ctx context.Context, id string) error
}

type connectGuardrailRules struct {
	c platformv1connect.GuardrailRulesServiceClient
}

func guardrailRuleFromProto(g *platformv1.GuardrailRule) GuardrailRule {
	r := GuardrailRule{
		ID:          g.GetId(),
		DisplayName: g.GetDisplayName(),
		Description: g.GetDescription(),
		Enabled:     g.GetEnabled(),
		ProjectID:   g.GetProjectId(),
		CreatedAt:   formatTimestamp(g.GetCreatedAt()),
		UpdatedAt:   formatTimestamp(g.GetUpdatedAt()),
	}
	for _, ref := range g.GetGuardrails() {
		out := GuardrailRef{
			ID:          ref.GetId(),
			ExecuteOn:   ref.GetExecuteOn(),
			SampleRate:  ref.SampleRate,
			IsGuardrail: ref.IsGuardrail,
		}
		if ref.GetOptions() != nil {
			out.Options = ref.GetOptions().AsMap()
		}
		r.Guardrails = append(r.Guardrails, out)
	}
	return r
}

func guardrailRefsToProto(refs []GuardrailRef) ([]*platformv1.GuardrailRuleGuardrail, error) {
	out := make([]*platformv1.GuardrailRuleGuardrail, 0, len(refs))
	for _, g := range refs {
		ref := &platformv1.GuardrailRuleGuardrail{
			Id:          g.ID,
			ExecuteOn:   g.ExecuteOn,
			SampleRate:  g.SampleRate,
			IsGuardrail: g.IsGuardrail,
		}
		if g.Options != nil {
			options, err := structpb.NewStruct(g.Options)
			if err != nil {
				return nil, &Error{Code: CodeInvalid, Message: "encoding guardrail options: " + err.Error()}
			}
			ref.Options = options
		}
		out = append(out, ref)
	}
	return out, nil
}

func (c *connectGuardrailRules) List(ctx context.Context, params ListParams) (*GuardrailRulePage, error) {
	req := &platformv1.ListGuardrailRulesRequest{StartingAfter: params.StartingAfter}
	if params.Limit > 0 {
		req.Limit = &params.Limit
	}
	resp, err := c.c.ListGuardrailRules(ctx, req)
	if err != nil {
		return nil, mapConnectError("guardrail rule", err)
	}
	out := &GuardrailRulePage{HasMore: resp.GetHasMore()}
	for _, g := range resp.GetData() {
		out.Rules = append(out.Rules, guardrailRuleFromProto(g))
	}
	return out, nil
}

func (c *connectGuardrailRules) Get(ctx context.Context, id string) (*GuardrailRule, error) {
	resp, err := c.c.GetGuardrailRule(ctx, &platformv1.GetGuardrailRuleRequest{GuardrailRuleId: id})
	if err != nil {
		return nil, mapConnectError("guardrail rule", err)
	}
	r := guardrailRuleFromProto(resp.GetGuardrailRule())
	return &r, nil
}

func (c *connectGuardrailRules) Create(ctx context.Context, in GuardrailRuleCreateInput) (*GuardrailRule, error) {
	refs, err := guardrailRefsToProto(in.Guardrails)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.CreateGuardrailRule(ctx, &platformv1.CreateGuardrailRuleRequest{
		DisplayName: in.DisplayName,
		Description: in.Description,
		Enabled:     in.Enabled,
		ProjectId:   in.ProjectID,
		Guardrails:  refs,
	})
	if err != nil {
		return nil, mapConnectError("guardrail rule", err)
	}
	r := guardrailRuleFromProto(resp.GetGuardrailRule())
	return &r, nil
}

// Update always replaces the guardrail list: the resource owns it, and an
// empty list is otherwise a no-op on the server.
func (c *connectGuardrailRules) Update(ctx context.Context, in GuardrailRuleUpdateInput) (*GuardrailRule, error) {
	refs, err := guardrailRefsToProto(in.Guardrails)
	if err != nil {
		return nil, err
	}
	replaceGuardrails := true
	resp, err := c.c.UpdateGuardrailRule(ctx, &platformv1.UpdateGuardrailRuleRequest{
		GuardrailRuleId:   in.ID,
		DisplayName:       in.DisplayName,
		Description:       in.Description,
		Enabled:           in.Enabled,
		Guardrails:        refs,
		ReplaceGuardrails: &replaceGuardrails,
	})
	if err != nil {
		return nil, mapConnectError("guardrail rule", err)
	}
	r := guardrailRuleFromProto(resp.GetGuardrailRule())
	return &r, nil
}

func (c *connectGuardrailRules) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteGuardrailRule(ctx, &platformv1.DeleteGuardrailRuleRequest{GuardrailRuleId: id})
	return mapConnectError("guardrail rule", err)
}
