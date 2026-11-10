package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/boolvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/orq-ai/terraform-provider-orq/internal/client"
)

var mcpExposureModes = []string{client.McpExposureModeAll, client.McpExposureModeSelected, client.McpExposureModeNone}

type mcpSharingModel struct {
	AllProjects types.Bool `tfsdk:"all_projects"`
	ProjectIDs  types.List `tfsdk:"project_ids"`
}

type mcpToolExposureModel struct {
	Mode            types.String `tfsdk:"mode"`
	ReadOnly        types.Bool   `tfsdk:"read_only"`
	ToolIDs         types.List   `tfsdk:"tool_ids"`
	ExcludedToolIDs types.List   `tfsdk:"excluded_tool_ids"`
}

func mcpSharingAttribute(entity string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: "Which projects may use this " + entity + ". Exactly one of `all_projects` or " +
			"`project_ids` must be set. Omitted shares with every project.",
		Default: objectdefault.StaticValue(types.ObjectValueMust(
			map[string]attr.Type{
				"all_projects": types.BoolType,
				"project_ids":  types.ListType{ElemType: types.StringType},
			},
			map[string]attr.Value{
				"all_projects": types.BoolValue(true),
				"project_ids":  types.ListNull(types.StringType),
			},
		)),
		Attributes: map[string]schema.Attribute{
			"all_projects": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Share with every project in the workspace. Only accepts `true`. " +
					"Mutually exclusive with `project_ids`.",
				Validators: []validator.Bool{
					boolvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("project_ids")),
					allProjectsTrueValidator{},
				},
			},
			"project_ids": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "Share with exactly these projects; an empty list shares with no project. " +
					"Mutually exclusive with `all_projects`.",
				Validators: []validator.List{uniqueStringsValidator{
					summary:         invalidSharingSummary,
					duplicateDetail: "project_ids must not repeat a project id.",
					nullDetail:      invalidNullProjectIDDetail,
				}},
			},
		},
	}
}

func (s *mcpSharingModel) toClient(ctx context.Context) (*client.McpSharing, diag.Diagnostics) {
	if s == nil {
		return nil, nil
	}
	if s.AllProjects.ValueBool() {
		return &client.McpSharing{AllProjects: true}, nil
	}
	projectIDs, diags := stringSlice(ctx, s.ProjectIDs)
	return &client.McpSharing{ProjectIDs: projectIDs}, diags
}

func mcpSharingModelFromClient(prior *mcpSharingModel, sharing *client.McpSharing) *mcpSharingModel {
	if sharing == nil || sharing.AllProjects {
		return &mcpSharingModel{AllProjects: types.BoolValue(true), ProjectIDs: types.ListNull(types.StringType)}
	}
	priorIDs := types.ListNull(types.StringType)
	if prior != nil {
		priorIDs = prior.ProjectIDs
	}
	return &mcpSharingModel{AllProjects: types.BoolNull(), ProjectIDs: preserveListOrder(priorIDs, sharing.ProjectIDs)}
}

func mcpToolExposureAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"mode": schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Selection strategy: `ALL`, `SELECTED` or `NONE`.",
			Validators:          []validator.String{stringvalidator.OneOf(mcpExposureModes...)},
		},
		"read_only": schema.BoolAttribute{
			Optional:            true,
			Computed:            true,
			Default:             booldefault.StaticBool(false),
			MarkdownDescription: "Keep only the tools the upstream annotates as read-only. Defaults to `false`.",
		},
		"tool_ids": schema.ListAttribute{
			Optional:    true,
			ElementType: types.StringType,
			MarkdownDescription: "Tool ids to expose when `mode` is `SELECTED`. Ids are assigned by orq when the " +
				"server's tools are synced.",
		},
		"excluded_tool_ids": schema.ListAttribute{
			Optional:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Tool ids deliberately left unexposed when `mode` is `SELECTED`.",
		},
	}
}

func (e *mcpToolExposureModel) toClient(ctx context.Context) (client.McpToolExposure, diag.Diagnostics) {
	toolIDs, diags := stringSlice(ctx, e.ToolIDs)
	excludedToolIDs, excludedDiags := stringSlice(ctx, e.ExcludedToolIDs)
	diags.Append(excludedDiags...)
	return client.McpToolExposure{
		Mode:            e.Mode.ValueString(),
		ReadOnly:        e.ReadOnly.ValueBool(),
		ToolIDs:         toolIDs,
		ExcludedToolIDs: excludedToolIDs,
	}, diags
}

func mcpToolExposureModelFromClient(prior *mcpToolExposureModel, exposure client.McpToolExposure) *mcpToolExposureModel {
	if prior == nil {
		prior = &mcpToolExposureModel{
			ToolIDs:         types.ListNull(types.StringType),
			ExcludedToolIDs: types.ListNull(types.StringType),
		}
	}
	return &mcpToolExposureModel{
		Mode:            types.StringValue(exposure.Mode),
		ReadOnly:        types.BoolValue(exposure.ReadOnly),
		ToolIDs:         preserveEmptyList(prior.ToolIDs, exposure.ToolIDs),
		ExcludedToolIDs: preserveEmptyList(prior.ExcludedToolIDs, exposure.ExcludedToolIDs),
	}
}
