package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

func TestGuardrailApplyPreservesPlannedEmptyOptions(t *testing.T) {
	r := &guardrailRuleResource{}
	m := guardrailRuleResourceModel{
		Guardrails: []guardrailRefModel{{
			ID:        types.StringValue("01EVAL"),
			ExecuteOn: types.StringValue("output"),
			Options:   jsontypes.NewNormalizedValue("{}"),
		}},
	}
	r.apply(&client.GuardrailRule{
		Guardrails: []client.GuardrailRef{{ID: "01EVAL", ExecuteOn: "output"}},
	}, &m)
	if got := m.Guardrails[0].Options; got.IsNull() || got.ValueString() != "{}" {
		t.Fatalf("planned empty options not preserved: %v", got)
	}

	// A ref the plan never gave options for stays null.
	m2 := guardrailRuleResourceModel{
		Guardrails: []guardrailRefModel{{
			ID:        types.StringValue("01EVAL"),
			ExecuteOn: types.StringValue("output"),
			Options:   jsontypes.NewNormalizedNull(),
		}},
	}
	r.apply(&client.GuardrailRule{
		Guardrails: []client.GuardrailRef{{ID: "01EVAL", ExecuteOn: "output"}},
	}, &m2)
	if !m2.Guardrails[0].Options.IsNull() {
		t.Fatalf("null options should stay null, got %v", m2.Guardrails[0].Options)
	}

	// Two references to the same guardrail and phase must not share a planned
	// value: only the element that asked for "{}" keeps it.
	dup := guardrailRuleResourceModel{
		Guardrails: []guardrailRefModel{
			{ID: types.StringValue("01EVAL"), ExecuteOn: types.StringValue("input"), Options: jsontypes.NewNormalizedValue("{}")},
			{ID: types.StringValue("01EVAL"), ExecuteOn: types.StringValue("input"), Options: jsontypes.NewNormalizedNull()},
		},
	}
	r.apply(&client.GuardrailRule{
		Guardrails: []client.GuardrailRef{
			{ID: "01EVAL", ExecuteOn: "input"},
			{ID: "01EVAL", ExecuteOn: "input"},
		},
	}, &dup)
	if got := dup.Guardrails[0].Options; got.ValueString() != "{}" {
		t.Errorf("the ref that planned {} must keep it, got %v", got)
	}
	if got := dup.Guardrails[1].Options; !got.IsNull() {
		t.Errorf("a duplicate ref without options must stay null, got %v", got)
	}

	// Non-empty server options always win over the planned value.
	m3 := guardrailRuleResourceModel{
		Guardrails: []guardrailRefModel{{
			ID:        types.StringValue("01EVAL"),
			ExecuteOn: types.StringValue("output"),
			Options:   jsontypes.NewNormalizedValue("{}"),
		}},
	}
	r.apply(&client.GuardrailRule{
		Guardrails: []client.GuardrailRef{{
			ID: "01EVAL", ExecuteOn: "output",
			Options: map[string]any{"threshold": 0.5},
		}},
	}, &m3)
	if m3.Guardrails[0].Options.ValueString() != `{"threshold":0.5}` {
		t.Fatalf("server options should win: %v", m3.Guardrails[0].Options)
	}
}

// Same empty-vs-absent hazard as project.description and evaluator.description.
func TestGuardrailApplyPreservesEmptyDescription(t *testing.T) {
	r := &guardrailRuleResource{}
	stored := &client.GuardrailRule{ID: "g1", DisplayName: "pii"}

	kept := guardrailRuleResourceModel{Description: types.StringValue("")}
	r.apply(stored, &kept)
	if kept.Description.IsNull() {
		t.Error("a configured empty description must survive the refresh")
	}

	omitted := guardrailRuleResourceModel{Description: types.StringNull()}
	r.apply(stored, &omitted)
	if !omitted.Description.IsNull() {
		t.Error("an omitted description must stay null")
	}
}
