package provider

import (
	"testing"
)

func TestParseTeamMemberID(t *testing.T) {
	teamID, userID, ok := parseTeamMemberID("team-1/user-2")
	if !ok || teamID != "team-1" || userID != "user-2" {
		t.Fatalf("got (%q, %q, %v), want (team-1, user-2, true)", teamID, userID, ok)
	}
	for _, bad := range []string{"", "no-slash", "/user", "team/", "a/b/c"} {
		if _, _, ok := parseTeamMemberID(bad); ok {
			t.Errorf("parseTeamMemberID(%q) = true, want false", bad)
		}
	}
}
