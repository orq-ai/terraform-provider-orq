package client

import (
	"context"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

type VaultSharing struct {
	AllFactories bool
	FactoryIDs   []string
}

// Sharing is nil for a factory-owned vault.
type Vault struct {
	ID          string
	DisplayName string
	Description string
	FactoryID   string
	Sharing     *VaultSharing
	CreatedAt   string
	UpdatedAt   string
}

// A nil Sharing with an empty FactoryID creates a workspace vault shared with all factories.
type VaultCreateInput struct {
	DisplayName string
	Description string
	FactoryID   string
	Sharing     *VaultSharing
}

// Nil fields are left unchanged; Sharing must stay nil for a factory-owned vault.
type VaultUpdateInput struct {
	ID          string
	DisplayName *string
	Description *string
	Sharing     *VaultSharing
}

type VaultsAPI interface {
	Get(ctx context.Context, id string) (*Vault, error)
	Create(ctx context.Context, in VaultCreateInput) (*Vault, error)
	Update(ctx context.Context, in VaultUpdateInput) (*Vault, error)
	Delete(ctx context.Context, id string) error
}

type connectVaults struct {
	c platformv1connect.VaultsServiceClient
}

func vaultSharingToProto(sharing *VaultSharing) *platformv1.VaultSharing {
	if sharing == nil {
		return nil
	}
	if sharing.AllFactories {
		return &platformv1.VaultSharing{Mode: &platformv1.VaultSharing_AllFactories{AllFactories: &platformv1.VaultSharingAllFactories{}}}
	}
	return &platformv1.VaultSharing{Mode: &platformv1.VaultSharing_Selected{
		Selected: &platformv1.VaultSharingSelectedFactories{FactoryIds: sharing.FactoryIDs},
	}}
}

func vaultSharingFromProto(vault *platformv1.Vault) *VaultSharing {
	if vault.GetFactoryId() != "" {
		return nil
	}
	if vault.GetSharing().GetAllFactories() != nil {
		return &VaultSharing{AllFactories: true}
	}
	return &VaultSharing{FactoryIDs: vault.GetSharing().GetSelected().GetFactoryIds()}
}

func vaultFromProto(vault *platformv1.Vault) *Vault {
	return &Vault{
		ID:          vault.GetId(),
		DisplayName: vault.GetDisplayName(),
		Description: vault.GetDescription(),
		FactoryID:   vault.GetFactoryId(),
		Sharing:     vaultSharingFromProto(vault),
		CreatedAt:   formatTimestamp(vault.GetCreated()),
		UpdatedAt:   formatTimestamp(vault.GetUpdated()),
	}
}

func (c *connectVaults) Get(ctx context.Context, id string) (*Vault, error) {
	resp, err := c.c.GetVault(ctx, &platformv1.GetVaultRequest{VaultId: id})
	if err != nil {
		return nil, mapConnectError("vault", err)
	}
	return vaultFromProto(resp.GetVault()), nil
}

func (c *connectVaults) Create(ctx context.Context, in VaultCreateInput) (*Vault, error) {
	req := &platformv1.CreateVaultRequest{DisplayName: in.DisplayName, Description: in.Description}
	switch {
	case in.FactoryID != "":
		req.Scope = &platformv1.CreateVaultRequest_FactoryId{FactoryId: in.FactoryID}
	case in.Sharing != nil:
		req.Scope = &platformv1.CreateVaultRequest_Sharing{Sharing: vaultSharingToProto(in.Sharing)}
	}
	resp, err := c.c.CreateVault(ctx, req)
	if err != nil {
		return nil, mapConnectError("vault", err)
	}
	return vaultFromProto(resp.GetVault()), nil
}

func (c *connectVaults) Update(ctx context.Context, in VaultUpdateInput) (*Vault, error) {
	resp, err := c.c.UpdateVault(ctx, &platformv1.UpdateVaultRequest{
		VaultId:     in.ID,
		DisplayName: in.DisplayName,
		Description: in.Description,
		Sharing:     vaultSharingToProto(in.Sharing),
	})
	if err != nil {
		return nil, mapConnectError("vault", err)
	}
	return vaultFromProto(resp.GetVault()), nil
}

func (c *connectVaults) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteVault(ctx, &platformv1.DeleteVaultRequest{VaultId: id})
	return mapConnectError("vault", err)
}
