package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newEvaluatorServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Token: sentinelToken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// externalPythonJSON is what POST/PATCH answer with. Note what is ABSENT:
// output_type, enabled, display_name.
func externalPythonJSON() map[string]any {
	return map[string]any{
		"_id":         "01JMDPA3QW5C1V0NJ1PW34T4E5",
		"key":         "my-eval",
		"project_id":  "proj_1",
		"description": "checks the answer",
		"created":     "2026-07-01T10:00:00.000Z",
		"updated":     "2026-07-02T10:00:00.000Z",
		"type":        "python_eval",
		"code":        "def evaluate(**kwargs):\n    return True\n",
	}
}

// internalPythonJSON is the stored record of the SAME evaluator: `display_name`
// instead of `key`, plus owner/domain_id/metadata/enabled/output_type.
func internalPythonJSON() map[string]any {
	return map[string]any{
		"_id":          "01JMDPA3QW5C1V0NJ1PW34T4E5",
		"display_name": "my-eval",
		"description":  "checks the answer",
		"owner":        "ws_1",
		"domain_id":    "proj_1",
		"project_id":   "proj_1",
		"metadata":     map[string]any{"supported_on_output_type": true},
		"enabled":      true,
		"output_type":  "boolean",
		"created":      "2026-07-01T10:00:00.000Z",
		"updated":      "2026-07-02T10:00:00.000Z",
		"type":         "python_eval",
		"code":         "def evaluate(**kwargs):\n    return True\n",
	}
}

// internalLLMJSON's `model` is an OBJECT holding a model DOCUMENT ID.
func internalLLMJSON() map[string]any {
	return map[string]any{
		"_id":          "01JMDPA3QW5C1V0NJ1PW34T4E5",
		"display_name": "tone",
		"description":  "",
		"owner":        "ws_1",
		"domain_id":    "proj_1",
		"project_id":   "proj_1",
		"metadata":     map[string]any{},
		"enabled":      true,
		"output_type":  "categorical",
		"created":      "2026-07-01T10:00:00.000Z",
		"updated":      "2026-07-02T10:00:00.000Z",
		"type":         "llm_eval",
		"mode":         "single",
		"repetitions":  2,
		"prompt":       "Rate the tone",
		"model": map[string]any{
			"id":                 "01HZZMODELDOCID0000000000",
			"integration_id":     nil,
			"model_parameters":   map[string]any{"temperature": 0.1},
			"unexpected_extra":   "ignored",
			"another_extra_here": 1,
		},
		"categorical_labels": []map[string]any{
			{"value": "friendly", "description": "warm"},
			{"value": "curt"},
		},
	}
}

func TestEvaluators_GetDecodesStoredRecord(t *testing.T) {
	var gotPath, gotMethod string
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(internalLLMJSON())
	})

	e, err := c.Evaluators().Get(context.Background(), "01JMDPA3QW5C1V0NJ1PW34T4E5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v2/evaluators/01JMDPA3QW5C1V0NJ1PW34T4E5" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if e.Shape != ShapeInternal {
		t.Errorf("shape = %q, want internal", e.Shape)
	}
	if e.Key != "tone" {
		t.Errorf("display_name must land in Key, got %q", e.Key)
	}
	if e.ModelID != "01HZZMODELDOCID0000000000" {
		t.Errorf("model document id not decoded: %q", e.ModelID)
	}
	if e.Model != "" {
		t.Errorf("Model must stay empty on an internal body (got %q) — a document id is not a provider/model ref", e.Model)
	}
	if e.OutputType != "categorical" || !e.Enabled || e.ProjectID != "proj_1" {
		t.Errorf("internal-only fields wrong: %+v", e)
	}
	if e.Repetitions == nil || *e.Repetitions != 2 {
		t.Errorf("repetitions not decoded: %+v", e.Repetitions)
	}
	if len(e.CategoricalLabels) != 2 || e.CategoricalLabels[0].Value != "friendly" ||
		e.CategoricalLabels[0].Description == nil || *e.CategoricalLabels[0].Description != "warm" {
		t.Errorf("categorical labels wrong: %+v", e.CategoricalLabels)
	}
	if e.CategoricalLabels[1].Description != nil {
		t.Errorf("an absent label description must stay nil, got %q", *e.CategoricalLabels[1].Description)
	}
}

