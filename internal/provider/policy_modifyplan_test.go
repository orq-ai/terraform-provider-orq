package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

// The tests below drive correlateEvaluatorComputed — the pure core of the policy
// resource's ModifyPlan. They construct the plan Terraform core would hand the
// provider AFTER its positional (by-index) merge of the computed nested-block
// fields (options, is_guardrail) and assert the correlation re-pairs each planned
// evaluator with its OWN prior value by identity, undoing the cross-assignment.

func evOptVal(s string) jsontypes.Normalized { return jsontypes.NewNormalizedValue(s) }
func evOptNull() jsontypes.Normalized        { return jsontypes.NewNormalizedNull() }
func evOptUnknown() jsontypes.Normalized     { return jsontypes.NewNormalizedUnknown() }

// evEval builds an evaluator model with a known id/execute_on.
func evEval(id, executeOn string, opts jsontypes.Normalized, g types.Bool) policyEvaluatorModel {
	return policyEvaluatorModel{
		ID:          types.StringValue(id),
		ExecuteOn:   types.StringValue(executeOn),
		Options:     opts,
		IsGuardrail: g,
	}
}

func assertEvalOptions(t *testing.T, got jsontypes.Normalized, want string) {
	t.Helper()
	if got.IsUnknown() {
		t.Fatalf("options unexpectedly unknown, want %q", want)
	}
	if got.IsNull() {
		t.Fatalf("options unexpectedly null, want %q", want)
	}
	if got.ValueString() != want {
		t.Errorf("options = %q, want %q", got.ValueString(), want)
	}
}

func assertEvalGuardrail(t *testing.T, got types.Bool, want bool) {
	t.Helper()
	if got.IsUnknown() || got.IsNull() {
		t.Fatalf("is_guardrail unexpectedly null/unknown, want %v", want)
	}
	if got.ValueBool() != want {
		t.Errorf("is_guardrail = %v, want %v", got.ValueBool(), want)
	}
}

// TestCorrelateEvaluatorSwapKeepsOwnComputed is the core MUST-FIX: swapping two
// evaluator blocks in config must NOT cross-assign one evaluator's server-derived
// options / is_guardrail onto the other. Core merges positionally, so the plan
// arrives with A's computed on B and vice-versa; correlation restores each
// evaluator's own values by (id, execute_on).
func TestCorrelateEvaluatorSwapKeepsOwnComputed(t *testing.T) {
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"a":1}`), types.BoolValue(true)),
		evEval("ev_B", "output", evOptVal(`{"b":2}`), types.BoolValue(false)),
	}
	// Operator swaps the blocks and omits the computed fields (config null).
	config := []policyEvaluatorModel{
		evEval("ev_B", "output", evOptNull(), types.BoolNull()),
		evEval("ev_A", "input", evOptNull(), types.BoolNull()),
	}
	// Core's positional merge: config user fields + prior[i] computed (WRONG).
	plan := []policyEvaluatorModel{
		evEval("ev_B", "output", evOptVal(`{"a":1}`), types.BoolValue(true)),
		evEval("ev_A", "input", evOptVal(`{"b":2}`), types.BoolValue(false)),
	}

	correlateEvaluatorComputed(plan, config, prior)

	// ev_B (now at index 0) must keep ITS OWN options/is_guardrail.
	assertEvalOptions(t, plan[0].Options, `{"b":2}`)
	assertEvalGuardrail(t, plan[0].IsGuardrail, false)
	// ev_A (now at index 1) likewise.
	assertEvalOptions(t, plan[1].Options, `{"a":1}`)
	assertEvalGuardrail(t, plan[1].IsGuardrail, true)
}

// TestCorrelateEvaluatorInsertFront proves inserting a new evaluator at the front
// leaves the existing one's computed values intact and marks the new one's
// unconfigured computed fields unknown ("known after apply").
func TestCorrelateEvaluatorInsertFront(t *testing.T) {
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"a":1}`), types.BoolValue(true)),
	}
	config := []policyEvaluatorModel{
		evEval("ev_C", "both", evOptNull(), types.BoolNull()),
		evEval("ev_A", "input", evOptNull(), types.BoolNull()),
	}
	// Core merges prior[0] (ev_A) onto the new front block ev_C, and has no prior
	// for index 1 so ev_A's computed arrive unknown.
	plan := []policyEvaluatorModel{
		evEval("ev_C", "both", evOptVal(`{"a":1}`), types.BoolValue(true)),
		evEval("ev_A", "input", evOptUnknown(), types.BoolUnknown()),
	}

	correlateEvaluatorComputed(plan, config, prior)

	// New ev_C: no prior match → unset computed fields reset to unknown.
	if !plan[0].Options.IsUnknown() {
		t.Errorf("new evaluator options must be unknown, got %q", plan[0].Options.ValueString())
	}
	if !plan[0].IsGuardrail.IsUnknown() {
		t.Errorf("new evaluator is_guardrail must be unknown, got %v", plan[0].IsGuardrail)
	}
	// Existing ev_A keeps its own values.
	assertEvalOptions(t, plan[1].Options, `{"a":1}`)
	assertEvalGuardrail(t, plan[1].IsGuardrail, true)
}

