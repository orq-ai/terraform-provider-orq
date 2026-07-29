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

// juryObject builds a stand-in for the jury block carrying only what
// validateJuryObject inspects: the two judge-list lengths and the threshold.
// Element typing is irrelevant to that check, so plain string lists are used.
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

func juryNull() types.Object { return types.ObjectNull(juryAttrTypes) }

// --- schema shape -----------------------------------------------------------

// TestEvaluatorSchemaModifiers pins the two attributes whose plan modifier is a
// correctness decision rather than cosmetics: `type` (the API rejects a type
// change with a 400) and `mode` (the $set-merge update cannot unset the
// abandoned jury/model sub-document, so jury->single would silently keep judging
// as a jury).
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
	// `key` is renameable in place (the API patches display_name) — marking it
	// RequiresReplace would destroy evaluators on a harmless rename.
	if requiresReplace("key") {
		t.Error("key must NOT force replacement: the API supports renaming in place")
	}
}

// TestEvaluatorSchemaComputedAttributes pins which attributes are server-owned.
// `enabled`, `project_id` and `model_id` are Computed-ONLY because no public
// create/update body accepts them — making any of them Optional would silently
// drop the operator's value.
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
	// `model` must NOT be Computed: it is config-authoritative and a Computed
	// marking would let a refresh substitute a value the config never wrote.
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

