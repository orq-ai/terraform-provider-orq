package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// --- helpers ----------------------------------------------------------------

func evaluatorSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	(&evaluatorResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

func labelObject(value string) types.Object {
	o, diags := types.ObjectValue(
		map[string]attr.Type{"value": types.StringType, "description": types.StringType},
		map[string]attr.Value{"value": types.StringValue(value), "description": types.StringNull()},
	)
	if diags.HasError() {
		panic(diags)
	}
	return o
}

func labelList(values ...string) types.List {
	elemType := types.ObjectType{AttrTypes: map[string]attr.Type{
		"value": types.StringType, "description": types.StringType,
	}}
	if values == nil {
		return types.ListNull(elemType)
	}
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, labelObject(v))
	}
	l, diags := types.ListValue(elemType, elems)
	if diags.HasError() {
		panic(diags)
	}
	return l
}

var juryAttrTypes = map[string]attr.Type{
	"judges":                types.ListType{ElemType: types.StringType},
	"replacement_judges":    types.ListType{ElemType: types.StringType},
	"min_successful_judges": types.Int64Type,
}

// juryObject carries only what validateJurySizing inspects: the two judge-list
// lengths and the threshold. A negative replacements count means a null list.
func juryObject(judges, replacements int, minSuccessful *int64) types.Object {
	strList := func(n int) attr.Value {
		elems := make([]attr.Value, 0, n)
		for i := 0; i < n; i++ {
			elems = append(elems, types.StringValue("m"))
		}
		l, _ := types.ListValue(types.StringType, elems)
		return l
	}
	minValue := types.Int64Null()
	if minSuccessful != nil {
		minValue = types.Int64Value(*minSuccessful)
	}
	replacementList := strList(replacements)
	if replacements < 0 {
		replacementList = types.ListNull(types.StringType)
	}
	o, diags := types.ObjectValue(juryAttrTypes, map[string]attr.Value{
		"judges":                strList(judges),
		"replacement_judges":    replacementList,
		"min_successful_judges": minValue,
	})
	if diags.HasError() {
		panic(diags)
	}
	return o
}

// --- schema shape -----------------------------------------------------------

func TestEvaluatorSchemaModifiers(t *testing.T) {
	ctx := context.Background()
	s := evaluatorSchema(t)
	requiresReplace := func(name string) bool {
		attribute, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a StringAttribute", name)
		}
		for _, pm := range attribute.PlanModifiers {
			if strings.Contains(pm.Description(ctx), "destroy and recreate") {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"type", "mode"} {
		if !requiresReplace(name) {
			t.Errorf("%s must force replacement", name)
		}
	}
	if requiresReplace("key") {
		t.Error("key must NOT force replacement: the API supports renaming in place")
	}
}

func TestEvaluatorSchemaComputedAttributes(t *testing.T) {
	s := evaluatorSchema(t)
	computedOnly := []string{"id", "enabled", "project_id", "model_id", "created_at", "updated_at"}
	for _, name := range computedOnly {
		a, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("%s must be Computed-only (computed=%v optional=%v required=%v)",
				name, a.IsComputed(), a.IsOptional(), a.IsRequired())
		}
	}
	for _, name := range []string{"key", "type", "path"} {
		if !s.Attributes[name].IsRequired() {
			t.Errorf("%s must be Required", name)
		}
	}
	// Optional+Computed: the server supplies a default the operator may omit.
	for _, name := range []string{"description", "output_type", "repetitions"} {
		a := s.Attributes[name]
		if !a.IsOptional() || !a.IsComputed() {
			t.Errorf("%s must be Optional+Computed", name)
		}
	}
	// `model` is config-authoritative: Computed would let a refresh substitute a
	// value the config never wrote.
	if s.Attributes["model"].IsComputed() {
		t.Error("model must not be Computed")
	}
}

// --- key validation ---------------------------------------------------------

