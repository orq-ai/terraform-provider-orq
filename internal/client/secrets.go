package client

import (
	"context"
	"fmt"
	"strings"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

const (
	SecretKindVariable    = "VARIABLE"
	SecretKindSecret      = "SECRET"
	SecretKindSetupSecret = "SETUP_SECRET"

	secretKindProtoPrefix = "SECRET_KIND_"
)

// The value is write-only on the server, so Secret never carries it.
type Secret struct {
	ID          string
	VaultID     string
	Name        string
	Kind        string
	Description string
	CreatedAt   string
	UpdatedAt   string
}

type SecretCreateInput struct {
	VaultID     string
	Name        string
	Kind        string
	Value       string
	Description string
}

// Nil fields are left unchanged; a non-nil Value rotates the stored value.
type SecretUpdateInput struct {
	VaultID     string
	ID          string
	Kind        *string
	Value       *string
	Description *string
}

type SecretsAPI interface {
	Get(ctx context.Context, vaultID, id string) (*Secret, error)
	Create(ctx context.Context, in SecretCreateInput) (*Secret, error)
	Update(ctx context.Context, in SecretUpdateInput) (*Secret, error)
	Delete(ctx context.Context, vaultID, id string) error
}

type connectSecrets struct {
	c platformv1connect.SecretsServiceClient
}

func secretKindToProto(short string) (platformv1.SecretKind, error) {
	value, ok := platformv1.SecretKind_value[secretKindProtoPrefix+strings.ToUpper(short)]
	if !ok || value == int32(platformv1.SecretKind_SECRET_KIND_UNSPECIFIED) {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown secret kind %q", short)}
	}
	return platformv1.SecretKind(value), nil
}

func secretKindFromProto(kind platformv1.SecretKind) string {
	if kind == platformv1.SecretKind_SECRET_KIND_UNSPECIFIED {
		return ""
	}
	return strings.TrimPrefix(kind.String(), secretKindProtoPrefix)
}

func secretFromProto(secret *platformv1.Secret) *Secret {
	return &Secret{
		ID:          secret.GetId(),
		VaultID:     secret.GetVaultId(),
		Name:        secret.GetName(),
		Kind:        secretKindFromProto(secret.GetKind()),
		Description: secret.GetDescription(),
		CreatedAt:   formatTimestamp(secret.GetCreated()),
		UpdatedAt:   formatTimestamp(secret.GetUpdated()),
	}
}

func (c *connectSecrets) Get(ctx context.Context, vaultID, id string) (*Secret, error) {
	resp, err := c.c.GetSecret(ctx, &platformv1.GetSecretRequest{VaultId: vaultID, SecretId: id})
	if err != nil {
		return nil, mapConnectError("secret", err)
	}
	return secretFromProto(resp.GetSecret()), nil
}

func (c *connectSecrets) Create(ctx context.Context, in SecretCreateInput) (*Secret, error) {
	kind, err := secretKindToProto(in.Kind)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.CreateSecret(ctx, &platformv1.CreateSecretRequest{
		VaultId:     in.VaultID,
		Name:        in.Name,
		Kind:        kind,
		Value:       in.Value,
		Description: in.Description,
	})
	if err != nil {
		return nil, mapConnectError("secret", err)
	}
	return secretFromProto(resp.GetSecret()), nil
}

func (c *connectSecrets) Update(ctx context.Context, in SecretUpdateInput) (*Secret, error) {
	req := &platformv1.UpdateSecretRequest{
		VaultId:     in.VaultID,
		SecretId:    in.ID,
		Value:       in.Value,
		Description: in.Description,
	}
	if in.Kind != nil {
		kind, err := secretKindToProto(*in.Kind)
		if err != nil {
			return nil, err
		}
		req.Kind = &kind
	}
	resp, err := c.c.UpdateSecret(ctx, req)
	if err != nil {
		return nil, mapConnectError("secret", err)
	}
	return secretFromProto(resp.GetSecret()), nil
}

func (c *connectSecrets) Delete(ctx context.Context, vaultID, id string) error {
	_, err := c.c.DeleteSecret(ctx, &platformv1.DeleteSecretRequest{VaultId: vaultID, SecretId: id})
	return mapConnectError("secret", err)
}