// TestEvaluatorKeyPattern pins the charset the server enforces.
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
		code      types.String
		prompt    types.String
		mode      types.String
		model     types.String
		output    types.String
		reps      types.Int64
		jury      types.Object
		labels    types.List
		wantError bool
	}{
		{"minimal ok", code, types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull(), types.Int64Null(), juryNull(), labelList(), false},
		{"number output ok", code, types.StringNull(), types.StringNull(), types.StringNull(), types.StringValue("number"), types.Int64Null(), juryNull(), labelList(), false},
		{"missing code rejected", types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull(), types.Int64Null(), juryNull(), labelList(), true},
		{"categorical output rejected", code, types.StringNull(), types.StringNull(), types.StringNull(), types.StringValue("categorical"), types.Int64Null(), juryNull(), labelList(), true},
		{"string output rejected", code, types.StringNull(), types.StringNull(), types.StringNull(), types.StringValue("string"), types.Int64Null(), juryNull(), labelList(), true},
		{"prompt rejected", code, types.StringValue("x"), types.StringNull(), types.StringNull(), types.StringNull(), types.Int64Null(), juryNull(), labelList(), true},
		{"mode rejected", code, types.StringNull(), types.StringValue("single"), types.StringNull(), types.StringNull(), types.Int64Null(), juryNull(), labelList(), true},
		{"model rejected", code, types.StringNull(), types.StringNull(), types.StringValue("openai/gpt-4o"), types.StringNull(), types.Int64Null(), juryNull(), labelList(), true},
		{"repetitions rejected", code, types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull(), types.Int64Value(2), juryNull(), labelList(), true},
		{"jury rejected", code, types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull(), types.Int64Null(), juryObject(2, 0, nil), labelList(), true},
		{"labels rejected", code, types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull(), types.Int64Null(), juryNull(), labelList("a", "b"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			validatePythonEvaluator(tc.code, tc.prompt, tc.mode, tc.model, tc.output, tc.reps, tc.jury, tc.labels, &diags)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}

func TestEvaluatorValidateLLM(t *testing.T) {
	prompt := types.StringValue("judge it")
	model := types.StringValue("openai/gpt-4o")
	two := int64(2)
	five := int64(5)
	cases := []struct {
		name      string
		code      types.String
		prompt    types.String
		mode      types.String
		model     types.String
		output    types.String
		jury      types.Object
		labels    types.List
		wantError bool
	}{
		{"single ok", types.StringNull(), prompt, types.StringValue("single"), model, types.StringNull(), juryNull(), labelList(), false},
		{"jury ok", types.StringNull(), prompt, types.StringValue("jury"), types.StringNull(), types.StringNull(), juryObject(2, 0, &two), labelList(), false},
		{"missing prompt rejected", types.StringNull(), types.StringNull(), types.StringValue("single"), model, types.StringNull(), juryNull(), labelList(), true},
		{"missing mode rejected", types.StringNull(), prompt, types.StringNull(), model, types.StringNull(), juryNull(), labelList(), true},
		{"code rejected", types.StringValue("x"), prompt, types.StringValue("single"), model, types.StringNull(), juryNull(), labelList(), true},
		{"single without model rejected", types.StringNull(), prompt, types.StringValue("single"), types.StringNull(), types.StringNull(), juryNull(), labelList(), true},
		{"jury without jury block rejected", types.StringNull(), prompt, types.StringValue("jury"), types.StringNull(), types.StringNull(), juryNull(), labelList(), true},
		{"model and jury together rejected", types.StringNull(), prompt, types.StringValue("single"), model, types.StringNull(), juryObject(2, 0, nil), labelList(), true},
		{"jury with model rejected", types.StringNull(), prompt, types.StringValue("jury"), model, types.StringNull(), juryObject(2, 0, nil), labelList(), true},
		{"jury with string output rejected", types.StringNull(), prompt, types.StringValue("jury"), types.StringNull(), types.StringValue("string"), juryObject(2, 0, nil), labelList(), true},
		{"single with string output ok", types.StringNull(), prompt, types.StringValue("single"), model, types.StringValue("string"), juryNull(), labelList(), false},
		{"min_successful over judge count rejected", types.StringNull(), prompt, types.StringValue("jury"), types.StringNull(), types.StringNull(), juryObject(2, 1, &five), labelList(), true},
		{"min_successful within judge count ok", types.StringNull(), prompt, types.StringValue("jury"), types.StringNull(), types.StringNull(), juryObject(2, 3, &five), labelList(), false},
		{"categorical without labels rejected", types.StringNull(), prompt, types.StringValue("single"), model, types.StringValue("categorical"), juryNull(), labelList(), true},
		{"categorical with one label rejected", types.StringNull(), prompt, types.StringValue("single"), model, types.StringValue("categorical"), juryNull(), labelList("a"), true},
		{"categorical with two labels ok", types.StringNull(), prompt, types.StringValue("single"), model, types.StringValue("categorical"), juryNull(), labelList("a", "b"), false},
		{"case-insensitive duplicate labels rejected", types.StringNull(), prompt, types.StringValue("single"), model, types.StringValue("categorical"), juryNull(), labelList("Friendly", " friendly "), true},
		{"unknown mode defers", types.StringNull(), prompt, types.StringUnknown(), model, types.StringNull(), juryNull(), labelList(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diags diag.Diagnostics
			validateLLMEvaluator(tc.code, tc.prompt, tc.mode, tc.model, tc.output, tc.jury, tc.labels, &diags)
			if diags.HasError() != tc.wantError {
				t.Errorf("HasError = %v, want %v (%v)", diags.HasError(), tc.wantError, diags)
			}
		})
	}
}

// --- conversions ------------------------------------------------------------

// TestEvaluatorCreateInputConversion covers config -> request for both types,
// including the fallback-list-of-strings -> list-of-objects mapping.
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

// TestEvaluatorUpdateInputClearsLabels proves the $set-merge workaround is
// driven from the model: no labels in config => "clear them".
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
// evaluator, exactly as the two endpoints serialize it.
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
// Create/Update. output_type is set explicitly here so the write path has a
// known planned value to preserve.
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

// TestEvaluatorShapeAsymmetryProducesNoDiff is the central regression test for
// this resource.
//
// Create/Update see the EXTERNAL body (key, `model` as a string, no
// output_type/enabled/project_id) while the very next refresh sees the INTERNAL
// one (display_name, `model` as a document-id object, plus those three fields).
// A naive implementation would therefore write one state on apply and a
// DIFFERENT state on the next read — a diff that never converges.
//
// Here the state produced by the write path and the state produced by the read
// path over the same evaluator must be IDENTICAL, and a second read must be a
// fixed point.
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

	// READ PATH: the same evaluator, refreshed. path and jury are not carried by
	// any response, so they survive from prior state — seed them as such.
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

// TestEvaluatorReadNeverWritesModelDocumentID proves the single worst failure
// mode is impossible: `model` must never be filled with the stored document id.
// With no prior state (a fresh import) the id is resolved through the catalog;
// with a matching prior model_id the catalog is not consulted at all.
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

	// Steady state: model_id matches, so the string is kept verbatim and the
	// catalog is NOT called again.
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

// TestEvaluatorApplyWritePreservesPlan proves the write path never overwrites a
// KNOWN planned value with the read-back — the rule Terraform enforces as
// "Provider produced inconsistent result after apply".
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

// TestEvaluatorApplyWriteFillsUnknownFromReadBack proves the other half of the
// authority split: an attribute the config omitted (planned unknown) IS filled
// from the by-id read-back — the only place output_type can come from, since the
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

// TestEvaluatorNullUnknownsMakesStatePersistable proves the partial-state escape
// hatch used when a write succeeds but the read-back fails: every attribute has
// to be concrete before state can be written, or Terraform rejects it and the
// just-created evaluator is orphaned.
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

// TestEvaluatorAssertManagedType proves an out-of-scope evaluator type is
// refused rather than adopted (which would make the next apply try to change an
// immutable `type`, or destroy and recreate the record as something else).
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

// TestEvaluatorImportRejectsNonULID proves import refuses anything that is not a
// record id — notably the built-in evaluators' slug ids, which have no record.
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

// TestEvaluatorApplyReadLeavesPathAndJuryAlone proves the read path does not
// invent values for the two attributes no response can express. Nulling them
// would produce a perpetual diff against a config that sets them.
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

// TestEvaluatorApplyReadPythonClearsLLMAttributes proves a python_eval refresh
// leaves no stale llm values behind.
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
