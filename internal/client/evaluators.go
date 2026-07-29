package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// EvaluatorShape records WHICH of the two incompatible representations an
// *Evaluator was decoded from. The /v2/evaluators endpoints do NOT share a
// response body, and the difference is not cosmetic:
//
//	POST /v2/evaluators           -> EXTERNAL  (EvaluatorApiResponseSchema)
//	PATCH /v2/evaluators/{id}     -> EXTERNAL
//	GET  /v2/evaluators           -> EXTERNAL (list; not generated here)
//	GET  /v2/evaluators/{id}      -> INTERNAL  (EvaluatorSchema — the stored record)
//
// EXTERNAL carries: _id, key, description, created, updated, updated_by_id,
// guardrail_config, type, and the type-specific fields — with `model` as a
// provider-qualified STRING ("openai/gpt-4o") and jury judge models likewise as
// strings.
//
// INTERNAL carries: _id, display_name (the same value `key` holds), description,
// owner, domain_id, metadata, enabled, output_type, created, updated,
// created_by_id, updated_by_id, guardrail_config, type, and the type-specific
// fields — with `model` as an OBJECT {id, integration_id, model_parameters}
// whose `id` is a MODEL DOCUMENT ID, not a provider/model string.
//
// So EXTERNAL alone can never tell you `output_type`, `enabled` or the owning
// project, and INTERNAL alone can never tell you the `provider/model` string the
// operator wrote. Neither shape carries `path` at all — see EvaluatorCreateInput.
//
// Every field below documents which shape populates it; a field the decoded
// shape does not carry is left at its zero value, and callers MUST NOT treat
// that zero as a server value. The resource layer merges one of each.
type EvaluatorShape string

const (
	// ShapeExternal is the create/update (and list) response representation.
	ShapeExternal EvaluatorShape = "external"
	// ShapeInternal is the by-id GET representation: the stored record.
	ShapeInternal EvaluatorShape = "internal"
)

// Evaluator types this provider manages. The API has eight; the other six
// (function_eval, ragas, json_schema, http_eval, typescript_eval, bedrock_eval)
// are deliberately out of scope and are neither created nor adopted here.
const (
	EvaluatorTypePython = "python_eval"
	EvaluatorTypeLLM    = "llm_eval"
)

// Evaluator is the transport-agnostic projection of an evaluator. It is a UNION
// of what the two response shapes can express; Shape says which half is real.
type Evaluator struct {
	Shape EvaluatorShape

	// Both shapes.
	ID          string // `_id`
	Key         string // EXTERNAL `key` / INTERNAL `display_name` — the same stored value
	Type        string
	Description string
	Created     string
	Updated     string

	// INTERNAL only.
	OutputType string // "" from an external body — NOT "the server has no output_type"
	Enabled    bool
	ProjectID  string // `domain_id`
	ModelID    string // `model.id`: a MODEL DOCUMENT ID, never a provider/model string

	// EXTERNAL only.
	Model string // provider-qualified model ref, e.g. "openai/gpt-4o"

	// Type-specific, both shapes.
	Code              string // python_eval
	Prompt            string // llm_eval
	Mode              string // llm_eval: single | jury
	Repetitions       *int64 // llm_eval
	CategoricalLabels []CategoricalLabel
}

// CategoricalLabel is one allowed output value of a categorical evaluator.
type CategoricalLabel struct {
	Value       string
	Description *string
}

// JuryRetry is a per-judge retry policy. Nil fields are omitted from the write
// so the server default applies.
type JuryRetry struct {
	Count   *int64
	OnCodes []int64
}

// JuryJudge is one judge of an llm_eval jury. Model and Fallbacks are
// provider-qualified model refs (the EXTERNAL spelling — the server resolves
// them to document ids before storing).
type JuryJudge struct {
	Model     string
	Retry     *JuryRetry
	Fallbacks []string
}

// Jury is the mode="jury" configuration. It is WRITE-ONLY as far as this client
// is concerned: the by-id GET returns judge models as document-id objects, so
// decoding it back would require a catalog lookup per judge. The resource keeps
// the configured jury authoritative instead — see the resource documentation.
type Jury struct {
	Judges              []JuryJudge
	ReplacementJudges   []JuryJudge
	MinSuccessfulJudges *int64
}

