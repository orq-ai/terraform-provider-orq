package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestPreserveListOrder pins both halves of the rule. Keeping the caller's order
// when the server returns the same ids sorted is what stops a create or update
// from reporting an inconsistent result; taking the server's list when the
// MEMBERSHIP changed is what lets out-of-band drift surface. The second half is
// the one a same-length swap could quietly break.
func TestPreserveListOrder(t *testing.T) {
	cases := []struct {
		name  string
		prior types.List
		srv   []string
		want  []string
	}{
		{"same ids reordered keep the caller's order", stringList("p3", "p1", "p2"), []string{"p1", "p2", "p3"}, []string{"p3", "p1", "p2"}},
		{"same ids in the same order are unchanged", stringList("p1", "p2"), []string{"p1", "p2"}, []string{"p1", "p2"}},
		{"a swap at the same length takes the server's", stringList("p1", "p2"), []string{"p1", "p9"}, []string{"p1", "p9"}},
		{"a shorter server list takes the server's", stringList("p1", "p2"), []string{"p1"}, []string{"p1"}},
		{"a longer server list takes the server's", stringList("p1"), []string{"p1", "p2"}, []string{"p1", "p2"}},
		{"no prior takes the server's", types.ListNull(types.StringType), []string{"p2", "p1"}, []string{"p2", "p1"}},
		{"an unknown prior takes the server's", types.ListUnknown(types.StringType), []string{"p2", "p1"}, []string{"p2", "p1"}},
		{
			"a prior with an unknown element takes the server's",
			types.ListValueMust(types.StringType, []attr.Value{types.StringValue("p2"), types.StringUnknown()}),
			[]string{"p1", "p2"},
			[]string{"p1", "p2"},
		},
		{"empty on both sides stays empty", stringList(), []string{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := knownListStrings(preserveListOrder(tc.prior, tc.srv))
			if !ok {
				t.Fatal("the result must be a fully known list")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
