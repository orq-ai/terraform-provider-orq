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

// projectsDataSource is a minimal read-only data source that lists projects via
// the Connect ProjectsService. It is the end-to-end smoke test of the client
// interface + auth + Connect transport.
type projectsDataSource struct {
	client *client.Client
}

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
	d.client = c
}

func (d *projectsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	page, err := d.client.Projects().List(ctx, client.ListParams{})
	if err != nil {
		resp.Diagnostics.AddError("Unable to list projects",
			"Connect ProjectsService/ListProjects failed. Verify that ORQ_TOKEN is a valid "+
				"management key and ORQ_URL is reachable.\n\n"+err.Error())
		return
	}

	state := projectsDataSourceModel{}
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

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
