package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

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
