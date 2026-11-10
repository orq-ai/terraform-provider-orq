package client

import (
	"context"
	"fmt"
	"strings"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

const (
	AuditLogDestinationStatusActive = "ACTIVE"
	AuditLogDestinationStatusPaused = "PAUSED"

	auditLogDestinationStatusPrefix = "AUDIT_LOG_DESTINATION_STATUS_"
)

type AuditLogDestination struct {
	ID                string
	DisplayName       string
	Description       string
	URL               string
	Status            string
	SigningSecretHint string
	SecretRotatedAt   string
	CreatedAt         string
	UpdatedAt         string
}

type AuditLogDestinationCreateInput struct {
	DisplayName string
	Description string
	URL         string
}

type AuditLogDestinationUpdateInput struct {
	ID          string
	DisplayName *string
	Description *string
	URL         *string
	Status      *string
}

type AuditLogDestinationsAPI interface {
	Get(ctx context.Context, id string) (*AuditLogDestination, error)
	Create(ctx context.Context, in AuditLogDestinationCreateInput) (destination *AuditLogDestination, signingSecret string, err error)
	Update(ctx context.Context, in AuditLogDestinationUpdateInput) (*AuditLogDestination, error)
	Delete(ctx context.Context, id string) error
}

type connectAuditLogDestinations struct {
	c platformv1connect.AuditLogDestinationsServiceClient
}

func auditLogDestinationStatusToProto(short string) (platformv1.AuditLogDestinationStatus, error) {
	v, ok := platformv1.AuditLogDestinationStatus_value[auditLogDestinationStatusPrefix+short]
	if !ok || v == 0 {
		return 0, &Error{Code: CodeInvalid, Message: fmt.Sprintf("unknown audit log destination status %q", short)}
	}
	return platformv1.AuditLogDestinationStatus(v), nil
}

func auditLogDestinationFromProto(d *platformv1.AuditLogDestination) *AuditLogDestination {
	return &AuditLogDestination{
		ID:                d.GetId(),
		DisplayName:       d.GetDisplayName(),
		Description:       d.GetDescription(),
		URL:               d.GetUrl(),
		Status:            strings.TrimPrefix(d.GetStatus().String(), auditLogDestinationStatusPrefix),
		SigningSecretHint: d.GetSigningSecretHint(),
		SecretRotatedAt:   formatTimestamp(d.GetSecretRotatedAt()),
		CreatedAt:         formatTimestamp(d.GetCreatedAt()),
		UpdatedAt:         formatTimestamp(d.GetUpdatedAt()),
	}
}

func (c *connectAuditLogDestinations) Get(ctx context.Context, id string) (*AuditLogDestination, error) {
	resp, err := c.c.GetAuditLogDestination(ctx, &platformv1.GetAuditLogDestinationRequest{DestinationId: id})
	if err != nil {
		return nil, mapConnectError("audit log destination", err)
	}
	return auditLogDestinationFromProto(resp.GetDestination()), nil
}

func (c *connectAuditLogDestinations) Create(ctx context.Context, in AuditLogDestinationCreateInput) (*AuditLogDestination, string, error) {
	resp, err := c.c.CreateAuditLogDestination(ctx, &platformv1.CreateAuditLogDestinationRequest{
		DisplayName: in.DisplayName,
		Description: in.Description,
		Url:         in.URL,
	})
	if err != nil {
		return nil, "", mapConnectError("audit log destination", err)
	}
	return auditLogDestinationFromProto(resp.GetDestination()), resp.GetSigningSecret(), nil
}

func (c *connectAuditLogDestinations) Update(ctx context.Context, in AuditLogDestinationUpdateInput) (*AuditLogDestination, error) {
	req := &platformv1.UpdateAuditLogDestinationRequest{
		DestinationId: in.ID,
		DisplayName:   in.DisplayName,
		Description:   in.Description,
		Url:           in.URL,
	}
	if in.Status != nil {
		status, err := auditLogDestinationStatusToProto(*in.Status)
		if err != nil {
			return nil, err
		}
		req.Status = &status
	}
	resp, err := c.c.UpdateAuditLogDestination(ctx, req)
	if err != nil {
		return nil, mapConnectError("audit log destination", err)
	}
	return auditLogDestinationFromProto(resp.GetDestination()), nil
}

func (c *connectAuditLogDestinations) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteAuditLogDestination(ctx, &platformv1.DeleteAuditLogDestinationRequest{DestinationId: id})
	return mapConnectError("audit log destination", err)
}
