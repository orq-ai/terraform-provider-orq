package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The server refuses a budget whose limits.period is UNSPECIFIED — on create and
// on any update that carries limits (budgets/connect_routes.go: "limits.period
// must be one of DAILY, WEEKLY, MONTHLY, YEARLY, ONE_TIME"). Optional made that
// an apply-time 400; Required plus the enum validator catches it at plan time.
func TestBudgetPeriodIsRequiredAndEnumValidated(t *testing.T) {
	ctx := context.Background()
	var sch resource.SchemaResponse
	NewBudgetResource().Schema(ctx, resource.SchemaRequest{}, &sch)
	if sch.Diagnostics.HasError() {
		t.Fatalf("schema errors: %v", sch.Diagnostics)
	}

	limits := sch.Schema.Attributes["limits"].(schema.SingleNestedAttribute)
	period := limits.Attributes["period"].(schema.StringAttribute)
	if !period.Required {
		t.Error("limits.period must be Required")
	}
	if period.Optional || period.Computed {
		t.Error("limits.period must not be Optional or Computed — the server has no default")
	}

	rejected := func(value string) bool {
		for _, v := range period.Validators {
			var resp validator.StringResponse
			v.ValidateString(ctx, validator.StringRequest{
				Path:        path.Root("limits").AtName("period"),
				ConfigValue: types.StringValue(value),
			}, &resp)
			if resp.Diagnostics.HasError() {
				return true
			}
		}
		return false
	}
	for _, value := range []string{"DAILY", "WEEKLY", "MONTHLY", "YEARLY", "ONE_TIME"} {
		if rejected(value) {
			t.Errorf("period %q must be accepted", value)
		}
	}
	for _, value := range []string{"", "monthly", "HOURLY"} {
		if !rejected(value) {
			t.Errorf("period %q must be rejected at plan time", value)
		}
	}
}

// A Required period is always in the plan, so the write carries it verbatim.
func TestBudgetWriteInputCarriesPeriod(t *testing.T) {
	r := &budgetResource{}
	in, err := r.writeInput(context.Background(), &budgetResourceModel{
		Limits: &budgetLimitsModel{
			Period: types.StringValue("MONTHLY"),
			Amount: types.Float64Value(100),
		},
	})
	if err != nil {
		t.Fatalf("writeInput: %v", err)
	}
	if in.Period != "MONTHLY" {
		t.Errorf("period = %q, want MONTHLY", in.Period)
	}
}

// budgetAlert builds an alert model for the id-correlation tests. id "<unknown>"
// is an unknown value, "" is null; dimension "<unknown>" is unknown, "" is null
// (an omitted dimension), any other string is a concrete value.
func budgetAlert(id string, threshold int64, dim string) budgetAlertModel {
	m := budgetAlertModel{ThresholdPercent: types.Int64Value(threshold)}
	switch id {
	case "<unknown>":
		m.ID = types.StringUnknown()
	case "":
		m.ID = types.StringNull()
	default:
		m.ID = types.StringValue(id)
	}
	switch dim {
	case "<unknown>":
		m.Dimension = types.StringUnknown()
	case "":
		m.Dimension = types.StringNull()
	default:
		m.Dimension = types.StringValue(dim)
	}
	return m
}

// wantAlertID asserts the id of the alert at index i. want "<unknown>" expects an
// unknown value, any other string an exact known value.
func wantAlertID(t *testing.T, got []budgetAlertModel, i int, want string) {
	t.Helper()
	if i >= len(got) {
		t.Fatalf("alert index %d out of range (len %d)", i, len(got))
	}
	id := got[i].ID
	if want == "<unknown>" {
		if !id.IsUnknown() {
			t.Errorf("alert[%d] id: want unknown, got %q (null=%v)", i, id.ValueString(), id.IsNull())
		}
		return
	}
	if id.IsUnknown() || id.IsNull() || id.ValueString() != want {
		t.Errorf("alert[%d] id: want %q, got %q (unknown=%v null=%v)", i, want, id.ValueString(), id.IsUnknown(), id.IsNull())
	}
}

