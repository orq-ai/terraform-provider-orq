package client

import (
	"context"
	"net/http"

	platformv1 "github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1"
	"github.com/orq-ai/terraform-provider-orq/internal/gen/orq/platform/v1/platformv1connect"
	"github.com/orq-ai/terraform-provider-orq/internal/restgen"
)

// Sharing modes, matching libs/go/sharing (the mongo/JSON representation of
// orq.platform.v1.Sharing).
const (
	SharingModeAllProjects = "all_projects"
	SharingModeSelected    = "selected"
)

// WorkspaceModel is the transport-agnostic projection of an enabled workspace
// model plus its sharing config. Enabled is false when the model reference is
// not present in the workspace catalog (read paths use this to detect drift /
// out-of-band disable).
type WorkspaceModel struct {
	ModelID     string
	DisplayName string
	Enabled     bool
	Sharing     *SharingConfig // nil when no sharing row is set yet
}

// SharingConfig is the normalized read-back of a model's sharing. For
// SharingModeSelected, ProjectIDs is always non-nil (empty slice when the model
// is shared with no project) so the resource never sees a null vs [] ambiguity.
type SharingConfig struct {
	Mode                 string
	ProjectIDs           []string
	AllowVersionPin      bool
	AllowFork            bool
	AutoGrantNewProjects bool
}

// SharingInput is the write shape. Exactly one of AllProjects / (Mode selected
// via ProjectIDs) is expressed by AllProjects: when AllProjects is false the
// selected mode is written with ProjectIDs (which may be empty).
type SharingInput struct {
	AllProjects          bool
	ProjectIDs           []string
	AllowVersionPin      bool
	AllowFork            bool
	AutoGrantNewProjects bool
}

// WorkspaceModelsAPI is the per-resource seam for workspace models. Enable and
// disable ride REST (/v2/workspace-models); sharing writes ride the Connect
// ModelSharingService; sharing reads come inline from GET /v2/models.
type WorkspaceModelsAPI interface {
	Enable(ctx context.Context, modelID string) error
	Disable(ctx context.Context, modelID string) error
	// Get returns the enabled model (with sharing) or nil when the reference
	// is not enabled in the workspace catalog.
	Get(ctx context.Context, modelID string) (*WorkspaceModel, error)
	SetSharing(ctx context.Context, modelID string, in SharingInput) error
}

// workspaceModels mixes transports: REST for enable/disable/read, Connect for
// the sharing write.
type workspaceModels struct {
	rest    *restgen.ClientWithResponses
	sharing platformv1connect.ModelSharingServiceClient
}

func (w *workspaceModels) Enable(ctx context.Context, modelID string) error {
	resp, err := w.rest.ModelEnableWithResponse(ctx, restgen.ModelEnableJSONRequestBody{ModelId: modelID})
	if err != nil {
		return mapRESTTransportError(err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}

func (w *workspaceModels) Disable(ctx context.Context, modelID string) error {
	resp, err := w.rest.ModelDisableWithResponse(ctx, modelID)
	if err != nil {
		return mapRESTTransportError(err)
	}
	switch resp.StatusCode() {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted:
		return nil
	default:
		return mapRESTStatus(resp.StatusCode(), resp.Body)
	}
}

func (w *workspaceModels) Get(ctx context.Context, modelID string) (*WorkspaceModel, error) {
	resp, err := w.rest.ModelListWithResponse(ctx)
	if err != nil {
		return nil, mapRESTTransportError(err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, mapRESTStatus(resp.StatusCode(), resp.Body)
	}
	for _, m := range *resp.JSON200 {
		if m.Id != modelID {
			continue
		}
		wm := &WorkspaceModel{
			ModelID:     m.Id,
			DisplayName: m.DisplayName,
			Enabled:     m.Enabled,
			Sharing:     sharingFromREST(m.Sharing),
		}
		return wm, nil
	}
	// Reference not present in the catalog at all → treat as not enabled.
	return nil, nil
}

// sharingFromREST normalizes the inline ModelSharingConfig. It resolves the
// selected/empty-vs-null quirk: mode == selected with an absent project_ids
// list becomes an empty (non-nil) slice.
func sharingFromREST(c *restgen.ModelSharingConfig) *SharingConfig {
	if c == nil {
		return nil
	}
	out := &SharingConfig{
		Mode:                 c.Mode,
		AllowVersionPin:      c.AllowVersionPin,
		AllowFork:            c.AllowFork,
		AutoGrantNewProjects: c.AutoGrantNewProjects,
	}
	if c.Mode == SharingModeSelected {
		out.ProjectIDs = []string{}
		if c.ProjectIds != nil {
			out.ProjectIDs = append(out.ProjectIDs, *c.ProjectIds...)
		}
	}
	return out
}

func (w *workspaceModels) SetSharing(ctx context.Context, modelID string, in SharingInput) error {
	sh := &platformv1.Sharing{
		AllowVersionPin:      in.AllowVersionPin,
		AllowFork:            in.AllowFork,
		AutoGrantNewProjects: in.AutoGrantNewProjects,
	}
	if in.AllProjects {
		sh.Mode = &platformv1.Sharing_AllProjects{AllProjects: &platformv1.SharingAllProjects{}}
	} else {
		sh.Mode = &platformv1.Sharing_Selected{Selected: &platformv1.SharingSelectedProjects{ProjectIds: in.ProjectIDs}}
	}
	_, err := w.sharing.SetModelSharing(ctx, &platformv1.SetModelSharingRequest{ModelId: modelID, Sharing: sh})
	return mapConnectError(err)
}