// EvaluatorCreateInput carries the fields for POST /v2/evaluators.
//
// Path is REQUIRED by the API and is write-only in the strongest sense: no
// endpoint ever returns it. The server uses it (pathValidationMiddleware +
// validateFinderPath) to resolve the owning project/folder, stamps the result on
// the record as `domain_id`, and then DROPS the path itself
// (create-eval.handler.ts commonLogic destructures it away).
type EvaluatorCreateInput struct {
	Key  string
	Type string
	Path string

	Description *string
	OutputType  *string

	// python_eval
	Code *string

	// llm_eval
	Prompt            *string
	Mode              *string
	Model             *string
	Repetitions       *int64
	Jury              *Jury
	CategoricalLabels []CategoricalLabel
}

// EvaluatorUpdateInput is the PATCH body. The endpoint applies a mongo `$set`
// FIELD MERGE, not a replace: a key absent from the body keeps its stored value.
// That is why ClearCategoricalLabels exists — dropping the labels from config
// has to be spelled as an explicit JSON null, or the stored ones would survive
// and every subsequent refresh would re-report them.
type EvaluatorUpdateInput struct {
	ID   string
	Key  string
	Type string
	Path string

	Description *string
	OutputType  *string

	Code *string

	Prompt            *string
	Mode              *string
	Model             *string
	Repetitions       *int64
	Jury              *Jury
	CategoricalLabels []CategoricalLabel
	// ClearCategoricalLabels sends `"categorical_labels": null` so the $set merge
	// removes them. Ignored when CategoricalLabels is non-empty.
	ClearCategoricalLabels bool
}

// EvaluatorsAPI is the per-resource seam for the evaluators domain (REST-backed).
//
// Create and Update return the EXTERNAL shape, Get returns the INTERNAL one —
// the methods are 1:1 with the endpoints and do NOT paper over the asymmetry.
// Composing them (write, then read back by id) is the resource's job, because
// only the resource can decide what to persist when the read-back half fails.
type EvaluatorsAPI interface {
	Get(ctx context.Context, id string) (*Evaluator, error)
	Create(ctx context.Context, in EvaluatorCreateInput) (*Evaluator, error)
	Update(ctx context.Context, in EvaluatorUpdateInput) (*Evaluator, error)
	Delete(ctx context.Context, id string) error
}

type restEvaluators struct {
	c *restgen.ClientWithResponses
}

