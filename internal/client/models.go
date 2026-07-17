package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// Model is the transport-agnostic projection of a CUSTOM OpenAI-compatible
// ("openai-like") model, REST-backed via /v2/models/openai-like. Only the
// fields the server reliably echoes on create/read are modeled here.
//
// The write-only inputs the server does NOT round-trip — the secret api_key
// (never echoed; only its env-var NAME appears, as configuration.api_key_env)
// and the scattered/derived metadata (costs, max_tokens, temperature,
// has_reasoning, supports_*) — are deliberately absent: the resource keeps
// those config-authoritative so a server default/transform can never manufacture
// drift. BaseURL and Region live under the server's `configuration` sub-object;
// they are surfaced flat here.
type Model struct {
	ID          string
	DisplayName string
	ModelID     string
	ModelType   string
	Region      string // from configuration.region
	BaseURL     string // from configuration.base_url
	Description string // "" when the server omits it
	Created     string // RFC 3339 (UTC)
	Updated     string // RFC 3339 (UTC)
}

// ModelCreateInput carries the fields for a create. The six leading fields are
// required by POST /v2/models/openai-like; the rest are optional (nil => omitted
// from the sparse write).
type ModelCreateInput struct {
	APIKey      string
	BaseURL     string
	DisplayName string
	ModelID     string
	ModelType   string
	Region      string

	Description         *string
	InputCost           *float64
	OutputCost          *float64
	CostPerImage        *float64
	MaxTokens           *int64
	Temperature         *float64
	HasReasoning        *bool
	SupportsVision      *bool
	SupportsToolCalling *bool
	SupportsStrictTool  *bool
	SupportsImageEdit   *bool
}

// ModelUpdateInput is the update patch. There is no APIKey: the update endpoint
// (PATCH /v2/models/openai-like/{id}) does not accept it — the server re-probes
// the target API with the stored encrypted key — so the secret is immutable in
// place and the resource marks api_key RequiresReplace. DisplayName, ModelType
// and Region are required by the update body; BaseURL and ModelID are always
// sent (they are Required resource attributes).
type ModelUpdateInput struct {
	ID          string
	BaseURL     string
	DisplayName string
	ModelID     string
	ModelType   string
	Region      string

	Description         *string
	InputCost           *float64
	OutputCost          *float64
	CostPerImage        *float64
	MaxTokens           *int64
	Temperature         *float64
	HasReasoning        *bool
	SupportsVision      *bool
	SupportsToolCalling *bool
	SupportsStrictTool  *bool
	SupportsImageEdit   *bool
}

// ModelsAPI is the per-resource seam for custom openai-like models (REST-backed).
// There is no single-GET route, so Get lists /v2/models and filters by id; an
// absent id normalizes to a not_found *Error so the resource drops it from state.
type ModelsAPI interface {
	Get(ctx context.Context, id string) (*Model, error)
	Create(ctx context.Context, in ModelCreateInput) (*Model, error)
	Update(ctx context.Context, in ModelUpdateInput) (*Model, error)
	Delete(ctx context.Context, id string) error
}

// restModels implements ModelsAPI over the REST /v2/models/openai-like endpoints.
type restModels struct {
	c *restgen.ClientWithResponses
}