func TestEvaluatorReservedKeyRejected(t *testing.T) {
	ctx := context.Background()
	check := func(key string) diag.Diagnostics {
		var resp validator.StringResponse
		reservedEvaluatorKeyValidator{}.ValidateString(ctx,
			validator.StringRequest{Path: path.Root("key"), ConfigValue: types.StringValue(key)}, &resp)
		return resp.Diagnostics
	}
	for _, key := range reservedEvaluatorKeys {
		if !check(key).HasError() {
			t.Errorf("reserved key %q must be rejected at plan time", key)
		}
	}
	if d := check("my-eval"); d.HasError() {
		t.Errorf("a normal key must be accepted: %v", d)
	}
	// An unknown value must defer rather than error.
	var resp validator.StringResponse
	reservedEvaluatorKeyValidator{}.ValidateString(ctx,
		validator.StringRequest{Path: path.Root("key"), ConfigValue: types.StringUnknown()}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("an unknown key must defer, got %v", resp.Diagnostics)
	}
}

func TestEvaluatorKeyPattern(t *testing.T) {
	valid := []string{"a", "my-eval", "my_eval", "Eval1", "a1-b_c2"}
	invalid := []string{"", "-eval", "eval-", "_eval", "eval_", "my eval", "my.eval", "my/eval"}
	for _, k := range valid {
		if !evaluatorKeyPattern.MatchString(k) {
			t.Errorf("key %q must be valid", k)
		}
	}
	for _, k := range invalid {
		if evaluatorKeyPattern.MatchString(k) {
			t.Errorf("key %q must be invalid", k)
		}
	}
}

// --- cross-field validation -------------------------------------------------

