package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// GuardrailRule is the transport-agnostic projection of a guardrail rule
// (router entity, REST-backed). ProjectID is empty for a workspace-global rule.
type GuardrailRule struct {
	ID          string
	DisplayName string
	Description string
	Enabled     bool
	ProjectID   string
	Timeout     int64
	Guardrails  []GuardrailRef
	CreatedAt   string
	UpdatedAt   string
}

// GuardrailRef is a reference to a guardrail evaluator from a rule. Options
// (an arbitrary per-guardrail JSON map) is not surfaced by the provider yet.
type GuardrailRef struct {
	ID          string
	ExecuteOn   string // input / output / both
	SampleRate  *float64
	IsGuardrail *bool
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
	Timeout     *int64
	Guardrails  []GuardrailRef
}

// GuardrailRuleUpdateInput is the update patch. There is no ProjectID: the
// update API omits project_id, so the resource marks it RequiresReplace.
type GuardrailRuleUpdateInput struct {
	ID          string
	DisplayName *string
	Description *string
	Enabled     *bool
	Timeout     *int64
	Guardrails  []GuardrailRef
}

// GuardrailRulesAPI is the per-resource seam for the guardrail-rules domain
// (REST-backed).
type GuardrailRulesAPI interface {
	List(ctx context.Context, params ListParams) (*GuardrailRulePage, error)
	Get(ctx context.Context, id string) (*GuardrailRule, error)
	Create(ctx context.Context, in GuardrailRuleCreateInput) (*GuardrailRule, error)
	Update(ctx context.Context, in GuardrailRuleUpdateInput) (*GuardrailRule, error)
	Delete(ctx context.Context, id string) error
}

type restGuardrailRules struct {
	c *restgen.ClientWithResponses
}

// guardrailRuleWire mirrors the JSON body every guardrail-rule read/write
// returns (create/get/update share this shape). Decoding the raw body avoids
// depending on oapi-codegen's per-operation anonymous struct types.
type guardrailRuleWire struct {
	ID          string  `json:"_id"`
	DisplayName string  `json:"display_name"`
	Description *string `json:"description"`
	Enabled     bool    `json:"enabled"`
	ProjectID   string  `json:"project_id"`
	Timeout     int64   `json:"timeout"`
	Guardrails  []struct {
		ID          string   `json:"id"`
		ExecuteOn   string   `json:"execute_on"`
		SampleRate  *float64 `json:"sample_rate"`
		IsGuardrail *bool    `json:"is_guardrail"`
	} `json:"guardrails"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (w *guardrailRuleWire) toRule() GuardrailRule {
	r := GuardrailRule{
		ID:          w.ID,
		DisplayName: w.DisplayName,
		Enabled:     w.Enabled,
		ProjectID:   w.ProjectID,
		Timeout:     w.Timeout,
		CreatedAt:   w.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   w.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if w.Description != nil {
		r.Description = *w.Description
	}
	for _, g := range w.Guardrails {
		r.Guardrails = append(r.Guardrails, GuardrailRef{
			ID:          g.ID,
			ExecuteOn:   g.ExecuteOn,
			SampleRate:  g.SampleRate,
			IsGuardrail: g.IsGuardrail,
		})
	}
	return r
}

// decodeRuleBody parses a single-rule JSON body into the transport-neutral type.
func decodeRuleBody(body []byte) (*GuardrailRule, error) {
	var w guardrailRuleWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding guardrail rule response: " + err.Error()}
	}
	r := w.toRule()
	return &r, nil
}

func guardrailRefsToRest(refs []GuardrailRef) *[]restgen.GuardrailRef {
	if refs == nil {
		return nil
	}
	out := make([]restgen.GuardrailRef, 0, len(refs))
	for _, g := range refs {
		rg := restgen.GuardrailRef{
			Id:          g.ID,
			ExecuteOn:   restgen.GuardrailRefExecuteOn(g.ExecuteOn),
			SampleRate:  g.SampleRate,
			IsGuardrail: g.IsGuardrail,
		}
		out = append(out, rg)
	}
	return &out
}

func (p *restGuardrailRules) List(ctx context.Context, params ListParams) (*GuardrailRulePage, error) {
	restParams := &restgen.GuardrailRuleListParams{}
	if params.Limit > 0 {
		limit := int64(params.Limit)
		restParams.Limit = &limit
	}
	if params.StartingAfter != "" {
		restParams.StartingAfter = &params.StartingAfter
	}
	resp, err := p.c.GuardrailRuleListWithResponse(ctx, restParams)
	if err != nil {
		return nil, mapRESTTransportError(err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	out := &GuardrailRulePage{HasMore: resp.JSON200.HasMore}
	if resp.JSON200.Data != nil {
		for _, g := range *resp.JSON200.Data {
			w := guardrailRuleWire{
				ID:          g.UnderscoreId,
				DisplayName: g.DisplayName,
				Description: g.Description,
				Enabled:     g.Enabled,
				ProjectID:   g.ProjectId,
				Timeout:     g.Timeout,
				CreatedAt:   g.CreatedAt,
				UpdatedAt:   g.UpdatedAt,
			}
			if g.Guardrails != nil {
				for _, ref := range *g.Guardrails {
					w.Guardrails = append(w.Guardrails, struct {
						ID          string   `json:"id"`
						ExecuteOn   string   `json:"execute_on"`
						SampleRate  *float64 `json:"sample_rate"`
						IsGuardrail *bool    `json:"is_guardrail"`
					}{ID: ref.Id, ExecuteOn: string(ref.ExecuteOn), SampleRate: ref.SampleRate, IsGuardrail: ref.IsGuardrail})
				}
			}
			out.Rules = append(out.Rules, w.toRule())
		}
	}
	return out, nil
}

func (p *restGuardrailRules) Get(ctx context.Context, id string) (*GuardrailRule, error) {
	resp, err := p.c.GuardrailRuleGetWithResponse(ctx, id)
	if err != nil {
		return nil, mapRESTTransportError(err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeRuleBody(resp.Body)
}

func (p *restGuardrailRules) Create(ctx context.Context, in GuardrailRuleCreateInput) (*GuardrailRule, error) {
	body := restgen.GuardrailRuleCreateJSONRequestBody{
		DisplayName: in.DisplayName,
		Description: in.Description,
		Enabled:     in.Enabled,
		ProjectId:   in.ProjectID,
		Timeout:     in.Timeout,
		Guardrails:  guardrailRefsToRest(in.Guardrails),
	}
	resp, err := p.c.GuardrailRuleCreateWithResponse(ctx, body)
	if err != nil {
		return nil, mapRESTTransportError(err)
	}
	if resp.StatusCode() != http.StatusCreated && resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeRuleBody(resp.Body)
}

func (p *restGuardrailRules) Update(ctx context.Context, in GuardrailRuleUpdateInput) (*GuardrailRule, error) {
	body := restgen.GuardrailRuleUpdateJSONRequestBody{
		DisplayName: in.DisplayName,
		Description: in.Description,
		Enabled:     in.Enabled,
		Timeout:     in.Timeout,
		Guardrails:  guardrailRefsToRest(in.Guardrails),
	}
	resp, err := p.c.GuardrailRuleUpdateWithResponse(ctx, in.ID, body)
	if err != nil {
		return nil, mapRESTTransportError(err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeRuleBody(resp.Body)
}

func (p *restGuardrailRules) Delete(ctx context.Context, id string) error {
	resp, err := p.c.GuardrailRuleDeleteWithResponse(ctx, id)
	if err != nil {
		return mapRESTTransportError(err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}
