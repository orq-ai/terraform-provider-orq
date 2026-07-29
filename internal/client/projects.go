package client

import (
	"context"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
)

// Project is the transport-agnostic projection of a platform project. The
// resource layer sees this, not the connect-go generated type.
type Project struct {
	ID          string
	Name        string
	Key         string
	Description string
	IsArchived  bool
	IsDefault   bool
	Teams       []string
	CreatedAt   string
	UpdatedAt   string
}

// ProjectPage is one page of a cursor-paginated list.
type ProjectPage struct {
	Projects []Project
	HasMore  bool
}

// ListParams are the shared cursor-pagination inputs. The {object,data,has_more}
// + cursor envelope is identical across all Connect list calls, so this type is
// reused as more domains are added.
type ListParams struct {
	Limit         int32
	StartingAfter string
}

// ProjectCreateInput carries the mutable fields for a project create. A nil
// Description omits the field (server keeps its default); Teams is sent as-is.
type ProjectCreateInput struct {
	Name        string
	Description *string
	Teams       []string
}

// ProjectUpdateInput is a sparse patch: a nil pointer leaves the field
// unchanged. Teams is always sent (an empty slice clears the associations, per
// the proto contract).
type ProjectUpdateInput struct {
	ID          string
	Name        *string
	Description *string
	Teams       []string
}

// ProjectsAPI is the per-resource seam for the projects domain (Connect-backed).
type ProjectsAPI interface {
	List(ctx context.Context, params ListParams) (*ProjectPage, error)
	Get(ctx context.Context, id string) (*Project, error)
	Create(ctx context.Context, in ProjectCreateInput) (*Project, error)
	Update(ctx context.Context, in ProjectUpdateInput) (*Project, error)
	Delete(ctx context.Context, id string) error
}

// connectProjects implements ProjectsAPI over the Connect ProjectsService.
type connectProjects struct {
	c platformv1connect.ProjectsServiceClient
}

func projectFromProto(pr *platformv1.Project) Project {
	p := Project{
		ID:          pr.GetProjectId(),
		Name:        pr.GetName(),
		Key:         pr.GetKey(),
		Description: pr.GetDescription(),
		IsArchived:  pr.GetIsArchived(),
		IsDefault:   pr.GetIsDefault(),
		Teams:       pr.GetTeams(),
		CreatedAt:   formatTimestamp(pr.GetCreatedAt()),
		UpdatedAt:   formatTimestamp(pr.GetUpdatedAt()),
	}
	return p
}

func (p *connectProjects) List(ctx context.Context, params ListParams) (*ProjectPage, error) {
	req := &platformv1.ListProjectsRequest{}
	if params.Limit > 0 {
		req.Limit = &params.Limit
	}
	if params.StartingAfter != "" {
		req.StartingAfter = params.StartingAfter
	}

	resp, err := p.c.ListProjects(ctx, req)
	if err != nil {
		return nil, mapConnectError("project", err)
	}

	out := &ProjectPage{HasMore: resp.GetHasMore()}
	for _, pr := range resp.GetData() {
		out.Projects = append(out.Projects, projectFromProto(pr))
	}
	return out, nil
}

func (p *connectProjects) Get(ctx context.Context, id string) (*Project, error) {
	resp, err := p.c.GetProject(ctx, &platformv1.GetProjectRequest{ProjectId: id})
	if err != nil {
		return nil, mapConnectError("project", err)
	}
	pr := projectFromProto(resp.GetProject())
	return &pr, nil
}

func (p *connectProjects) Create(ctx context.Context, in ProjectCreateInput) (*Project, error) {
	req := &platformv1.CreateProjectRequest{Name: in.Name, Teams: in.Teams}
	if in.Description != nil {
		req.Description = in.Description
	}
	resp, err := p.c.CreateProject(ctx, req)
	if err != nil {
		return nil, mapConnectError("project", err)
	}
	pr := projectFromProto(resp.GetProject())
	return &pr, nil
}

func (p *connectProjects) Update(ctx context.Context, in ProjectUpdateInput) (*Project, error) {
	req := &platformv1.UpdateProjectRequest{ProjectId: in.ID, Teams: in.Teams}
	if in.Name != nil {
		req.Name = in.Name
	}
	if in.Description != nil {
		req.Description = in.Description
	}
	resp, err := p.c.UpdateProject(ctx, req)
	if err != nil {
		return nil, mapConnectError("project", err)
	}
	pr := projectFromProto(resp.GetProject())
	return &pr, nil
}

func (p *connectProjects) Delete(ctx context.Context, id string) error {
	_, err := p.c.DeleteProject(ctx, &platformv1.DeleteProjectRequest{ProjectId: id})
	return mapConnectError("project", err)
}