func TestEvaluatorValidatePython(t *testing.T) {
	code := types.StringValue("def evaluate(**kwargs): return True")
	cases := []struct {
		name      string
		cfg       evaluatorConfig
		wantError bool
	}{
		{"minimal ok", evaluatorConfig{Code: code}, false},
		{"number output ok", evaluatorConfig{Code: code, OutputType: types.StringValue("number")}, false},
		{"missing code rejected", evaluatorConfig{}, true},
		{"categorical output rejected", evaluatorConfig{Code: code, OutputType: types.StringValue("categorical")}, true},
		{"string output rejected", evaluatorConfig{Code: code, OutputType: types.StringValue("string")}, true},
		{"prompt rejected", evaluatorConfig{Code: code, Prompt: types.StringValue("x")}, true},
		{"mode rejected", evaluatorConfig{Code: code, Mode: types.StringValue("single")}, true},
		{"model rejected", evaluatorConfig{Code: code, Model: types.StringValue("openai/gpt-4o")}, true},
		{"repetitions rejected", evaluatorConfig{Code: code, Repetitions: types.Int64Value(2)}, true},
		{"jury rejected", evaluatorConfig{Code: code, Jury: juryObject(2, 0, nil)}, true},
		{"labels rejected", evaluatorConfig{Code: code, Labels: labelList("a", "b")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			tc.cfg.validatePython(&diags)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}

func TestEvaluatorValidateLLM(t *testing.T) {
	prompt := types.StringValue("judge it")
	model := types.StringValue("openai/gpt-4o")
	single := types.StringValue("single")
	jury := types.StringValue("jury")
	two := int64(2)
	five := int64(5)
	cases := []struct {
		name      string
		cfg       evaluatorConfig
		wantError bool
	}{
		{"single ok", evaluatorConfig{Prompt: prompt, Mode: single, Model: model}, false},
		{"jury ok", evaluatorConfig{Prompt: prompt, Mode: jury, Jury: juryObject(2, 0, &two)}, false},
		{"missing prompt rejected", evaluatorConfig{Mode: single, Model: model}, true},
		{"missing mode rejected", evaluatorConfig{Prompt: prompt, Model: model}, true},
		{"code rejected", evaluatorConfig{Code: types.StringValue("x"), Prompt: prompt, Mode: single, Model: model}, true},
		{"single without model rejected", evaluatorConfig{Prompt: prompt, Mode: single}, true},
		{"jury without jury block rejected", evaluatorConfig{Prompt: prompt, Mode: jury}, true},
		{"model and jury together rejected", evaluatorConfig{Prompt: prompt, Mode: single, Model: model, Jury: juryObject(2, 0, nil)}, true},
		{"jury with model rejected", evaluatorConfig{Prompt: prompt, Mode: jury, Model: model, Jury: juryObject(2, 0, nil)}, true},
		{"jury with string output rejected", evaluatorConfig{Prompt: prompt, Mode: jury, OutputType: types.StringValue("string"), Jury: juryObject(2, 0, nil)}, true},
		{"single with string output ok", evaluatorConfig{Prompt: prompt, Mode: single, Model: model, OutputType: types.StringValue("string")}, false},
		{"min_successful over judge count rejected", evaluatorConfig{Prompt: prompt, Mode: jury, Jury: juryObject(2, 1, &five)}, true},
		{"min_successful within judge count ok", evaluatorConfig{Prompt: prompt, Mode: jury, Jury: juryObject(2, 3, &five)}, false},
		{"categorical without labels rejected", evaluatorConfig{Prompt: prompt, Mode: single, Model: model, OutputType: types.StringValue("categorical")}, true},
		{"categorical with one label rejected", evaluatorConfig{Prompt: prompt, Mode: single, Model: model, OutputType: types.StringValue("categorical"), Labels: labelList("a")}, true},
		{"categorical with two labels ok", evaluatorConfig{Prompt: prompt, Mode: single, Model: model, OutputType: types.StringValue("categorical"), Labels: labelList("a", "b")}, false},
		{"case-insensitive duplicate labels rejected", evaluatorConfig{Prompt: prompt, Mode: single, Model: model, OutputType: types.StringValue("categorical"), Labels: labelList("Friendly", " friendly ")}, true},
		{"unknown mode defers", evaluatorConfig{Prompt: prompt, Mode: types.StringUnknown(), Model: model}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			tc.cfg.validateLLM(&diags)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}

// --- conversions ------------------------------------------------------------

func TestEvaluatorCreateInputConversion(t *testing.T) {
	python := evaluatorResourceModel{
		Key:         types.StringValue("my-eval"),
		Type:        types.StringValue(client.EvaluatorTypePython),
		Path:        types.StringValue("Default/evaluators"),
		Description: types.StringValue("checks"),
		OutputType:  types.StringValue("boolean"),
		Code:        types.StringValue("code"),
		// Everything llm-shaped is null and must not reach the request.
		Prompt: types.StringNull(), Mode: types.StringNull(), Model: types.StringNull(),
		Repetitions: types.Int64Null(),
	}
	in := python.createInput()
	if in.Key != "my-eval" || in.Type != client.EvaluatorTypePython || in.Path != "Default/evaluators" {
		t.Errorf("identity fields wrong: %+v", in)
	}
	if in.Code == nil || *in.Code != "code" {
		t.Errorf("code not carried: %+v", in.Code)
	}
	if in.Prompt != nil || in.Mode != nil || in.Model != nil || in.Jury != nil || in.Repetitions != nil {
		t.Errorf("llm fields must stay nil for python_eval: %+v", in)
	}

	minSuccessful := int64(3)
	count := int64(4)
	llm := evaluatorResourceModel{
		Key:    types.StringValue("jury-eval"),
		Type:   types.StringValue(client.EvaluatorTypeLLM),
		Path:   types.StringValue("Default"),
		Prompt: types.StringValue("judge"),
		Mode:   types.StringValue("jury"),
		Jury: &evaluatorJuryModel{
			Judges: []evaluatorJudgeModel{
				{
					Model:     types.StringValue("openai/gpt-4o"),
					Retry:     &evaluatorRetryModel{Count: types.Int64Value(count), OnCodes: []types.Int64{types.Int64Value(429)}},
					Fallbacks: []types.String{types.StringValue("anthropic/claude-sonnet-4-5")},
				},
				{Model: types.StringValue("google/gemini-2.5-pro")},
			},
			MinSuccessfulJudges: types.Int64Value(minSuccessful),
		},
		CategoricalLabels: []evaluatorLabelModel{
			{Value: types.StringValue("friendly"), Description: types.StringValue("warm")},
			{Value: types.StringValue("curt"), Description: types.StringNull()},
		},
	}
	lin := llm.createInput()
	if lin.Jury == nil || len(lin.Jury.Judges) != 2 {
		t.Fatalf("jury not converted: %+v", lin.Jury)
	}
	if len(lin.Jury.Judges[0].Fallbacks) != 1 || lin.Jury.Judges[0].Fallbacks[0] != "anthropic/claude-sonnet-4-5" {
		t.Errorf("fallbacks not converted: %+v", lin.Jury.Judges[0].Fallbacks)
	}
	if lin.Jury.Judges[0].Retry == nil || *lin.Jury.Judges[0].Retry.Count != 4 {
		t.Errorf("retry not converted: %+v", lin.Jury.Judges[0].Retry)
	}
	if lin.Jury.Judges[1].Retry != nil {
		t.Errorf("a judge with no retry must convert to nil, got %+v", lin.Jury.Judges[1].Retry)
	}
	if lin.Jury.MinSuccessfulJudges == nil || *lin.Jury.MinSuccessfulJudges != 3 {
		t.Errorf("min_successful_judges not converted: %+v", lin.Jury.MinSuccessfulJudges)
	}
	if len(lin.CategoricalLabels) != 2 || lin.CategoricalLabels[1].Description != nil {
		t.Errorf("labels not converted: %+v", lin.CategoricalLabels)
	}
}

func TestEvaluatorUpdateInputClearsLabels(t *testing.T) {
	m := evaluatorResourceModel{
		Key:  types.StringValue("my-eval"),
		Type: types.StringValue(client.EvaluatorTypePython),
		Path: types.StringValue("Default"),
	}
	in := m.updateInput("01JMDPA3QW5C1V0NJ1PW34T4E5")
	if !in.ClearCategoricalLabels {
		t.Error("an empty categorical_labels config must request an explicit clear")
	}
	m.CategoricalLabels = []evaluatorLabelModel{{Value: types.StringValue("a"), Description: types.StringNull()}}
	if m.updateInput("x").ClearCategoricalLabels {
		t.Error("configured labels must not request a clear")
	}
}

// --- the internal/external asymmetry ----------------------------------------

// stubModels is a ModelsAPI that resolves exactly one document id.
type stubModels struct {
	byID  map[string]*client.Model
	calls int
}

func (s *stubModels) Get(_ context.Context, id string) (*client.Model, error) {
	s.calls++
	if m, ok := s.byID[id]; ok {
		return m, nil
	}
	return nil, &client.Error{Code: client.CodeNotFound, Message: "no such model"}
}
func (s *stubModels) Resolve(ctx context.Context, ref string) (*client.Model, error) {
	return s.Get(ctx, ref)
}
func (s *stubModels) Create(context.Context, client.ModelCreateInput) (*client.Model, error) {
	return nil, nil
}
func (s *stubModels) Update(context.Context, client.ModelUpdateInput) (*client.Model, error) {
	return nil, nil
}
func (s *stubModels) Delete(context.Context, string) error { return nil }

// externalLLM / internalLLM are the two representations of THE SAME stored
// evaluator, as the two endpoints serialize it.
func externalLLM() *client.Evaluator {
	reps := int64(2)
	warm := "warm"
	return &client.Evaluator{
		Shape:       client.ShapeExternal,
		ID:          "01JMDPA3QW5C1V0NJ1PW34T4E5",
		Key:         "tone",
		Type:        client.EvaluatorTypeLLM,
		Description: "rates tone",
		Created:     "2026-07-01T10:00:00.000Z",
		Updated:     "2026-07-02T10:00:00.000Z",
		Mode:        "single",
		Prompt:      "Rate the tone",
		Repetitions: &reps,
		Model:       "openai/gpt-4o", // a STRING here …
		CategoricalLabels: []client.CategoricalLabel{
			{Value: "friendly", Description: &warm},
			{Value: "curt"},
		},
		// output_type / enabled / project_id / model_id are absent from this shape.
	}
}

func internalLLM() *client.Evaluator {
	reps := int64(2)
	warm := "warm"
	return &client.Evaluator{
		Shape:       client.ShapeInternal,
		ID:          "01JMDPA3QW5C1V0NJ1PW34T4E5",
		Key:         "tone", // from display_name
		Type:        client.EvaluatorTypeLLM,
		Description: "rates tone",
		Created:     "2026-07-01T10:00:00.000Z",
		Updated:     "2026-07-02T10:00:00.000Z",
		Mode:        "single",
		Prompt:      "Rate the tone",
		Repetitions: &reps,
		OutputType:  "categorical",
		Enabled:     true,
		ProjectID:   "proj_1",
		ModelID:     "01HZZMODELDOCID0000000000", // … a DOCUMENT ID here
		CategoricalLabels: []client.CategoricalLabel{
			{Value: "friendly", Description: &warm},
			{Value: "curt"},
		},
		// model (the provider/model string) is absent from this shape.
	}
}

// plannedLLM is what the operator configured, as the plan carries it into
// Create/Update.
func plannedLLM() evaluatorResourceModel {
	warm := types.StringValue("warm")
	return evaluatorResourceModel{
		ID:          types.StringUnknown(),
		Key:         types.StringValue("tone"),
		Type:        types.StringValue(client.EvaluatorTypeLLM),
		Path:        types.StringValue("Default/evaluators"),
		Description: types.StringValue("rates tone"),
		OutputType:  types.StringValue("categorical"),
		Enabled:     types.BoolUnknown(),
		ProjectID:   types.StringUnknown(),
		CreatedAt:   types.StringUnknown(),
		UpdatedAt:   types.StringUnknown(),
		Code:        types.StringNull(),
		Prompt:      types.StringValue("Rate the tone"),
		Mode:        types.StringValue("single"),
		Model:       types.StringValue("openai/gpt-4o"),
		ModelID:     types.StringUnknown(),
		Repetitions: types.Int64Value(2),
		CategoricalLabels: []evaluatorLabelModel{
			{Value: types.StringValue("friendly"), Description: warm},
			{Value: types.StringValue("curt"), Description: types.StringNull()},
		},
	}
}

// The write path sees the EXTERNAL body and the very next refresh sees the
// INTERNAL one, so the two must produce IDENTICAL state and a second read must
// be a fixed point — otherwise the diff never converges.
func TestEvaluatorShapeAsymmetryProducesNoDiff(t *testing.T) {
	ctx := context.Background()
	r := &evaluatorResource{models: &stubModels{byID: map[string]*client.Model{
		"01HZZMODELDOCID0000000000": {ID: "01HZZMODELDOCID0000000000", RefID: "openai/gpt-4o"},
	}}}

	// WRITE PATH: POST answered externally, then the by-id read-back.
	afterApply := plannedLLM()
	applyWrite(internalLLM(), externalLLM(), &afterApply)

	// Nothing may still be unknown once an apply completes.
	assertNoUnknowns(t, "after apply", afterApply)

	// READ PATH: the same evaluator, refreshed.
	afterRefresh := afterApply
	applyRead(internalLLM(), &afterRefresh)
	model, diags := r.resolveModelRef(ctx, internalLLM(), afterApply)
	if diags.HasError() {
		t.Fatalf("resolveModelRef: %v", diags)
	}
	afterRefresh.Model = model

	if !reflect.DeepEqual(afterApply, afterRefresh) {
		t.Fatalf("the two response shapes produced different state:\n apply:   %#v\n refresh: %#v", afterApply, afterRefresh)
	}

	// And refreshing again changes nothing (fixed point).
	second := afterRefresh
	applyRead(internalLLM(), &second)
	model2, _ := r.resolveModelRef(ctx, internalLLM(), afterRefresh)
	second.Model = model2
	if !reflect.DeepEqual(afterRefresh, second) {
		t.Errorf("refresh is not a fixed point:\n first:  %#v\n second: %#v", afterRefresh, second)
	}
}

func TestEvaluatorReadNeverWritesModelDocumentID(t *testing.T) {
	ctx := context.Background()
	stub := &stubModels{byID: map[string]*client.Model{
		"01HZZMODELDOCID0000000000": {ID: "01HZZMODELDOCID0000000000", RefID: "openai/gpt-4o"},
	}}
	r := &evaluatorResource{models: stub}

	// Fresh import: no model / model_id in prior state.
	fresh := evaluatorResourceModel{Model: types.StringNull(), ModelID: types.StringNull()}
	got, diags := r.resolveModelRef(ctx, internalLLM(), fresh)
	if diags.HasError() {
		t.Fatalf("resolveModelRef: %v", diags)
	}
	if got.ValueString() != "openai/gpt-4o" {
		t.Errorf("model = %q, want the resolved provider/model ref", got.ValueString())
	}
	if got.ValueString() == "01HZZMODELDOCID0000000000" {
		t.Fatal("a raw model document id must never be written into `model`")
	}
	if stub.calls != 1 {
		t.Errorf("catalog calls = %d, want 1 for an unresolved id", stub.calls)
	}

	// Steady state: model_id matches, so the catalog is NOT called again.
	stub.calls = 0
	steady := evaluatorResourceModel{
		Model:   types.StringValue("openai/gpt-4o"),
		ModelID: types.StringValue("01HZZMODELDOCID0000000000"),
	}
	got, _ = r.resolveModelRef(ctx, internalLLM(), steady)
	if got.ValueString() != "openai/gpt-4o" {
		t.Errorf("model = %q, want the configured ref kept verbatim", got.ValueString())
	}
	if stub.calls != 0 {
		t.Errorf("catalog calls = %d, want 0 when model_id still matches", stub.calls)
	}

	// Drift: the stored document id changed out of band → re-resolve.
	stub.byID["01HZZOTHERMODELDOC000000"] = &client.Model{ID: "01HZZOTHERMODELDOC000000", RefID: "anthropic/claude-sonnet-4-5"}
	drifted := internalLLM()
	drifted.ModelID = "01HZZOTHERMODELDOC000000"
	got, _ = r.resolveModelRef(ctx, drifted, steady)
	if got.ValueString() != "anthropic/claude-sonnet-4-5" {
		t.Errorf("an out-of-band model change must surface, got %q", got.ValueString())
	}

	// Unresolvable: keep the previous string and warn rather than writing an id.
	unknown := internalLLM()
	unknown.ModelID = "01HZZNOTINCATALOG0000000"
	got, diags = r.resolveModelRef(ctx, unknown, steady)
	if diags.HasError() {
		t.Errorf("an unresolvable model must warn, not error: %v", diags)
	}
	if len(diags) == 0 {
		t.Error("an unresolvable model must produce a warning")
	}
	if got.ValueString() != "openai/gpt-4o" {
		t.Errorf("the previous ref must be kept, got %q", got.ValueString())
	}

	// A jury evaluator has no single judge model at all.
	jury := internalLLM()
	jury.Mode = "jury"
	got, _ = r.resolveModelRef(ctx, jury, steady)
	if !got.IsNull() {
		t.Errorf("a jury evaluator must leave `model` null, got %q", got.ValueString())
	}
}

// Overwriting a known planned value with the read-back is what Terraform aborts
// as "Provider produced inconsistent result after apply".
func TestEvaluatorApplyWritePreservesPlan(t *testing.T) {
	planned := plannedLLM()
	// A server that (wrongly) reports different values for the managed fields.
	hostile := internalLLM()
	hostile.Description = "SERVER OVERWROTE THIS"
	hostile.OutputType = "number"
	reps := int64(3)
	hostile.Repetitions = &reps
	hostile.Key = "renamed-by-server"

	got := planned
	applyWrite(hostile, externalLLM(), &got)

	if got.Description.ValueString() != "rates tone" {
		t.Errorf("planned description was overwritten: %q", got.Description.ValueString())
	}
	if got.OutputType.ValueString() != "categorical" {
		t.Errorf("planned output_type was overwritten: %q", got.OutputType.ValueString())
	}
	if got.Repetitions.ValueInt64() != 2 {
		t.Errorf("planned repetitions were overwritten: %d", got.Repetitions.ValueInt64())
	}
	if got.Key.ValueString() != "tone" {
		t.Errorf("planned key was overwritten: %q", got.Key.ValueString())
	}
	// Computed-only values DO come from the server.
	if got.ProjectID.ValueString() != "proj_1" || !got.Enabled.ValueBool() {
		t.Errorf("computed-only attributes must take the server value: %+v", got)
	}
}

// The by-id read-back is the only place output_type can come from: the
// create/update response omits it entirely.
func TestEvaluatorApplyWriteFillsUnknownFromReadBack(t *testing.T) {
	planned := plannedLLM()
	planned.OutputType = types.StringUnknown()
	planned.Description = types.StringUnknown()
	planned.Repetitions = types.Int64Unknown()

	got := planned
	applyWrite(internalLLM(), externalLLM(), &got)

	if got.OutputType.ValueString() != "categorical" {
		t.Errorf("output_type must come from the by-id read-back, got %q", got.OutputType.ValueString())
	}
	if got.Description.ValueString() != "rates tone" {
		t.Errorf("description not filled from the read-back: %q", got.Description.ValueString())
	}
	if got.Repetitions.ValueInt64() != 2 {
		t.Errorf("repetitions not filled from the read-back: %d", got.Repetitions.ValueInt64())
	}
	assertNoUnknowns(t, "filled", got)
}

func TestEvaluatorNullUnknownsMakesStatePersistable(t *testing.T) {
	m := plannedLLM()
	m.ID = types.StringValue("01JMDPA3QW5C1V0NJ1PW34T4E5")
	nullUnknowns(&m)
	assertNoUnknowns(t, "after nullUnknowns", m)
	if m.ID.ValueString() != "01JMDPA3QW5C1V0NJ1PW34T4E5" {
		t.Error("nullUnknowns must not clear a known id")
	}
}

func assertNoUnknowns(t *testing.T, label string, m evaluatorResourceModel) {
	t.Helper()
	strings := map[string]types.String{
		"id": m.ID, "key": m.Key, "type": m.Type, "path": m.Path,
		"description": m.Description, "output_type": m.OutputType,
		"project_id": m.ProjectID, "created_at": m.CreatedAt, "updated_at": m.UpdatedAt,
		"code": m.Code, "prompt": m.Prompt, "mode": m.Mode, "model": m.Model, "model_id": m.ModelID,
	}
	for name, v := range strings {
		if v.IsUnknown() {
			t.Errorf("%s: %s is still unknown", label, name)
		}
	}
	if m.Enabled.IsUnknown() {
		t.Errorf("%s: enabled is still unknown", label)
	}
	if m.Repetitions.IsUnknown() {
		t.Errorf("%s: repetitions is still unknown", label)
	}
}

// --- type guard + import ----------------------------------------------------

func TestEvaluatorAssertManagedType(t *testing.T) {
	for _, kind := range []string{client.EvaluatorTypePython, client.EvaluatorTypeLLM} {
		if d := assertManagedType(kind); d != nil {
			t.Errorf("%s must be accepted: %v", kind, d)
		}
	}
	for _, kind := range []string{"ragas", "function_eval", "json_schema", "http_eval", "typescript_eval", "bedrock_eval"} {
		if assertManagedType(kind) == nil {
			t.Errorf("%s must be refused", kind)
		}
	}
}

// The built-in evaluators' slug ids have no evaluator record at all.
func TestEvaluatorImportRejectsNonULID(t *testing.T) {
	ctx := context.Background()
	r := &evaluatorResource{}
	bad := []string{"orq_pii_detection", "orq_secret_detection", "", "my-eval",
		"01JMDPA3QW5C1V0NJ1PW34T4", // 24 chars
		"01JMDPA3QW5C1V0NJ1PW34T4E5X",
		"01IMDPA3QW5C1V0NJ1PW34T4E5", // contains I, not in Crockford base32
		"01LMDPA3QW5C1V0NJ1PW34T4E5", // contains L
		"01UMDPA3QW5C1V0NJ1PW34T4E5", // contains U
	}
	for _, id := range bad {
		var resp resource.ImportStateResponse
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import id %q must be refused", id)
		}
	}
	if !ulidPattern.MatchString("01JMDPA3QW5C1V0NJ1PW34T4E5") {
		t.Error("a real ULID must be accepted")
	}
}

// --- read projection --------------------------------------------------------

func TestEvaluatorApplyReadLeavesPathAndJuryAlone(t *testing.T) {
	m := evaluatorResourceModel{
		Path: types.StringValue("Default/evaluators"),
		Jury: &evaluatorJuryModel{Judges: []evaluatorJudgeModel{{Model: types.StringValue("openai/gpt-4o")}}},
	}
	applyRead(internalLLM(), &m)
	if m.Path.ValueString() != "Default/evaluators" {
		t.Errorf("path must survive a refresh, got %q", m.Path.ValueString())
	}
	if m.Jury == nil || len(m.Jury.Judges) != 1 {
		t.Errorf("jury must survive a refresh, got %+v", m.Jury)
	}
}

func TestEvaluatorApplyReadPythonClearsLLMAttributes(t *testing.T) {
	m := plannedLLM()
	applyRead(&client.Evaluator{
		Shape: client.ShapeInternal, ID: "01JMDPA3QW5C1V0NJ1PW34T4E5", Key: "my-eval",
		Type: client.EvaluatorTypePython, OutputType: "boolean", Enabled: true,
		ProjectID: "proj_1", Code: "print(1)",
	}, &m)
	if !m.Prompt.IsNull() || !m.Mode.IsNull() || !m.ModelID.IsNull() || !m.Repetitions.IsNull() {
		t.Errorf("llm attributes must be null for a python_eval: %+v", m)
	}
	if m.Code.ValueString() != "print(1)" {
		t.Errorf("code not read: %q", m.Code.ValueString())
	}
	if len(m.CategoricalLabels) != 0 {
		t.Errorf("labels must be cleared: %+v", m.CategoricalLabels)
	}
}
