package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &VariableEntryPatchResource{}

func NewVariableEntryPatchResource() resource.Resource {
	return &VariableEntryPatchResource{}
}

type VariableEntryPatchResource struct {
	client *ProviderClient
}

type VariableEntryPatchResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Warehouse types.String `tfsdk:"warehouse"`
	SetID     types.String `tfsdk:"set_id"`
	Name      types.String `tfsdk:"name"`
	PatchJSON types.String `tfsdk:"patch_json"`
	Triggers  types.Map    `tfsdk:"triggers"`
	Version   types.Int64  `tfsdk:"version"`
}

func (r *VariableEntryPatchResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_variable_entry_patch"
}

func (r *VariableEntryPatchResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Applies an RFC 7396 JSON merge-patch at apply time to an existing JSON-valued Unco variable entry.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"warehouse": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"set_id": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"patch_json": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "RFC 7396 JSON merge-patch object to merge into the existing variable entry value.",
			},
			"triggers": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Arbitrary key/value pairs that re-apply the patch when changed.",
			},
			"version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Resulting variable set version after applying the patch.",
			},
		},
	}
}

func (r *VariableEntryPatchResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pc, ok := req.ProviderData.(*ProviderClient)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Resource Configure Type", fmt.Sprintf("Expected *ProviderClient, got %T", req.ProviderData))
		return
	}
	r.client = pc
}

func (r *VariableEntryPatchResource) applyPatch(ctx context.Context, data *VariableEntryPatchResourceModel) error {
	wh := data.Warehouse.ValueString()
	setID := data.SetID.ValueString()
	entryName := data.Name.ValueString()
	patchBytes := []byte(data.PatchJSON.ValueString())

	version, err := r.client.MutateSet(ctx, wh, setID, func(set *VariableSet, exists bool) (bool, error) {
		if !exists {
			return false, fmt.Errorf("variable set %s/%s not found", wh, setID)
		}
		cur, ok := set.Variables[entryName]
		if !ok {
			return false, fmt.Errorf("variable entry %s/%s/%s not found", wh, setID, entryName)
		}
		merged, changed, err := applyMergePatch(cur.Value, patchBytes)
		if err != nil {
			return false, err
		}
		if !changed {
			return false, nil
		}
		cur.Value = merged
		set.Variables[entryName] = cur
		return true, nil
	})
	if err != nil {
		return err
	}

	data.ID = types.StringValue(fmt.Sprintf("%s/%s/%s", wh, setID, entryName))
	data.Version = types.Int64Value(version)
	return nil
}

func (r *VariableEntryPatchResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryPatchResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.applyPatch(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to patch Unco variable entry", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryPatchResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VariableEntryPatchResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryPatchResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryPatchResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.applyPatch(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to patch Unco variable entry", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryPatchResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}