// evaluatorWire mirrors the fields this provider reads out of EITHER response
// shape. The two shapes are disjoint in places, so every shape-specific field is
// a pointer or is guarded by the Shape the decoder was called for:
//
//   - Key comes from `key` (external) or `display_name` (internal),
//   - Model is a string (external) or an object (internal) — hence RawModel,
//   - OutputType / Enabled / DomainID exist only internally.
//
// Decoding the raw body (rather than oapi-codegen's per-operation anonymous
// union structs) follows the guardrail-rules adapter and keeps this readable.
type evaluatorWire struct {
	ID          string  `json:"_id"`
	Key         string  `json:"key"`
	DisplayName string  `json:"display_name"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Created     string  `json:"created"`
	Updated     string  `json:"updated"`
	OutputType  string  `json:"output_type"`
	Enabled     *bool   `json:"enabled"`
	DomainID    string  `json:"domain_id"`
	Code        string  `json:"code"`
	Prompt      string  `json:"prompt"`
	Mode        string  `json:"mode"`
	Repetitions *int64  `json:"repetitions"`
	RawModel    rawJSON `json:"model"`

	CategoricalLabels []struct {
		Value       string  `json:"value"`
		Description *string `json:"description"`
	} `json:"categorical_labels"`
}

// rawJSON is json.RawMessage under another name so the `model` field can hold
// either a JSON string (external) or a JSON object (internal) without the
// decoder failing on the shape it did not expect.
type rawJSON = json.RawMessage

func (w *evaluatorWire) toEvaluator(shape EvaluatorShape) Evaluator {
	e := Evaluator{
		Shape:       shape,
		ID:          w.ID,
		Type:        w.Type,
		Description: w.Description,
		Created:     w.Created,
		Updated:     w.Updated,
		Code:        w.Code,
		Prompt:      w.Prompt,
		Mode:        w.Mode,
		Repetitions: w.Repetitions,
	}
	// `key` (external) and `display_name` (internal) are the SAME stored string;
	// prefer whichever the body actually carried.
	e.Key = w.Key
	if e.Key == "" {
		e.Key = w.DisplayName
	}
	for _, l := range w.CategoricalLabels {
		e.CategoricalLabels = append(e.CategoricalLabels, CategoricalLabel{Value: l.Value, Description: l.Description})
	}

	switch shape {
	case ShapeInternal:
		e.OutputType = w.OutputType
		e.ProjectID = w.DomainID
		// The stored record defaults `enabled` to true when absent
		// (normalizeEvalToInternalEvaluator), so a missing key is true, not false.
		e.Enabled = w.Enabled == nil || *w.Enabled
		var m struct {
			ID string `json:"id"`
		}
		if len(w.RawModel) > 0 && json.Unmarshal(w.RawModel, &m) == nil {
			e.ModelID = m.ID
		}
	case ShapeExternal:
		var s string
		if len(w.RawModel) > 0 && json.Unmarshal(w.RawModel, &s) == nil {
			e.Model = s
		}
	}
	return e
}

// decodeEvaluatorBody parses a single-evaluator JSON body in the given shape.
func decodeEvaluatorBody(body []byte, shape EvaluatorShape) (*Evaluator, error) {
	var w evaluatorWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decoding evaluator response: " + err.Error()}
	}
	e := w.toEvaluator(shape)
	return &e, nil
}

// --- request bodies ---------------------------------------------------------
//
// The generated CreateEval body is a nested anonymous union
// (CreateEvalJSONBody{union json.RawMessage} wrapping two more union levels),
// which no caller can build readably, and the generated UpdateEval body is a
// flat struct with deeply anonymous nested jury structs. Both operations are
// therefore issued through the generated *WithBody* entry points with an
// explicit payload marshalled from the structs below — the generated request
// builder, base URL and bearer transport are still used, only the body type is
// bypassed.

type juryJudgePayload struct {
	Model     string             `json:"model"`
	Retry     *juryRetryPayload  `json:"retry,omitempty"`
	Fallbacks []juryModelPayload `json:"fallbacks,omitempty"`
}

type juryRetryPayload struct {
	Count   *int64  `json:"count,omitempty"`
	OnCodes []int64 `json:"on_codes,omitempty"`
}

type juryModelPayload struct {
	Model string `json:"model"`
}

type juryPayload struct {
	Judges              []juryJudgePayload `json:"judges"`
	ReplacementJudges   []juryJudgePayload `json:"replacement_judges,omitempty"`
	MinSuccessfulJudges *int64             `json:"min_successful_judges,omitempty"`
}

type categoricalLabelPayload struct {
	Value       string  `json:"value"`
	Description *string `json:"description,omitempty"`
}

func juryToPayload(j *Jury) *juryPayload {
	if j == nil {
		return nil
	}
	toJudges := func(in []JuryJudge) []juryJudgePayload {
		if len(in) == 0 {
			return nil
		}
		out := make([]juryJudgePayload, 0, len(in))
		for _, judge := range in {
			p := juryJudgePayload{Model: judge.Model}
			if judge.Retry != nil {
				p.Retry = &juryRetryPayload{Count: judge.Retry.Count, OnCodes: judge.Retry.OnCodes}
			}
			for _, f := range judge.Fallbacks {
				p.Fallbacks = append(p.Fallbacks, juryModelPayload{Model: f})
			}
			out = append(out, p)
		}
		return out
	}
	return &juryPayload{
		Judges:              toJudges(j.Judges),
		ReplacementJudges:   toJudges(j.ReplacementJudges),
		MinSuccessfulJudges: j.MinSuccessfulJudges,
	}
}

func labelsToPayload(in []CategoricalLabel) []categoricalLabelPayload {
	if len(in) == 0 {
		return nil
	}
	out := make([]categoricalLabelPayload, 0, len(in))
	for _, l := range in {
		out = append(out, categoricalLabelPayload{Value: l.Value, Description: l.Description})
	}
	return out
}

// createPayload is the POST body. Fields the input leaves nil are omitted, so a
// python_eval never sends llm keys and vice versa.
type createPayload struct {
	Key         string  `json:"key"`
	Type        string  `json:"type"`
	Path        string  `json:"path"`
	Description *string `json:"description,omitempty"`
	OutputType  *string `json:"output_type,omitempty"`

	Code *string `json:"code,omitempty"`

	Prompt            *string                   `json:"prompt,omitempty"`
	Mode              *string                   `json:"mode,omitempty"`
	Model             *string                   `json:"model,omitempty"`
	Repetitions       *int64                    `json:"repetitions,omitempty"`
	Jury              *juryPayload              `json:"jury,omitempty"`
	CategoricalLabels []categoricalLabelPayload `json:"categorical_labels,omitempty"`
}

// updatePayload is the PATCH body. CategoricalLabels is a *[]… so the resource
// can distinguish "leave alone" (nil) from "clear" (a non-nil pointer to nil,
// marshalled as `null`) — the $set merge has no other way to remove them.
type updatePayload struct {
	Key         string  `json:"key"`
	Type        string  `json:"type"`
	Path        string  `json:"path,omitempty"`
	Description *string `json:"description,omitempty"`
	OutputType  *string `json:"output_type,omitempty"`

	Code *string `json:"code,omitempty"`

	Prompt            *string                    `json:"prompt,omitempty"`
	Mode              *string                    `json:"mode,omitempty"`
	Model             *string                    `json:"model,omitempty"`
	Repetitions       *int64                     `json:"repetitions,omitempty"`
	Jury              *juryPayload               `json:"jury,omitempty"`
	CategoricalLabels *[]categoricalLabelPayload `json:"categorical_labels,omitempty"`
}

// --- adapter -----------------------------------------------------------------

const evaluatorDomain = "evaluator"
const jsonContentType = "application/json"

func (r *restEvaluators) Get(ctx context.Context, id string) (*Evaluator, error) {
	resp, err := r.c.GetEvalWithResponse(ctx, id)
	if err != nil {
		return nil, mapRESTTransportError(evaluatorDomain, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	// GET /v2/evaluators/{id} serializes the STORED record — see EvaluatorShape.
	return decodeEvaluatorBody(resp.Body, ShapeInternal)
}

func (r *restEvaluators) Create(ctx context.Context, in EvaluatorCreateInput) (*Evaluator, error) {
	payload := createPayload{
		Key:               in.Key,
		Type:              in.Type,
		Path:              in.Path,
		Description:       in.Description,
		OutputType:        in.OutputType,
		Code:              in.Code,
		Prompt:            in.Prompt,
		Mode:              in.Mode,
		Model:             in.Model,
		Repetitions:       in.Repetitions,
		Jury:              juryToPayload(in.Jury),
		CategoricalLabels: labelsToPayload(in.CategoricalLabels),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "encoding evaluator create request: " + err.Error()}
	}
	resp, err := r.c.CreateEvalWithBodyWithResponse(ctx, jsonContentType, bytes.NewReader(body))
	if err != nil {
		return nil, mapRESTTransportError(evaluatorDomain, err)
	}
	if resp.StatusCode() != http.StatusOK && resp.StatusCode() != http.StatusCreated {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	// POST answers with the EXTERNAL representation, NOT the stored record.
	return decodeEvaluatorBody(resp.Body, ShapeExternal)
}

func (r *restEvaluators) Update(ctx context.Context, in EvaluatorUpdateInput) (*Evaluator, error) {
	payload := updatePayload{
		Key:         in.Key,
		Type:        in.Type,
		Path:        in.Path,
		Description: in.Description,
		OutputType:  in.OutputType,
		Code:        in.Code,
		Prompt:      in.Prompt,
		Mode:        in.Mode,
		Model:       in.Model,
		Repetitions: in.Repetitions,
		Jury:        juryToPayload(in.Jury),
	}
	switch {
	case len(in.CategoricalLabels) > 0:
		labels := labelsToPayload(in.CategoricalLabels)
		payload.CategoricalLabels = &labels
	case in.ClearCategoricalLabels:
		// Explicit JSON null: the update is a `$set` merge, so omitting the key
		// would leave previously stored labels in place forever.
		var null []categoricalLabelPayload
		payload.CategoricalLabels = &null
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "encoding evaluator update request: " + err.Error()}
	}
	resp, err := r.c.UpdateEvalWithBodyWithResponse(ctx, in.ID, jsonContentType, bytes.NewReader(body))
	if err != nil {
		return nil, mapRESTTransportError(evaluatorDomain, err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	// PATCH answers with the EXTERNAL representation, like POST.
	return decodeEvaluatorBody(resp.Body, ShapeExternal)
}

func (r *restEvaluators) Delete(ctx context.Context, id string) error {
	resp, err := r.c.DeleteEvalWithResponse(ctx, id)
	if err != nil {
		return mapRESTTransportError(evaluatorDomain, err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}
