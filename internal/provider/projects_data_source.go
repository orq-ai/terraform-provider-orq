package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var (
	_ datasource.DataSource              = &projectsDataSource{}
	_ datasource.DataSourceWithConfigure = &projectsDataSource{}
)

// NewProjectsDataSource is the factory registered on the provider.
func NewProjectsDataSource() datasource.DataSource {
	return &projectsDataSource{}
}

// projectLister is the read-only slice of the projects domain this data source
// needs. Depending on List alone (not the full client.ProjectsAPI, which also
// carries the write methods the resource uses) keeps the data source's surface
// minimal and its fakes tiny.
type projectLister interface {
	List(ctx context.Context, params client.ListParams) (*client.ProjectPage, error)
}

// projectsDataSource is a minimal read-only data source that lists projects. It
// is the end-to-end smoke test of the client interface + auth transport. It
// holds ONLY its narrow domain interface, never the concrete *client.Client, so
// the resource layer stays transport-agnostic.
type projectsDataSource struct {
	projects projectLister
}

// projectsPageLimit is the per-request page size for the paginated list. The
// Connect list envelope caps limit at 200; the server defaults to 25 when
// unset, so we must page explicitly to see every project.
const projectsPageLimit = 200

type projectsDataSourceModel struct {
	Projects []projectModel `tfsdk:"projects"`
}

type projectModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Key         types.String `tfsdk:"key"`
	Description types.String `tfsdk:"description"`
	IsArchived  types.Bool   `tfsdk:"is_archived"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
}

func (d *projectsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_projects"
}

func (d *projectsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists projects in the workspace bound to the provider credential.",
		Attributes: map[string]schema.Attribute{
			"projects": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Projects visible to the credential, newest first.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Project ID."},
						"name":        schema.StringAttribute{Computed: true, MarkdownDescription: "Project name."},
						"key":         schema.StringAttribute{Computed: true, MarkdownDescription: "Stable project key."},
						"description": schema.StringAttribute{Computed: true, MarkdownDescription: "Project description."},
						"is_archived": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the project is archived."},
						"is_default":  schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether this is the workspace default project."},
					},
				},
			},
		},
	}
}

func (d *projectsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // provider not yet configured
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	// Hold only the narrow domain interface, not the concrete client.
	d.projects = c.Projects()
}

func (d *projectsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	// A non-nil empty slice maps to an empty TF list, not null, so a workspace
	// with zero projects produces `projects = []` rather than a null attribute.
	state := projectsDataSourceModel{Projects: []projectModel{}}

	cursor := ""
	for {
		page, err := d.projects.List(ctx, client.ListParams{Limit: projectsPageLimit, StartingAfter: cursor})
		if err != nil {
			resp.Diagnostics.AddError("Unable to list projects",
				"Listing projects failed. Verify that ORQ_TOKEN is a valid management key and "+
					"ORQ_URL is reachable.\n\nerror ["+string(client.CodeOf(err))+"]: "+err.Error())
			return
		}

		for _, p := range page.Projects {
			state.Projects = append(state.Projects, projectModel{
				ID:          types.StringValue(p.ID),
				Name:        types.StringValue(p.Name),
				Key:         types.StringValue(p.Key),
				Description: types.StringValue(p.Description),
				IsArchived:  types.BoolValue(p.IsArchived),
				IsDefault:   types.BoolValue(p.IsDefault),
			})
		}

		if !page.HasMore {
			break
		}
		// has_more=true with an empty page would loop forever — treat as a
		// server contract violation rather than spinning.
		if len(page.Projects) == 0 {
			resp.Diagnostics.AddError("Projects pagination error",
				"The server reported more projects but returned an empty page. Aborting to avoid an infinite loop.")
			return
		}
		next := page.Projects[len(page.Projects)-1].ID
		// A missing or non-advancing cursor would also loop forever.
		if next == "" || next == cursor {
			resp.Diagnostics.AddError("Projects pagination error",
				"The pagination cursor did not advance between pages. Aborting to avoid an infinite loop.")
			return
		}
		cursor = next
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
