package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// RoutingRule is the transport-agnostic projection of a routing rule (router
// entity, REST-backed). ProjectID is empty for a workspace-global rule.
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
// API omits project_id (verified against apps/platform-api/routingrules/routes.go
// — updateRoutingRuleRequest has no ProjectID field), so the resource marks it
// RequiresReplace.
//
// A nil ModelsConfig leaves the stored one untouched; ClearModelsConfig removes
// it (see routingRuleUpdatePayload).
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
// (REST-backed).
type RoutingRulesAPI interface {
	List(ctx context.Context, params ListParams) (*RoutingRulePage, error)
	Get(ctx context.Context, id string) (*RoutingRule, error)
	Create(ctx context.Context, in RoutingRuleCreateInput) (*RoutingRule, error)
	Update(ctx context.Context, in RoutingRuleUpdateInput) (*RoutingRule, error)
	Delete(ctx context.Context, id string) error
}

type restRoutingRules struct {
	c *restgen.ClientWithResponses
}

// routingRuleWire mirrors the JSON body every routing-rule read/write returns.
// The server emits proto zero values, so an absent models_config arrives as an
// explicit `null` and its leaf strings as "".
type routingRuleWire struct {
	ID          string  `json:"_id"`
	DisplayName string  `json:"display_name"`
	Description *string `json:"description"`
	Enabled     bool    `json:"enabled"`
	ProjectID   string  `json:"project_id"`
	Priority    int64   `json:"priority"`
	Expression  *struct {
		Cel string `json:"cel"`
	} `json:"expression"`
	ModelsConfig *modelsConfigWire `json:"models_config"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

type modelsConfigWire struct {
	Mode   string         `json:"mode"`
	Models []modelRefWire `json:"models"`
}

type modelRefWire struct {
	Model         string   `json:"model"`
	DisplayName   string   `json:"display_name"`
	Weight        *float64 `json:"weight"`
	IntegrationID string   `json:"integration_id"`
}

func (w *modelsConfigWire) toConfig() *RoutingRuleModelsConfig {
	if w == nil {
		return nil
	}
	out := &RoutingRuleModelsConfig{Mode: w.Mode, Models: make([]RoutingRuleModelRef, 0, len(w.Models))}
	for _, m := range w.Models {
		out.Models = append(out.Models, RoutingRuleModelRef{
			Model:         m.Model,
			DisplayName:   m.DisplayName,
			Weight:        m.Weight,
			IntegrationID: m.IntegrationID,
		})
	}
	return out
}

func (w *routingRuleWire) toRule() RoutingRule {
	r := RoutingRule{
		ID:           w.ID,
		DisplayName:  w.DisplayName,
		Enabled:      w.Enabled,
		ProjectID:    w.ProjectID,
		Priority:     w.Priority,
		ModelsConfig: w.ModelsConfig.toConfig(),
		CreatedAt:    w.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    w.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if w.Description != nil {
		r.Description = *w.Description
	}
	if w.Expression != nil {
		r.ExpressionCEL = w.Expression.Cel
	}
	return r
}

func decodeRoutingRuleBody(body []byte) (*RoutingRule, error) {
	var w routingRuleWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding routing rule response: " + err.Error()}
	}
	r := w.toRule()
	return &r, nil
}

// modelsConfigToREST maps the domain config onto the generated request type. A
// nil config yields nil, which the `models_config,omitempty` request field omits
// entirely — the server rejects an explicit null.
func modelsConfigToREST(c *RoutingRuleModelsConfig) *restgen.ModelsConfig {
	if c == nil {
		return nil
	}
	models := make([]restgen.ModelRef, 0, len(c.Models))
	for _, m := range c.Models {
		ref := restgen.ModelRef{Model: m.Model, Weight: m.Weight}
		if m.DisplayName != "" {
			ref.DisplayName = &m.DisplayName
		}
		if m.IntegrationID != "" {
			ref.IntegrationId = &m.IntegrationID
		}
		models = append(models, ref)
	}
	return &restgen.ModelsConfig{Mode: restgen.ModelsConfigMode(c.Mode), Models: &models}
}

// routingRuleUpdatePayload is the generated update body plus
// clear_models_config, a request field the public OpenAPI spec hides
// (x-orq-hidden) but the server honors — the only way a PATCH can remove a
// stored models_config, since an omitted or null one is a no-op.
type routingRuleUpdatePayload struct {
	restgen.RoutingRuleUpdateJSONRequestBody
	ClearModelsConfig bool `json:"clear_models_config,omitempty"`
}

func (p *restRoutingRules) List(ctx context.Context, params ListParams) (*RoutingRulePage, error) {
	restParams := &restgen.RoutingRuleListParams{}
	if params.Limit > 0 {
		limit := int64(params.Limit)
		restParams.Limit = &limit
	}
	if params.StartingAfter != "" {
		restParams.StartingAfter = &params.StartingAfter
	}
	resp, err := p.c.RoutingRuleListWithResponse(ctx, restParams)
	if err != nil {
		return nil, mapRESTTransportError("routing rule", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	// Decode the raw list envelope so the per-item shape is the same wire struct
	// the single-rule reads use (avoids the oapi-codegen anonymous list types).
	var env struct {
		Data    []routingRuleWire `json:"data"`
		HasMore bool              `json:"has_more"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding routing rule list: " + err.Error()}
	}
	out := &RoutingRulePage{HasMore: env.HasMore}
	for i := range env.Data {
		out.Rules = append(out.Rules, env.Data[i].toRule())
	}
	return out, nil
}