// TestCorrelateEvaluatorRemoveFirst proves removing the first evaluator leaves the
// survivor with its OWN computed values (not the removed evaluator's, which core's
// positional merge would otherwise assign at index 0).
func TestCorrelateEvaluatorRemoveFirst(t *testing.T) {
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"a":1}`), types.BoolValue(true)),
		evEval("ev_B", "output", evOptVal(`{"b":2}`), types.BoolValue(false)),
	}
	config := []policyEvaluatorModel{
		evEval("ev_B", "output", evOptNull(), types.BoolNull()),
	}
	// Core merges prior[0] (ev_A) onto the sole remaining block ev_B (WRONG).
	plan := []policyEvaluatorModel{
		evEval("ev_B", "output", evOptVal(`{"a":1}`), types.BoolValue(true)),
	}

	correlateEvaluatorComputed(plan, config, prior)

	if len(plan) != 1 || plan[0].ID.ValueString() != "ev_B" {
		t.Fatalf("expected only ev_B to remain, got %+v", plan)
	}
	assertEvalOptions(t, plan[0].Options, `{"b":2}`)
	assertEvalGuardrail(t, plan[0].IsGuardrail, false)
}

// TestCorrelateEvaluatorDuplicateIDs proves duplicate identities pair positionally
// among themselves: the i-th duplicate in the plan pairs with the i-th unused
// prior of the same (id, execute_on) — a deterministic tie-break.
func TestCorrelateEvaluatorDuplicateIDs(t *testing.T) {
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"x":1}`), types.BoolValue(true)),
		evEval("ev_A", "input", evOptVal(`{"y":2}`), types.BoolValue(false)),
	}
	config := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptNull(), types.BoolNull()),
		evEval("ev_A", "input", evOptNull(), types.BoolNull()),
	}
	// Start from unknown computed to prove the pairing is positional (not "first
	// match wins for both", which would give plan[1] the same as plan[0]).
	plan := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptUnknown(), types.BoolUnknown()),
		evEval("ev_A", "input", evOptUnknown(), types.BoolUnknown()),
	}

	correlateEvaluatorComputed(plan, config, prior)

	assertEvalOptions(t, plan[0].Options, `{"x":1}`)
	assertEvalGuardrail(t, plan[0].IsGuardrail, true)
	assertEvalOptions(t, plan[1].Options, `{"y":2}`)
	assertEvalGuardrail(t, plan[1].IsGuardrail, false)
}

