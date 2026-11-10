package client

import (
	"slices"
	"testing"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
)

func TestVaultSharingRoundTrip(t *testing.T) {
	allProto := vaultSharingToProto(&VaultSharing{AllFactories: true})
	if allProto.GetAllFactories() == nil {
		t.Fatal("expected AllFactories oneof")
	}
	gotAll := vaultSharingFromProto(&platformv1.Vault{
		Scope: &platformv1.Vault_Sharing{Sharing: allProto},
	})
	if gotAll == nil || !gotAll.AllFactories {
		t.Fatalf("expected AllFactories=true, got %+v", gotAll)
	}

	selProto := vaultSharingToProto(&VaultSharing{
		FactoryIDs: []string{"fac_1"},
		ProjectIDs: []string{"proj_1", "proj_2"},
	})
	sel := selProto.GetSelected()
	if sel == nil {
		t.Fatal("expected Selected oneof")
	}
	if !slices.Equal(sel.GetFactoryIds(), []string{"fac_1"}) {
		t.Errorf("factory_ids = %v, want [fac_1]", sel.GetFactoryIds())
	}
	if !slices.Equal(sel.GetProjectIds(), []string{"proj_1", "proj_2"}) {
		t.Errorf("project_ids = %v, want [proj_1 proj_2]", sel.GetProjectIds())
	}
	gotSel := vaultSharingFromProto(&platformv1.Vault{
		Scope: &platformv1.Vault_Sharing{Sharing: selProto},
	})
	if gotSel == nil || gotSel.AllFactories {
		t.Fatalf("expected selected sharing, got %+v", gotSel)
	}
	if !slices.Equal(gotSel.FactoryIDs, []string{"fac_1"}) || !slices.Equal(gotSel.ProjectIDs, []string{"proj_1", "proj_2"}) {
		t.Errorf("round-trip mismatch: %+v", gotSel)
	}
}
