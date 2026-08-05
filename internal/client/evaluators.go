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
// response body:
//
//	POST /v2/evaluators       -> EXTERNAL: `model` is a "provider/model" STRING
//	PATCH /v2/evaluators/{id} -> EXTERNAL
//	GET  /v2/evaluators/{id}  -> INTERNAL: the stored record; `model` is an
//	                             OBJECT holding a model DOCUMENT ID, and it is
//	                             the only shape carrying output_type, enabled
//	                             and domain_id
//
// A field the decoded shape does not carry is left at its ZERO value, which
// callers must not read as a server value.
type EvaluatorShape string

const (
	ShapeExternal EvaluatorShape = "external"
	ShapeInternal EvaluatorShape = "internal"
)

// The API serves eight evaluator types; the other six (function_eval, ragas,
// json_schema, http_eval, typescript_eval, bedrock_eval) are out of scope.
const (
	EvaluatorTypePython = "python_eval"
	EvaluatorTypeLLM    = "llm_eval"
)

// Evaluator is the UNION of what the two response shapes can express; Shape says
// which half is real.
type Evaluator struct {
	Shape EvaluatorShape

	// Both shapes.
	ID          string // `_id`
	Key         string // EXTERNAL `key` / INTERNAL `display_name` — the same stored value
	Type        string
	Description string
	Created     string
	Updated     string
	ProjectID   string // `project_id`; the stored record spells the same value `domain_id`

	// INTERNAL only.
	OutputType string // "" from an external body — NOT "the server has no output_type"
	Enabled    bool
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

// JuryRetry is a per-judge retry policy. Nil fields fall back to the server default.
type JuryRetry struct {
	Count   *int64
	OnCodes []int64
}

// JuryJudge is one judge of an llm_eval jury. Model and Fallbacks are
// provider-qualified refs; the server resolves them to document ids before storing.
type JuryJudge struct {
	Model     string
	Retry     *JuryRetry
	Fallbacks []string
}

// Jury is WRITE-ONLY here: the by-id GET returns judge models as document-id
// objects, so decoding it back would cost a catalog lookup per judge.
type Jury struct {
	Judges              []JuryJudge
	ReplacementJudges   []JuryJudge
	MinSuccessfulJudges *int64
}

// EvaluatorCreateInput carries the fields for POST /v2/evaluators.
type EvaluatorCreateInput struct {
	Key       string
	Type      string
	ProjectID string

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
type EvaluatorUpdateInput struct {
	ID        string
	Key       string
	Type      string
	ProjectID string

	Description *string
	OutputType  *string

	Code *string

	Prompt            *string
	Mode              *string
	Model             *string
	Repetitions       *int64
	Jury              *Jury
	CategoricalLabels []CategoricalLabel
	// ClearCategoricalLabels sends `"categorical_labels": null`, the only way the
	// $set merge can remove them. Ignored when CategoricalLabels is non-empty.
	ClearCategoricalLabels bool
}

// EvaluatorsAPI is 1:1 with the endpoints and does NOT paper over the shape
// asymmetry: composing a write with a read-back is the resource's job, because
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

// evaluatorWire mirrors the fields read out of EITHER response shape, so every
// shape-specific field is a pointer or is guarded by the Shape the decoder was
// called for. RawModel stays raw because `model` is a string in one shape and an
// object in the other.
type evaluatorWire struct {
	ID          string          `json:"_id"`
	Key         string          `json:"key"`
	DisplayName string          `json:"display_name"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Created     string          `json:"created"`
	Updated     string          `json:"updated"`
	OutputType  string          `json:"output_type"`
	Enabled     *bool           `json:"enabled"`
	ProjectID   string          `json:"project_id"`
	DomainID    string          `json:"domain_id"`
	Code        string          `json:"code"`
	Prompt      string          `json:"prompt"`
	Mode        string          `json:"mode"`
	Repetitions *int64          `json:"repetitions"`
	RawModel    json.RawMessage `json:"model"`

	CategoricalLabels []struct {
		Value       string  `json:"value"`
		Description *string `json:"description"`
	} `json:"categorical_labels"`
}

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
	// `key` (external) and `display_name` (internal) are the SAME stored string.
	e.Key = w.Key
	if e.Key == "" {
		e.Key = w.DisplayName
	}
	// Likewise `project_id` and the stored record's `domain_id`.
	e.ProjectID = w.ProjectID
	if e.ProjectID == "" {
		e.ProjectID = w.DomainID
	}
	for _, l := range w.CategoricalLabels {
		e.CategoricalLabels = append(e.CategoricalLabels, CategoricalLabel{Value: l.Value, Description: l.Description})
	}

	switch shape {
	case ShapeInternal:
		e.OutputType = w.OutputType
		// The stored record defaults `enabled` to true when absent.
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
// The generated request bodies are nested anonymous unions no caller can build
// readably, so both writes go through the generated *WithBody* entry points with
// a payload marshalled from the structs below. Only the body type is bypassed;
// the generated request builder, base URL and bearer transport still apply.

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

// createPayload omits every nil field, so a python_eval never sends llm keys.
type createPayload struct {
	Key         string  `json:"key"`
	Type        string  `json:"type"`
	ProjectID   string  `json:"project_id"`
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

// updatePayload's CategoricalLabels is a *[]… so "leave alone" (nil) is distinct
// from "clear" (a non-nil pointer to nil, marshalled as `null`).
type updatePayload struct {
	Key         string  `json:"key"`
	Type        string  `json:"type"`
	ProjectID   string  `json:"project_id,omitempty"`
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
	return decodeEvaluatorBody(resp.Body, ShapeInternal)
}

func (r *restEvaluators) Create(ctx context.Context, in EvaluatorCreateInput) (*Evaluator, error) {
	payload := createPayload{
		Key:               in.Key,
		Type:              in.Type,
		ProjectID:         in.ProjectID,
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
	return decodeEvaluatorBody(resp.Body, ShapeExternal)
}

func (r *restEvaluators) Update(ctx context.Context, in EvaluatorUpdateInput) (*Evaluator, error) {
	payload := updatePayload{
		Key:         in.Key,
		Type:        in.Type,
		ProjectID:   in.ProjectID,
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