// TestCorrelateAlertIDsByKey proves the MUST-FIX: server-issued alert ids are
// carried into the plan by STABLE KEY (threshold + dimension), not by list
// position, so inserting / removing / reordering alerts no longer reassigns an
// id to a different threshold. The `plan` slices below carry the WRONG,
// positionally-merged ids Terraform core proposes; correlateAlertIDs must correct
// them.
func TestCorrelateAlertIDsByKey(t *testing.T) {
	// Reorder: prior [A@80, B@90] with config swapped to [90, 80]. Core merges the
	// prior id at each position (A@90, B@80); the id must follow its own threshold.
	t.Run("reorder keeps id with its threshold", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST"), budgetAlert("B", 90, "COST")}
		config := []budgetAlertModel{budgetAlert("", 90, ""), budgetAlert("", 80, "")}
		plan := []budgetAlertModel{budgetAlert("A", 90, "COST"), budgetAlert("B", 80, "COST")}
		correlateAlertIDs(plan, config, prior)
		wantAlertID(t, plan, 0, "B")
		wantAlertID(t, plan, 1, "A")
	})

	// Insert-at-front: a new 70% alert prepended. It has no prior -> unknown id;
	// the existing 80/90 alerts keep A/B.
	t.Run("insert at front", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST"), budgetAlert("B", 90, "COST")}
		config := []budgetAlertModel{budgetAlert("", 70, ""), budgetAlert("", 80, ""), budgetAlert("", 90, "")}
		plan := []budgetAlertModel{budgetAlert("A", 70, "COST"), budgetAlert("B", 80, "COST"), budgetAlert("<unknown>", 90, "COST")}
		correlateAlertIDs(plan, config, prior)
		wantAlertID(t, plan, 0, "<unknown>")
		wantAlertID(t, plan, 1, "A")
		wantAlertID(t, plan, 2, "B")
	})

	// Remove-first: the 80% alert (A) is dropped; the surviving 90% alert keeps B,
	// NOT A (which the positional merge would have handed it).
	t.Run("remove first", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST"), budgetAlert("B", 90, "COST")}
		config := []budgetAlertModel{budgetAlert("", 90, "")}
		plan := []budgetAlertModel{budgetAlert("A", 90, "COST")}
		correlateAlertIDs(plan, config, prior)
		wantAlertID(t, plan, 0, "B")
	})

	// Two identical alerts (same key) pair positionally among themselves.
	t.Run("identical alerts pair positionally", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST"), budgetAlert("B", 80, "COST")}
		config := []budgetAlertModel{budgetAlert("", 80, ""), budgetAlert("", 80, "")}
		plan := []budgetAlertModel{budgetAlert("A", 80, "COST"), budgetAlert("B", 80, "COST")}
		correlateAlertIDs(plan, config, prior)
		wantAlertID(t, plan, 0, "A")
		wantAlertID(t, plan, 1, "B")
	})

	// Dimension participates in the key: two 80% alerts distinguished only by
	// dimension keep their own ids when reordered.
	t.Run("dimension distinguishes same threshold", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST"), budgetAlert("B", 80, "TOKENS")}
		config := []budgetAlertModel{budgetAlert("", 80, "TOKENS"), budgetAlert("", 80, "COST")}
		plan := []budgetAlertModel{budgetAlert("A", 80, "COST"), budgetAlert("B", 80, "TOKENS")}
		correlateAlertIDs(plan, config, prior)
		wantAlertID(t, plan, 0, "B")
		wantAlertID(t, plan, 1, "A")
	})

	// A stable, unchanged single alert keeps its id (no churn). An omitted
	// (null) config dimension matches the server's stored COST.
	t.Run("stable alert keeps its id", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST")}
		config := []budgetAlertModel{budgetAlert("", 80, "")}
		plan := []budgetAlertModel{budgetAlert("A", 80, "COST")}
		correlateAlertIDs(plan, config, prior)
		wantAlertID(t, plan, 0, "A")
	})

	// Changing a threshold changes the identity: no match -> unknown id (the server
	// mints a fresh alert whose fired-state does not carry over from the old one).
	t.Run("changed threshold gets a fresh id", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST")}
		config := []budgetAlertModel{budgetAlert("", 85, "")}
		plan := []budgetAlertModel{budgetAlert("A", 85, "COST")}
		correlateAlertIDs(plan, config, prior)
		wantAlertID(t, plan, 0, "<unknown>")
	})

	// An unknown key component (interpolated threshold or dimension) cannot be
	// matched: leave the id unknown rather than guessing.
	t.Run("unknown key component leaves id unknown", func(t *testing.T) {
		prior := []budgetAlertModel{budgetAlert("A", 80, "COST")}

		unknownThreshold := budgetAlertModel{ID: types.StringNull(), ThresholdPercent: types.Int64Unknown(), Dimension: types.StringNull()}
		plan := []budgetAlertModel{budgetAlert("A", 80, "COST")}
		correlateAlertIDs(plan, []budgetAlertModel{unknownThreshold}, prior)
		wantAlertID(t, plan, 0, "<unknown>")

		plan = []budgetAlertModel{budgetAlert("A", 80, "COST")}
		correlateAlertIDs(plan, []budgetAlertModel{budgetAlert("", 80, "<unknown>")}, prior)
		wantAlertID(t, plan, 0, "<unknown>")
	})
}