// TestCorrelateEvaluatorUnknownID proves an evaluator whose id is unknown
// (interpolated from another not-yet-applied resource) is NOT correlated: its
// unconfigured computed fields stay unknown rather than being guessed from a
// positionally-merged prior. An explicitly configured field is still preserved.
func TestCorrelateEvaluatorUnknownID(t *testing.T) {
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"a":1}`), types.BoolValue(true)),
	}

	t.Run("unset stays unknown", func(t *testing.T) {
		config := []policyEvaluatorModel{{
			ID:          types.StringUnknown(),
			ExecuteOn:   types.StringValue("input"),
			Options:     evOptNull(),
			IsGuardrail: types.BoolNull(),
		}}
		// Core positionally merged prior[0]'s computed onto this block — a value the
		// correlation must discard because the identity is unknowable.
		plan := []policyEvaluatorModel{{
			ID:          types.StringUnknown(),
			ExecuteOn:   types.StringValue("input"),
			Options:     evOptVal(`{"a":1}`),
			IsGuardrail: types.BoolValue(true),
		}}
		correlateEvaluatorComputed(plan, config, prior)
		if !plan[0].Options.IsUnknown() {
			t.Errorf("unknown-id options must stay unknown, got %q", plan[0].Options.ValueString())
		}
		if !plan[0].IsGuardrail.IsUnknown() {
			t.Errorf("unknown-id is_guardrail must stay unknown, got %v", plan[0].IsGuardrail)
		}
	})

	t.Run("explicit config preserved", func(t *testing.T) {
		config := []policyEvaluatorModel{{
			ID:          types.StringUnknown(),
			ExecuteOn:   types.StringValue("input"),
			Options:     evOptVal(`{"set":1}`),
			IsGuardrail: types.BoolValue(true),
		}}
		plan := []policyEvaluatorModel{{
			ID:          types.StringUnknown(),
			ExecuteOn:   types.StringValue("input"),
			Options:     evOptVal(`{"set":1}`),
			IsGuardrail: types.BoolValue(true),
		}}
		correlateEvaluatorComputed(plan, config, prior)
		assertEvalOptions(t, plan[0].Options, `{"set":1}`)
		assertEvalGuardrail(t, plan[0].IsGuardrail, true)
	})
}

// TestCorrelateEvaluatorUnrelatedUpdateRetains is the e2bb999 regression case: an
// unrelated update (e.g. a display_name rename) leaves the evaluator block
// untouched and its computed fields unconfigured; the prior options/is_guardrail
// must be retained (carried into the plan), not wiped by the wholesale replace.
func TestCorrelateEvaluatorUnrelatedUpdateRetains(t *testing.T) {
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"threshold":0.8}`), types.BoolValue(true)),
	}
	config := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptNull(), types.BoolNull()),
	}
	// The unconfigured computed fields plan as unknown; correlation fills them.
	plan := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptUnknown(), types.BoolUnknown()),
	}

	correlateEvaluatorComputed(plan, config, prior)

	assertEvalOptions(t, plan[0].Options, `{"threshold":0.8}`)
	assertEvalGuardrail(t, plan[0].IsGuardrail, true)
}

// TestCorrelateEvaluatorExplicitConfigUntouched is the AssertPlanValid safety
// guard: an explicitly configured options / is_guardrail (non-null config) is
// never overwritten by the prior value — correlation only fills unset fields, so
// core never sees a planned value diverge from a non-null config value.
func TestCorrelateEvaluatorExplicitConfigUntouched(t *testing.T) {
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"a":1}`), types.BoolValue(false)),
	}
	// Operator now sets both fields explicitly (config non-null); plan == config.
	config := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{}`), types.BoolValue(true)),
	}
	plan := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{}`), types.BoolValue(true)),
	}

	correlateEvaluatorComputed(plan, config, prior)

	assertEvalOptions(t, plan[0].Options, `{}`)
	assertEvalGuardrail(t, plan[0].IsGuardrail, true)
}

// TestMatchPriorByIDPositional proves the apply-path read-back correlation pairs
// same-id evaluators positionally (server[0]→first prior of that id, server[1]→
// second), a tie-break consistent with correlateEvaluatorComputed, rather than
// collapsing every duplicate onto the first prior.
func TestMatchPriorByIDPositional(t *testing.T) {
	server := []client.EvaluatorRef{
		{ID: "ev_A", ExecuteOn: "input"},
		{ID: "ev_A", ExecuteOn: "output"},
		{ID: "ev_B", ExecuteOn: "input"},
	}
	prior := []policyEvaluatorModel{
		evEval("ev_A", "input", evOptVal(`{"1":1}`), types.BoolNull()),
		evEval("ev_A", "output", evOptVal(`{"2":2}`), types.BoolNull()),
	}
	got := matchPriorByID(server, prior)
	want := []int{0, 1, -1} // ev_A→0, ev_A→1, ev_B has no prior.
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("priorIdx[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}
