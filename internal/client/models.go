package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// ModelProviderOpenAILike is the `provider` discriminator the server stamps on a
// CUSTOM OpenAI-compatible model. orq_model manages ONLY those, so Read/Import
// verify this marker before PATCHing or DELETEing anything.
const ModelProviderOpenAILike = "openailike"

// Model is the projection of a custom openai-like model.
//
// The secret api_key is never round-tripped, so the resource keeps it
// config-authoritative; so are max_tokens / temperature / has_reasoning, which
// the server encodes into a `parameters` array rather than clean scalars. The
// cost and capability fields ARE echoed, so they are refreshed to surface drift.
type Model struct {
	ID          string
	RefID       string // human-readable ref: provider/model_id (workspaceKey@provider/model_id for private models)
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

	// Nil distinguishes "the server omitted this for this model_type" from a value.
	InputCost           *float64
	OutputCost          *float64
	CostPerImage        *float64
	SupportsVision      *bool
	SupportsToolCalling *bool
	SupportsStrictTool  *bool
	SupportsImageEdit   *bool
}

// ModelCreateInput's six leading fields are required by the API; the rest are
// omitted from the write when nil.
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

// ModelUpdateInput has no APIKey: the update endpoint does not accept one, so
// the secret is immutable in place and the resource marks api_key
// RequiresReplace.
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

// ModelsAPI has no single-GET route, so Get and Resolve both list /v2/models and
// filter. Only Delete's own /:id route is authoritative for not_found.
type ModelsAPI interface {
	Get(ctx context.Context, id string) (*Model, error)
	// Resolve maps a ref_id (provider/model_id, or workspaceKey@provider/model_id
	// for a private model) or a document id to its catalog document.
	Resolve(ctx context.Context, ref string) (*Model, error)
	Create(ctx context.Context, in ModelCreateInput) (*Model, error)
	Update(ctx context.Context, in ModelUpdateInput) (*Model, error)
	Delete(ctx context.Context, id string) error
}

type restModels struct {
	c *restgen.ClientWithResponses
}

// modelWire mirrors the ModelDocument body every model read/write returns. Only
// the reliably-echoed fields are pulled out.
type modelWire struct {
	ID            string   `json:"id"`
	RefID         string   `json:"refId"`
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
		// metadata.region is where an openai-like model's region actually lives;
		// configuration.region is left unset for this provider.
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
		RefID:               w.RefID,
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

func decodeModelBody(body []byte) (*Model, error) {
	var w modelWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding model response: " + err.Error()}
	}
	m := w.toModel()
	return &m, nil
}

func modelFromDocument(d *restgen.ModelDocument) Model {
	m := Model{
		ID:                  d.Id,
		RefID:               d.RefId,
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
	m.Region = firstNonEmpty(d.Metadata.Region, d.Configuration.Region)
	return m
}

// listModelDocuments fetches the whole model catalog. There is no GET-by-id
// route, so every read goes through here. A 404/410 from the COLLECTION endpoint
// cannot say a single id is gone, so it is demoted rather than passed on as
// not_found — which would make Read drop the resource and the next apply create a
// DUPLICATE.
func listModelDocuments(ctx context.Context, c *restgen.ClientWithResponses) ([]restgen.ModelDocument, error) {
	resp, err := c.ModelListWithResponse(ctx)
	if err != nil {
		return nil, mapRESTTransportError("model", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		mapped := mapRESTStatus(resp.StatusCode(), resp.Body)
		if CodeOf(mapped) == CodeNotFound {
			return nil, &Error{
				Code: CodeUnavailable,
				Message: "the model catalog list endpoint returned not-found; there is no lookup-by-id " +
					"route, so this is a routing or deployment anomaly rather than a deleted model. " +
					"State is left intact — verify the API base URL and that the model list route is " +
					"reachable.",
				err: mapped,
			}
		}
		return nil, mapped
	}
	return *resp.JSON200, nil
}

// modelNotVisibleError covers a list-miss. The list applies workspace/project
// scope and visibility filters, so an absent id may be deleted OR merely
// invisible to this credential. Refuse to guess: a NON-not_found error keeps it
// in state.
func modelNotVisibleError(id string) error {
	return &Error{
		Code: CodeInternal,
		Message: "model " + id + " is not visible in the workspace model catalog; it may " +
			"have been deleted, or hidden from this credential by workspace/project scoping. " +
			"The list-only API cannot distinguish the two, so it will not be dropped from state " +
			"automatically — if it was deleted, remove it with `terraform state rm`; otherwise " +
			"ensure the credential can see it.",
	}
}

func (r *restModels) Get(ctx context.Context, id string) (*Model, error) {
	docs, err := listModelDocuments(ctx, r.c)
	if err != nil {
		return nil, err
	}
	for i := range docs {
		if docs[i].Id != id {
			continue
		}
		m := modelFromDocument(&docs[i])
		return &m, nil
	}
	return nil, modelNotVisibleError(id)
}

// Resolve maps a model reference to its catalog document: an exact `id` match
// wins outright, otherwise the unique exact `ref_id` match does. The id-vs-ref
// decision is made purely by equality, never by a "looks like a ref"
// contains-slash heuristic, because some backends use slug-shaped document ids.
func (r *restModels) Resolve(ctx context.Context, ref string) (*Model, error) {
	docs, err := listModelDocuments(ctx, r.c)
	if err != nil {
		return nil, err
	}

	// 1. Exact id match wins outright.
	for i := range docs {
		if docs[i].Id == ref {
			m := modelFromDocument(&docs[i])
			return &m, nil
		}
	}

	// 2. Otherwise collect exact ref_id matches.
	var matchIDs []string
	var match *restgen.ModelDocument
	for i := range docs {
		if docs[i].RefId == ref {
			matchIDs = append(matchIDs, docs[i].Id)
			match = &docs[i]
		}
	}
	switch len(matchIDs) {
	case 1:
		m := modelFromDocument(match)
		return &m, nil
	case 0:
		return nil, &Error{
			Code: CodeNotFound,
			Message: "no model in the workspace catalog matches the reference " + ref + ": it was tried " +
				"both as a model document id and as a model ref_id, and neither matched. Look the model up " +
				"in GET /v2/models and use its `ref_id` (for example openai/gpt-4o, or " +
				"workspaceKey@openailike/my-model for a workspace-custom model) or its document id.",
		}
	default:
		return nil, &Error{
			Code: CodeInvalid,
			Message: "the model reference " + ref + " is ambiguous: it matches more than one model " +
				"document (" + strings.Join(matchIDs, ", ") + "). Use the model document id (the `id` field " +
				"in GET /v2/models) instead of the ref to select exactly one.",
		}
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
	return deleteModelByID(ctx, r.c, id)
}

// deleteModelByID drives the shared DELETE /v2/models/:id route, used by every
// custom-model flavour.
func deleteModelByID(ctx context.Context, c *restgen.ClientWithResponses, id string) error {
	resp, err := c.ModelDeleteWithResponse(ctx, id)
	if err != nil {
		return mapRESTTransportError("model", err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
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

// modelDeleteRefusalMessage detects a REFUSED delete hidden behind a 2xx: the
// server replies 200 with an EMPTY body on success and 200 with a JSON
// `{"message": ...}` body when it declines and leaves the model intact. The
// presence of a message is the structural signal; no string matching needed.
func modelDeleteRefusalMessage(body []byte) string {
	var refusal struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &refusal) != nil {
		return ""
	}
	return strings.TrimSpace(refusal.Message)
}
