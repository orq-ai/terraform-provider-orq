package client

import (
	"context"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// Environment is owned by exactly one scope: ProjectID is set for a
// project-owned environment, Sharing for a workspace-owned one.
type Environment struct {
	ID          string
	Slug        string
	DisplayName string
	Description string
	ProjectID   string
	Sharing     *SharingConfig
	CreatedAt   string
	UpdatedAt   string
}

type EnvironmentCreateInput struct {
	Slug        string
	DisplayName string
	Description string
	ProjectID   string
	Sharing     *SharingInput
}

type EnvironmentUpdateInput struct {
	ID          string
	DisplayName *string
	Description *string
	Sharing     *SharingInput
}

type EnvironmentsAPI interface {
	Get(ctx context.Context, id string) (*Environment, error)
	Create(ctx context.Context, in EnvironmentCreateInput) (*Environment, error)
	Update(ctx context.Context, in EnvironmentUpdateInput) (*Environment, error)
	Delete(ctx context.Context, id string) error
}

type connectEnvironments struct {
	c platformv1connect.EnvironmentsServiceClient
}

func sharingFromProto(s *platformv1.Sharing) *SharingConfig {
	if s == nil {
		return nil
	}
	out := &SharingConfig{
		Mode:                 SharingModeAllProjects,
		AllowVersionPin:      s.GetAllowVersionPin(),
		AllowFork:            s.GetAllowFork(),
		AutoGrantNewProjects: s.GetAutoGrantNewProjects(),
	}
	if s.GetAllProjects() == nil {
		out.Mode = SharingModeSelected
		out.ProjectIDs = append([]string{}, s.GetSelected().GetProjectIds()...)
	}
	return out
}

func environmentFromProto(e *platformv1.Environment) *Environment {
	return &Environment{
		ID:          e.GetId(),
		Slug:        e.GetSlug(),
		DisplayName: e.GetDisplayName(),
		Description: e.GetDescription(),
		ProjectID:   e.GetProjectId(),
		Sharing:     sharingFromProto(e.GetSharing()),
		CreatedAt:   formatTimestamp(e.GetCreatedAt()),
		UpdatedAt:   formatTimestamp(e.GetUpdatedAt()),
	}
}

func (c *connectEnvironments) Get(ctx context.Context, id string) (*Environment, error) {
	resp, err := c.c.GetEnvironment(ctx, &platformv1.GetEnvironmentRequest{EnvironmentId: id})
	if err != nil {
		return nil, mapConnectError("environment", err)
	}
	return environmentFromProto(resp.GetEnvironment()), nil
}

func (c *connectEnvironments) Create(ctx context.Context, in EnvironmentCreateInput) (*Environment, error) {
	req := &platformv1.CreateEnvironmentRequest{
		Slug:        in.Slug,
		DisplayName: in.DisplayName,
		Description: in.Description,
	}
	if in.Sharing != nil {
		req.Scope = &platformv1.CreateEnvironmentRequest_Sharing{Sharing: sharingToProto(*in.Sharing)}
	} else {
		req.Scope = &platformv1.CreateEnvironmentRequest_ProjectId{ProjectId: in.ProjectID}
	}
	resp, err := c.c.CreateEnvironment(ctx, req)
	if err != nil {
		return nil, mapConnectError("environment", err)
	}
	return environmentFromProto(resp.GetEnvironment()), nil
}

func (c *connectEnvironments) Update(ctx context.Context, in EnvironmentUpdateInput) (*Environment, error) {
	req := &platformv1.UpdateEnvironmentRequest{
		EnvironmentId: in.ID,
		DisplayName:   in.DisplayName,
		Description:   in.Description,
	}
	if in.Sharing != nil {
		req.Sharing = sharingToProto(*in.Sharing)
	}
	resp, err := c.c.UpdateEnvironment(ctx, req)
	if err != nil {
		return nil, mapConnectError("environment", err)
	}
	return environmentFromProto(resp.GetEnvironment()), nil
}

func (c *connectEnvironments) Delete(ctx context.Context, id string) error {
	_, err := c.c.DeleteEnvironment(ctx, &platformv1.DeleteEnvironmentRequest{EnvironmentId: id})
	return mapConnectError("environment", err)
}
