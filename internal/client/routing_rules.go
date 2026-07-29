package client

import (
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
// ModelsConfig is carried as raw JSON so its (evolving, deeply-nested) shape is
// preserved end-to-end without the provider modeling every field; a nil value
// means the field was absent.
type RoutingRule struct {
	ID            string
	DisplayName   string
	Description   string
	Enabled       bool
	ProjectID     string
	Priority      int64
	ExpressionCEL string
	ModelsConfig  json.RawMessage
	CreatedAt     string
	UpdatedAt     string
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
	ModelsConfig  json.RawMessage
}

// RoutingRuleUpdateInput is the update patch. There is no ProjectID: the update
// API omits project_id (verified against apps/platform-api/routingrules/routes.go
// — updateRoutingRuleRequest has no ProjectID field), so the resource marks it
// RequiresReplace.
type RoutingRuleUpdateInput struct {
	ID            string
	DisplayName   *string
	Description   *string
	Enabled       *bool
	Priority      *int64
	ExpressionCEL *string
	ModelsConfig  json.RawMessage
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
// models_config is kept as raw JSON so an update never drops or reshapes it.
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
	ModelsConfig json.RawMessage `json:"models_config"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func (w *routingRuleWire) toRule() RoutingRule {
	r := RoutingRule{
		ID:           w.ID,
		DisplayName:  w.DisplayName,
		Enabled:      w.Enabled,
		ProjectID:    w.ProjectID,
		Priority:     w.Priority,
		ModelsConfig: w.ModelsConfig,
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

// modelsConfigToRest unmarshals a raw models_config blob into the generated REST
// type. A nil blob yields nil (field omitted). A malformed blob is a validation
// error, not a transport failure.
func modelsConfigToRest(raw json.RawMessage) (*restgen.ModelsConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var mc restgen.ModelsConfig
	if err := json.Unmarshal(raw, &mc); err != nil {
		return nil, &Error{Code: CodeInvalid, Message: "invalid models_config: " + err.Error()}
	}
	return &mc, nil
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
	mc, err := modelsConfigToRest(in.ModelsConfig)
	if err != nil {
		return nil, err
	}
	body := restgen.RoutingRuleCreateJSONRequestBody{
		DisplayName:  in.DisplayName,
		Description:  in.Description,
		Enabled:      in.Enabled,
		Priority:     in.Priority,
		ProjectId:    in.ProjectID,
		ModelsConfig: mc,
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
	mc, err := modelsConfigToRest(in.ModelsConfig)
	if err != nil {
		return nil, err
	}
	body := restgen.RoutingRuleUpdateJSONRequestBody{
		DisplayName:  in.DisplayName,
		Description:  in.Description,
		Enabled:      in.Enabled,
		Priority:     in.Priority,
		ModelsConfig: mc,
	}
	if in.ExpressionCEL != nil {
		body.Expression = &restgen.ExpressionInput{Cel: *in.ExpressionCEL}
	}
	resp, err := p.c.RoutingRuleUpdateWithResponse(ctx, in.ID, body)
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
