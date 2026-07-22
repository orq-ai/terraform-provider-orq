package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// ModelProviderOpenAILike is the `provider` discriminator the server stamps on
// a CUSTOM OpenAI-compatible model (apps/platform-api/models/openai_like.go).
// System models carry owner "system" and a real provider ("openai", ...); other
// custom models carry a different provider. The orq_model resource manages ONLY
// openai-like models, so Read/Import verify this marker before touching a model
// (a mismatched id would otherwise be PATCHed / DELETEd destructively).
const ModelProviderOpenAILike = "openailike"

// Model is the transport-agnostic projection of a CUSTOM OpenAI-compatible
// ("openai-like") model, REST-backed via /v2/models/openai-like.
//
// The secret api_key is never round-tripped (only its env-var NAME appears, as
// configuration.api_key_env), so the resource keeps it config-authoritative.
// The cost + capability fields below ARE exposed in the list/read response
// (ModelDocument / metadata), so they are refreshed to surface out-of-band
// drift; they are pointers so the resource can tell "server omitted this for
// this model_type" (nil) from a real value. max_tokens / temperature /
// has_reasoning are encoded into the server's `parameters` array (not clean
// scalars) and stay config-authoritative. BaseURL lives under the server's
// `configuration` sub-object; Region lives under `metadata` — an openai-like
// model does NOT populate configuration.region (buildOpenAILikeMetadata stores
// it on metadata.region instead) — and both are surfaced flat here.
type Model struct {
	ID          string
	DisplayName string
	ModelID     string
	ModelType   string
	Region      string // from metadata.region (configuration.region fallback)
	BaseURL     string // from configuration.base_url
	Description string // "" when the server omits it
	Provider    string // configuration/provider discriminator (e.g. "openailike")
	Owner       string // "system" for system models; workspace id for custom ones
	Created     string // RFC 3339 (UTC)
	Updated     string // RFC 3339 (UTC)

	// Refreshed cost + capability fields (nil => server omitted it for this model).
	InputCost           *float64
	OutputCost          *float64
	CostPerImage        *float64
	SupportsVision      *bool
	SupportsToolCalling *bool
	SupportsStrictTool  *bool
	SupportsImageEdit   *bool
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
// There is no single-GET route (only the LIST endpoint, apps/platform-api/models/
// routes.go), so Get lists /v2/models and filters by id. A list-miss is NOT
// normalized to not_found: the list applies workspace/project scope + visibility
// filters, so an absent id may be invisible to this credential rather than deleted,
// and dropping it from state would make the next apply create a DUPLICATE. Get
// therefore returns a non-not_found error on a miss; only an authoritative 404
// (e.g. from Delete's own /:id route) is treated as "gone".
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
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name"`
	ModelID       string   `json:"model_id"`
	ModelType     string   `json:"model_type"`
	Description   *string  `json:"description"`
	Provider      string   `json:"provider"`
	Owner         string   `json:"owner"`
	InputCost     *float64 `json:"input_cost"`
	OutputCost    *float64 `json:"output_cost"`
	Configuration struct {
		BaseURL *string `json:"base_url"`
		Region  *string `json:"region"`
	} `json:"configuration"`
	Metadata struct {
		// region is the AUTHORITATIVE location for a custom openai-like model's
		// region (buildOpenAILikeMetadata writes it here; configuration.region is
		// left unset for this provider).
		Region              *string  `json:"region"`
		CostPerImage        *float64 `json:"cost_per_image"`
		SupportsVision      *bool    `json:"supports_vision"`
		SupportsToolCalling *bool    `json:"supports_tool_calling"`
		SupportsStrictTool  *bool    `json:"supports_strict_tool"`
		SupportsImageEdit   *bool    `json:"supports_image_edit"`
	} `json:"metadata"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

func (w *modelWire) toModel() Model {
	m := Model{
		ID:                  w.ID,
		DisplayName:         w.DisplayName,
		ModelID:             w.ModelID,
		ModelType:           w.ModelType,
		Provider:            w.Provider,
		Owner:               w.Owner,
		InputCost:           w.InputCost,
		OutputCost:          w.OutputCost,
		CostPerImage:        w.Metadata.CostPerImage,
		SupportsVision:      w.Metadata.SupportsVision,
		SupportsToolCalling: w.Metadata.SupportsToolCalling,
		SupportsStrictTool:  w.Metadata.SupportsStrictTool,
		SupportsImageEdit:   w.Metadata.SupportsImageEdit,
		Created:             w.Created.UTC().Format(time.RFC3339),
		Updated:             w.Updated.UTC().Format(time.RFC3339),
	}
	if w.Description != nil {
		m.Description = *w.Description
	}
	if w.Configuration.BaseURL != nil {
		m.BaseURL = *w.Configuration.BaseURL
	}
	// region: prefer metadata.region (where openai-like models store it); fall
	// back to configuration.region for robustness against other shapes.
	m.Region = firstNonEmpty(w.Metadata.Region, w.Configuration.Region)
	return m
}

// firstNonEmpty returns the first non-nil, non-empty pointed-to string, or "".
func firstNonEmpty(ps ...*string) string {
	for _, p := range ps {
		if p != nil && *p != "" {
			return *p
		}
	}
	return ""
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
		ID:                  d.Id,
		DisplayName:         d.DisplayName,
		ModelID:             d.ModelId,
		ModelType:           d.ModelType,
		Provider:            d.Provider,
		Owner:               d.Owner,
		InputCost:           d.InputCost,
		OutputCost:          d.OutputCost,
		CostPerImage:        d.Metadata.CostPerImage,
		SupportsVision:      d.Metadata.SupportsVision,
		SupportsToolCalling: d.Metadata.SupportsToolCalling,
		SupportsStrictTool:  d.Metadata.SupportsStrictTool,
		SupportsImageEdit:   d.Metadata.SupportsImageEdit,
		Created:             d.Created.UTC().Format(time.RFC3339),
		Updated:             d.Updated.UTC().Format(time.RFC3339),
	}
	if d.Description != nil {
		m.Description = *d.Description
	}
	if d.Configuration.BaseUrl != nil {
		m.BaseURL = *d.Configuration.BaseUrl
	}
	// region: prefer metadata.region (where openai-like models store it); fall
	// back to configuration.region for robustness against other shapes.
	m.Region = firstNonEmpty(d.Metadata.Region, d.Configuration.Region)
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
	// Not present in the list. There is NO GET-by-id route, and the list applies
	// workspace/project scope + visibility filters (ListModels → filterModelsByScope),
	// so an absent id may be genuinely deleted OR merely invisible to this credential.
	// We cannot tell the two apart, and treating an invisible model as deleted would
	// drop it from state and make the next apply create a DUPLICATE. Refuse to guess:
	// return a NON-not_found error so Read surfaces it instead of silently removing the
	// resource. A genuinely deleted model is removed with `terraform state rm` (or is
	// reported gone by Delete's authoritative 404).
	return nil, &Error{
		Code: CodeInternal,
		Message: "model " + id + " is not visible in the workspace model catalog; it may " +
			"have been deleted, or hidden from this credential by workspace/project scoping. " +
			"The list-only API cannot distinguish the two, so it will not be dropped from state " +
			"automatically — if it was deleted, remove it with `terraform state rm`; otherwise " +
			"ensure the credential can see it.",
	}
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
		// A genuine delete replies 200 with an EMPTY body; a REFUSED delete replies
		// 200 with a JSON `{"message": ...}` body and leaves the model intact. Treat
		// the refusal as a conflict so the resource keeps the still-live model in
		// state instead of silently orphaning it — see modelDeleteRefusalMessage.
		if msg := modelDeleteRefusalMessage(resp.Body); msg != "" {
			return &Error{
				Code: CodeConflict,
				Message: "the server refused to delete the model and left it in place: " + msg +
					" If it is still referenced by experiments, disable it (orq_workspace_model) or " +
					"remove the references, then retry.",
			}
		}
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}

// modelDeleteRefusalMessage returns the server's refusal message when a 2xx
// delete body signals a REFUSED (not performed) delete, or "" for a genuine
// delete. The server replies 200 with an empty body on success and 200 with a
// JSON `{"message": ...}` body when it declines and leaves the model intact —
// e.g. the model is still referenced by experiments ("The model is being used
// in N experiments and cannot be deleted. Try disabling it instead.") or is a
// system model ("This model is a system model and cannot be deleted.")
// (apps/platform-api/models/delete.go:45,53-54,99-100). Presence of a non-empty
// message is the structural signal — no string matching required; an empty body
// fails to unmarshal and reads as a genuine delete.
func modelDeleteRefusalMessage(body []byte) string {
	var refusal struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &refusal) != nil {
		return ""
	}
	return strings.TrimSpace(refusal.Message)
}