func TestEvaluators_GetDefaultsEnabledToTrue(t *testing.T) {
	body := internalPythonJSON()
	delete(body, "enabled")
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	e, err := c.Evaluators().Get(context.Background(), "01JMDPA3QW5C1V0NJ1PW34T4E5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !e.Enabled {
		t.Error("a missing `enabled` key must decode as true")
	}
}

func TestEvaluators_CreateDecodesExternalShape(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(externalPythonJSON())
	})

	code := "def evaluate(**kwargs):\n    return True\n"
	outputType := "boolean"
	e, err := c.Evaluators().Create(context.Background(), EvaluatorCreateInput{
		Key:        "my-eval",
		Type:       EvaluatorTypePython,
		ProjectID:  "proj_1",
		Code:       &code,
		OutputType: &outputType,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/evaluators" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	if gotBody["key"] != "my-eval" || gotBody["project_id"] != "proj_1" || gotBody["code"] != code {
		t.Errorf("create body wrong: %v", gotBody)
	}
	if _, ok := gotBody["path"]; ok {
		t.Error("`path` is gone; the project is addressed by project_id only")
	}
	if _, ok := gotBody["prompt"]; ok {
		t.Error("a python_eval create must not carry llm keys")
	}
	if e.Shape != ShapeExternal {
		t.Errorf("shape = %q, want external", e.Shape)
	}
	if e.Key != "my-eval" || e.ID != "01JMDPA3QW5C1V0NJ1PW34T4E5" {
		t.Errorf("external decode wrong: %+v", e)
	}
	if e.OutputType != "" || e.ModelID != "" {
		t.Errorf("internal-only fields must stay zero on an external body: %+v", e)
	}
	if e.ProjectID != "proj_1" {
		t.Errorf("project_id must decode from the external body too, got %q", e.ProjectID)
	}
}

// Judge fallbacks are written as the API's `[{model: …}]` objects even though the
// resource models them as a plain list of refs.
func TestEvaluators_CreateLLMJuryPayload(t *testing.T) {
	var gotBody map[string]any
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(externalPythonJSON())
	})

	prompt := "judge it"
	mode := "jury"
	count := int64(3)
	minSuccessful := int64(2)
	if _, err := c.Evaluators().Create(context.Background(), EvaluatorCreateInput{
		Key:       "jury-eval",
		Type:      EvaluatorTypeLLM,
		ProjectID: "proj_1",
		Prompt:    &prompt,
		Mode:      &mode,
		Jury: &Jury{
			Judges: []JuryJudge{
				{Model: "openai/gpt-4o", Retry: &JuryRetry{Count: &count, OnCodes: []int64{429, 503}}, Fallbacks: []string{"anthropic/claude-sonnet-4-5"}},
				{Model: "google/gemini-2.5-pro"},
			},
			MinSuccessfulJudges: &minSuccessful,
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	jury, ok := gotBody["jury"].(map[string]any)
	if !ok {
		t.Fatalf("no jury in body: %v", gotBody)
	}
	judges, ok := jury["judges"].([]any)
	if !ok || len(judges) != 2 {
		t.Fatalf("judges wrong: %v", jury["judges"])
	}
	first := judges[0].(map[string]any)
	fallbacks, ok := first["fallbacks"].([]any)
	if !ok || len(fallbacks) != 1 {
		t.Fatalf("fallbacks not written as objects: %v", first["fallbacks"])
	}
	if fallbacks[0].(map[string]any)["model"] != "anthropic/claude-sonnet-4-5" {
		t.Errorf("fallback model wrong: %v", fallbacks[0])
	}
	retry := first["retry"].(map[string]any)
	if retry["count"].(float64) != 3 || len(retry["on_codes"].([]any)) != 2 {
		t.Errorf("retry wrong: %v", retry)
	}
	if jury["min_successful_judges"].(float64) != 2 {
		t.Errorf("min_successful_judges wrong: %v", jury["min_successful_judges"])
	}
	if _, ok := judges[1].(map[string]any)["retry"]; ok {
		t.Error("a judge without retry must omit the key entirely")
	}
	if _, ok := gotBody["model"]; ok {
		t.Error("a jury create must not also send `model`")
	}
}

// Omitting the key would leave the stored labels in place forever, because the
// update is a $set merge.
func TestEvaluators_UpdateClearsLabelsWithExplicitNull(t *testing.T) {
	var raw map[string]json.RawMessage
	var gotPath, gotMethod string
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(externalPythonJSON())
	})

	if _, err := c.Evaluators().Update(context.Background(), EvaluatorUpdateInput{
		ID:                     "01JMDPA3QW5C1V0NJ1PW34T4E5",
		Key:                    "my-eval",
		Type:                   EvaluatorTypePython,
		ProjectID:              "proj_1",
		ClearCategoricalLabels: true,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/v2/evaluators/01JMDPA3QW5C1V0NJ1PW34T4E5" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
	labels, ok := raw["categorical_labels"]
	if !ok {
		t.Fatal("categorical_labels must be PRESENT as null so the $set merge clears it")
	}
	if string(labels) != "null" {
		t.Errorf("categorical_labels = %s, want null", labels)
	}
}

func TestEvaluators_UpdateSendsLabels(t *testing.T) {
	var gotBody map[string]any
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(externalPythonJSON())
	})
	desc := "warm"
	if _, err := c.Evaluators().Update(context.Background(), EvaluatorUpdateInput{
		ID:                "01JMDPA3QW5C1V0NJ1PW34T4E5",
		Key:               "tone",
		Type:              EvaluatorTypeLLM,
		ProjectID:         "proj_1",
		CategoricalLabels: []CategoricalLabel{{Value: "friendly", Description: &desc}, {Value: "curt"}},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	labels, ok := gotBody["categorical_labels"].([]any)
	if !ok || len(labels) != 2 {
		t.Fatalf("labels not sent: %v", gotBody["categorical_labels"])
	}
	if _, ok := labels[1].(map[string]any)["description"]; ok {
		t.Error("a label with no description must omit the key")
	}
}

func TestEvaluators_GetNotFound(t *testing.T) {
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Evaluator not found"}`))
	})
	_, err := c.Evaluators().Get(context.Background(), "01JMDPA3QW5C1V0NJ1PW34T4E5")
	if err == nil {
		t.Fatal("expected an error")
	}
	if CodeOf(err) != CodeNotFound {
		t.Errorf("code = %q, want not_found", CodeOf(err))
	}
}

