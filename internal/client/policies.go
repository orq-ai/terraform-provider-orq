package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// Policy is the transport-agnostic projection of a policy (router entity). The
// resource layer sees this, not the oapi-codegen generated type.
//
// ModelsConfig, Limits and RetryConfig are carried as raw JSON so their
// (evolving, deeply-nested) shapes survive a round-trip without the provider
// modeling every field; a nil value means the field was absent. Evaluators are
// modeled richly (they mirror guardrail refs).
type Policy struct {
	ID          string
	DisplayName string
	Description string
	Enabled     bool
	// ProjectID is empty for a workspace-global policy. Unlike routing/guardrail
	// rules, policy project_id is MUTABLE (the update API accepts it — verified
	// against apps/platform-api/policies/routes.go updatePolicyRequest), so the
	// resource does NOT mark it RequiresReplace.
	ProjectID    string
	Slug         string
	Timeout      int64
	Evaluators   []EvaluatorRef
	Limits       json.RawMessage
	ModelsConfig json.RawMessage
	RetryConfig  json.RawMessage
	CreatedAt    string
	UpdatedAt    string
}

// EvaluatorRef is a reference to an evaluator from a policy. Its shape mirrors
// GuardrailRef; Options is arbitrary per-evaluator config carried as a decoded
// JSON object.
type EvaluatorRef struct {
	ID          string
	ExecuteOn   string
	SampleRate  *float64
	IsGuardrail *bool
	Options     map[string]any
}

// PolicyPage is one page of a cursor-paginated list.
type PolicyPage struct {
	Policies []Policy
	HasMore  bool
}

// PolicyCreateInput carries the fields for a create.
type PolicyCreateInput struct {
	DisplayName  string
	Description  *string
	Enabled      *bool
	ProjectID    *string
	Timeout      *int64
	Evaluators   []EvaluatorRef
	Limits       json.RawMessage
	ModelsConfig json.RawMessage
	RetryConfig  json.RawMessage
}

// PolicyUpdateInput is the update patch. ProjectID is included because policy
// project_id is mutable on update.
type PolicyUpdateInput struct {
	ID           string
	DisplayName  *string
	Description  *string
	Enabled      *bool
	ProjectID    *string
	Timeout      *int64
	Evaluators   []EvaluatorRef
	Limits       json.RawMessage
	ModelsConfig json.RawMessage
	RetryConfig  json.RawMessage
}

// PoliciesAPI is the per-resource seam for the policies domain (REST-backed).
type PoliciesAPI interface {
	List(ctx context.Context, params ListParams) (*PolicyPage, error)
	Get(ctx context.Context, id string) (*Policy, error)
	Create(ctx context.Context, in PolicyCreateInput) (*Policy, error)
	Update(ctx context.Context, in PolicyUpdateInput) (*Policy, error)
	Delete(ctx context.Context, id string) error
}

// restPolicies implements PoliciesAPI over the REST /v2/policies endpoint.
type restPolicies struct {
	c *restgen.ClientWithResponses
}