func (p *restRoutingRules) Get(ctx context.Context, id string) (*RoutingRule, error) {
	resp, err := p.c.RoutingRuleGetWithResponse(ctx, id)
	if err != nil {
		return nil, mapRESTTransportError("routing rule", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeRoutingRuleBody(resp.Body)
}

func (p *restRoutingRules) Create(ctx context.Context, in RoutingRuleCreateInput) (*RoutingRule, error) {
	body := restgen.RoutingRuleCreateJSONRequestBody{
		DisplayName:  in.DisplayName,
		Description:  in.Description,
		Enabled:      in.Enabled,
		Priority:     in.Priority,
		ProjectId:    in.ProjectID,
		ModelsConfig: modelsConfigToREST(in.ModelsConfig),
	}
	if in.ExpressionCEL != nil {
		body.Expression = &restgen.ExpressionInput{Cel: *in.ExpressionCEL}
	}
	resp, err := p.c.RoutingRuleCreateWithResponse(ctx, body)
	if err != nil {
		return nil, mapRESTTransportError("routing rule", err)
	}
	if resp.StatusCode() != http.StatusCreated && resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeRoutingRuleBody(resp.Body)
}

func (p *restRoutingRules) Update(ctx context.Context, in RoutingRuleUpdateInput) (*RoutingRule, error) {
	payload := routingRuleUpdatePayload{
		RoutingRuleUpdateJSONRequestBody: restgen.RoutingRuleUpdateJSONRequestBody{
			DisplayName:  in.DisplayName,
			Description:  in.Description,
			Enabled:      in.Enabled,
			Priority:     in.Priority,
			ModelsConfig: modelsConfigToREST(in.ModelsConfig),
		},
		ClearModelsConfig: in.ClearModelsConfig && in.ModelsConfig == nil,
	}
	if in.ExpressionCEL != nil {
		payload.Expression = &restgen.ExpressionInput{Cel: *in.ExpressionCEL}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "encoding routing rule update request: " + err.Error()}
	}
	resp, err := p.c.RoutingRuleUpdateWithBodyWithResponse(ctx, in.ID, jsonContentType, bytes.NewReader(body))
	if err != nil {
		return nil, mapRESTTransportError("routing rule", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeRoutingRuleBody(resp.Body)
}

func (p *restRoutingRules) Delete(ctx context.Context, id string) error {
	resp, err := p.c.RoutingRuleDeleteWithResponse(ctx, id)
	if err != nil {
		return mapRESTTransportError("routing rule", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}