func TestEvaluators_DeleteAcceptsNoContent(t *testing.T) {
	var gotMethod, gotPath string
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Evaluators().Delete(context.Background(), "01JMDPA3QW5C1V0NJ1PW34T4E5"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v2/evaluators/01JMDPA3QW5C1V0NJ1PW34T4E5" {
		t.Errorf("wrong request: %s %s", gotMethod, gotPath)
	}
}

func TestEvaluators_DecodeExternalModelString(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"_id":         "01JMDPA3QW5C1V0NJ1PW34T4E5",
		"key":         "tone",
		"description": "",
		"type":        "llm_eval",
		"mode":        "single",
		"model":       "openai/gpt-4o",
		"prompt":      "Rate the tone",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e, err := decodeEvaluatorBody(body, ShapeExternal)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Model != "openai/gpt-4o" {
		t.Errorf("Model = %q, want openai/gpt-4o", e.Model)
	}
	if e.ModelID != "" {
		t.Errorf("ModelID must stay empty on an external body, got %q", e.ModelID)
	}
}

// The update endpoint moves an evaluator between projects, so project_id has to
// reach the wire on a PATCH as well.
func TestEvaluators_UpdateSendsProjectID(t *testing.T) {
	var gotBody map[string]any
	c := newEvaluatorServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(externalPythonJSON())
	})
	if _, err := c.Evaluators().Update(context.Background(), EvaluatorUpdateInput{
		ID:        "01JMDPA3QW5C1V0NJ1PW34T4E5",
		Key:       "my-eval",
		Type:      EvaluatorTypePython,
		ProjectID: "proj_2",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotBody["project_id"] != "proj_2" {
		t.Errorf("project_id not sent on update: %v", gotBody)
	}
	if _, ok := gotBody["path"]; ok {
		t.Error("`path` is gone; the project is addressed by project_id only")
	}
}

// The stored record spells the project `domain_id`; `project_id` is the newer
// alias for the same value and either alone must decode.
func TestEvaluators_DecodeProjectIDFallsBackToDomainID(t *testing.T) {
	body := internalPythonJSON()
	delete(body, "project_id")
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e, err := decodeEvaluatorBody(raw, ShapeInternal)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.ProjectID != "proj_1" {
		t.Errorf("ProjectID = %q, want the domain_id fallback proj_1", e.ProjectID)
	}
}