// policyWire mirrors the JSON body every policy read/write returns.
type policyWire struct {
	ID          string  `json:"_id"`
	DisplayName string  `json:"display_name"`
	Description *string `json:"description"`
	Enabled     bool    `json:"enabled"`
	ProjectID   string  `json:"project_id"`
	Slug        string  `json:"slug"`
	Timeout     int64   `json:"timeout"`
	Evaluators  []struct {
		ID          string         `json:"id"`
		ExecuteOn   string         `json:"execute_on"`
		SampleRate  *float64       `json:"sample_rate"`
		IsGuardrail *bool          `json:"is_guardrail"`
		Options     map[string]any `json:"options"`
	} `json:"evaluators"`
	Limits       json.RawMessage `json:"limits"`
	ModelsConfig json.RawMessage `json:"models_config"`
	RetryConfig  json.RawMessage `json:"retry_config"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func (w *policyWire) toPolicy() Policy {
	p := Policy{
		ID:           w.ID,
		DisplayName:  w.DisplayName,
		Enabled:      w.Enabled,
		ProjectID:    w.ProjectID,
		Slug:         w.Slug,
		Timeout:      w.Timeout,
		Limits:       w.Limits,
		ModelsConfig: w.ModelsConfig,
		RetryConfig:  w.RetryConfig,
		CreatedAt:    w.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    w.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if w.Description != nil {
		p.Description = *w.Description
	}
	for _, e := range w.Evaluators {
		p.Evaluators = append(p.Evaluators, EvaluatorRef{
			ID:          e.ID,
			ExecuteOn:   e.ExecuteOn,
			SampleRate:  e.SampleRate,
			IsGuardrail: e.IsGuardrail,
			Options:     e.Options,
		})
	}
	return p
}

func decodePolicyBody(body []byte) (*Policy, error) {
	var w policyWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding policy response: " + err.Error()}
	}
	p := w.toPolicy()
	return &p, nil
}

func evaluatorRefsToRest(refs []EvaluatorRef) *[]restgen.EvaluatorRef {
	if refs == nil {
		return nil
	}
	out := make([]restgen.EvaluatorRef, 0, len(refs))
	for _, e := range refs {
		re := restgen.EvaluatorRef{
			Id:          e.ID,
			ExecuteOn:   restgen.EvaluatorRefExecuteOn(e.ExecuteOn),
			SampleRate:  e.SampleRate,
			IsGuardrail: e.IsGuardrail,
		}
		if e.Options != nil {
			opts := map[string]interface{}(e.Options)
			re.Options = &opts
		}
		out = append(out, re)
	}
	return &out
}

func limitsToRest(raw json.RawMessage) (*restgen.Limits, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var l restgen.Limits
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, &Error{Code: CodeInvalid, Message: "invalid limits: " + err.Error()}
	}
	return &l, nil
}

func retryConfigToRest(raw json.RawMessage) (*restgen.PolicyRetryConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rc restgen.PolicyRetryConfig
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, &Error{Code: CodeInvalid, Message: "invalid retry_config: " + err.Error()}
	}
	return &rc, nil
}

func (p *restPolicies) List(ctx context.Context, params ListParams) (*PolicyPage, error) {
	restParams := &restgen.PolicyListParams{}
	if params.Limit > 0 {
		limit := int64(params.Limit)
		restParams.Limit = &limit
	}
	if params.StartingAfter != "" {
		restParams.StartingAfter = &params.StartingAfter
	}

	resp, err := p.c.PolicyListWithResponse(ctx, restParams)
	if err != nil {
		return nil, mapRESTTransportError("policy", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	var env struct {
		Data    []policyWire `json:"data"`
		HasMore bool         `json:"has_more"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding policy list: " + err.Error()}
	}
	out := &PolicyPage{HasMore: env.HasMore}
	for i := range env.Data {
		out.Policies = append(out.Policies, env.Data[i].toPolicy())
	}
	return out, nil
}

func (p *restPolicies) Get(ctx context.Context, id string) (*Policy, error) {
	resp, err := p.c.PolicyGetWithResponse(ctx, id)
	if err != nil {
		return nil, mapRESTTransportError("policy", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodePolicyBody(resp.Body)
}

func (p *restPolicies) Create(ctx context.Context, in PolicyCreateInput) (*Policy, error) {
	limits, err := limitsToRest(in.Limits)
	if err != nil {
		return nil, err
	}
	mc, err := modelsConfigToRest(in.ModelsConfig)
	if err != nil {
		return nil, err
	}
	rc, err := retryConfigToRest(in.RetryConfig)
	if err != nil {
		return nil, err
	}
	body := restgen.PolicyCreateJSONRequestBody{
		DisplayName:  in.DisplayName,
		Description:  in.Description,
		Enabled:      in.Enabled,
		ProjectId:    in.ProjectID,
		Timeout:      in.Timeout,
		Evaluators:   evaluatorRefsToRest(in.Evaluators),
		Limits:       limits,
		ModelsConfig: mc,
		RetryConfig:  rc,
	}
	resp, err := p.c.PolicyCreateWithResponse(ctx, body)
	if err != nil {
		return nil, mapRESTTransportError("policy", err)
	}
	if resp.StatusCode() != http.StatusCreated && resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodePolicyBody(resp.Body)
}

func (p *restPolicies) Update(ctx context.Context, in PolicyUpdateInput) (*Policy, error) {
	limits, err := limitsToRest(in.Limits)
	if err != nil {
		return nil, err
	}
	mc, err := modelsConfigToRest(in.ModelsConfig)
	if err != nil {
		return nil, err
	}
	rc, err := retryConfigToRest(in.RetryConfig)
	if err != nil {
		return nil, err
	}
	body := restgen.PolicyUpdateJSONRequestBody{
		DisplayName:  in.DisplayName,
		Description:  in.Description,
		Enabled:      in.Enabled,
		ProjectId:    in.ProjectID,
		Timeout:      in.Timeout,
		Evaluators:   evaluatorRefsToRest(in.Evaluators),
		Limits:       limits,
		ModelsConfig: mc,
		RetryConfig:  rc,
	}
	resp, err := p.c.PolicyUpdateWithResponse(ctx, in.ID, body)
	if err != nil {
		return nil, mapRESTTransportError("policy", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodePolicyBody(resp.Body)
}

func (p *restPolicies) Delete(ctx context.Context, id string) error {
	resp, err := p.c.PolicyDeleteWithResponse(ctx, id)
	if err != nil {
		return mapRESTTransportError("policy", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}
