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