// modelWire mirrors the JSON body every model read/write returns (the list
// item, create and update responses all share the ModelDocument shape).
// Decoding the raw body — as the policy/guardrail adapters do — avoids depending
// on oapi-codegen's per-operation anonymous response struct types. Only the
// reliably-echoed fields are pulled out; everything else on the wire is ignored.
type modelWire struct {
	ID            string  `json:"id"`
	DisplayName   string  `json:"display_name"`
	ModelID       string  `json:"model_id"`
	ModelType     string  `json:"model_type"`
	Description   *string `json:"description"`
	Configuration struct {
		BaseURL *string `json:"base_url"`
		Region  *string `json:"region"`
	} `json:"configuration"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

func (w *modelWire) toModel() Model {
	m := Model{
		ID:          w.ID,
		DisplayName: w.DisplayName,
		ModelID:     w.ModelID,
		ModelType:   w.ModelType,
		Created:     w.Created.UTC().Format(time.RFC3339),
		Updated:     w.Updated.UTC().Format(time.RFC3339),
	}
	if w.Description != nil {
		m.Description = *w.Description
	}
	if w.Configuration.BaseURL != nil {
		m.BaseURL = *w.Configuration.BaseURL
	}
	if w.Configuration.Region != nil {
		m.Region = *w.Configuration.Region
	}
	return m
}

// decodeModelBody parses a single-model JSON body (create/update response) into
// the transport-neutral type.
func decodeModelBody(body []byte) (*Model, error) {
	var w modelWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding model response: " + err.Error()}
	}
	m := w.toModel()
	return &m, nil
}

// modelFromDocument projects the generated ModelDocument (list item) onto the
// transport-neutral Model.
func modelFromDocument(d *restgen.ModelDocument) Model {
	m := Model{
		ID:          d.Id,
		DisplayName: d.DisplayName,
		ModelID:     d.ModelId,
		ModelType:   d.ModelType,
		Created:     d.Created.UTC().Format(time.RFC3339),
		Updated:     d.Updated.UTC().Format(time.RFC3339),
	}
	if d.Description != nil {
		m.Description = *d.Description
	}
	if d.Configuration.BaseUrl != nil {
		m.BaseURL = *d.Configuration.BaseUrl
	}
	if d.Configuration.Region != nil {
		m.Region = *d.Configuration.Region
	}
	return m
}

func (r *restModels) Get(ctx context.Context, id string) (*Model, error) {
	resp, err := r.c.ModelListWithResponse(ctx)
	if err != nil {
		return nil, mapRESTTransportError("model", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	for i := range *resp.JSON200 {
		d := &(*resp.JSON200)[i]
		if d.Id != id {
			continue
		}
		m := modelFromDocument(d)
		return &m, nil
	}
	// Not present in the catalog → gone server-side. Normalize to not_found so
	// the resource Read removes it from state (and Delete treats it as done).
	return nil, &Error{Code: CodeNotFound, Message: "the requested model was not found"}
}

func (r *restModels) Create(ctx context.Context, in ModelCreateInput) (*Model, error) {
	body := restgen.ModelCreateOpenAILikeJSONRequestBody{
		ApiKey:              in.APIKey,
		BaseUrl:             in.BaseURL,
		DisplayName:         in.DisplayName,
		ModelId:             in.ModelID,
		ModelType:           in.ModelType,
		Region:              in.Region,
		Description:         in.Description,
		InputCost:           in.InputCost,
		OutputCost:          in.OutputCost,
		CostPerImage:        in.CostPerImage,
		MaxTokens:           in.MaxTokens,
		Temperature:         in.Temperature,
		HasReasoning:        in.HasReasoning,
		SupportsVision:      in.SupportsVision,
		SupportsToolCalling: in.SupportsToolCalling,
		SupportsStrictTool:  in.SupportsStrictTool,
		SupportsImageEdit:   in.SupportsImageEdit,
	}
	resp, err := r.c.ModelCreateOpenAILikeWithResponse(ctx, body)
	if err != nil {
		return nil, mapRESTTransportError("model", err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusCreated {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeModelBody(resp.Body)
}

func (r *restModels) Update(ctx context.Context, in ModelUpdateInput) (*Model, error) {
	baseURL := in.BaseURL
	modelID := in.ModelID
	body := restgen.ModelUpdateOpenAILikeJSONRequestBody{
		DisplayName:         in.DisplayName,
		ModelType:           in.ModelType,
		Region:              in.Region,
		BaseUrl:             &baseURL,
		ModelId:             &modelID,
		Description:         in.Description,
		InputCost:           in.InputCost,
		OutputCost:          in.OutputCost,
		CostPerImage:        in.CostPerImage,
		MaxTokens:           in.MaxTokens,
		Temperature:         in.Temperature,
		HasReasoning:        in.HasReasoning,
		SupportsVision:      in.SupportsVision,
		SupportsToolCalling: in.SupportsToolCalling,
		SupportsStrictTool:  in.SupportsStrictTool,
		SupportsImageEdit:   in.SupportsImageEdit,
	}
	resp, err := r.c.ModelUpdateOpenAILikeWithResponse(ctx, in.ID, body)
	if err != nil {
		return nil, mapRESTTransportError("model", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	return decodeModelBody(resp.Body)
}

func (r *restModels) Delete(ctx context.Context, id string) error {
	resp, err := r.c.ModelDeleteWithResponse(ctx, id)
	if err != nil {
		return mapRESTTransportError("model", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}
