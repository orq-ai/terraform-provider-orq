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

// ProjectsAPI is the per-resource seam for the projects domain.
type ProjectsAPI interface {
	List(ctx context.Context, params ListParams) (*ProjectPage, error)
}

// connectProjects implements ProjectsAPI over the Connect ProjectsService.
type connectProjects struct {
	c platformv1connect.ProjectsServiceClient
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
		return nil, err
	}

	out := &ProjectPage{HasMore: resp.GetHasMore()}
	for _, pr := range resp.GetData() {
		out.Projects = append(out.Projects, Project{
			ID:          pr.GetProjectId(),
			Name:        pr.GetName(),
			Key:         pr.GetKey(),
			Description: pr.GetDescription(),
			IsArchived:  pr.GetIsArchived(),
			IsDefault:   pr.GetIsDefault(),
		})
	}
	return out, nil
}
