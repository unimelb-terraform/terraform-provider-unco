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

var _ resource.Resource = &VariableEntryDeleteResource{}

func NewVariableEntryDeleteResource() resource.Resource {
	return &VariableEntryDeleteResource{}
}

type VariableEntryDeleteResource struct {
	client *ProviderClient
}

type VariableEntryDeleteResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Warehouse types.String `tfsdk:"warehouse"`
	SetID     types.String `tfsdk:"set_id"`
	Name      types.String `tfsdk:"name"`
	Triggers  types.Map    `tfsdk:"triggers"`
}

func (r *VariableEntryDeleteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_variable_entry_delete"
}

func (r *VariableEntryDeleteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Idempotently removes a named entry from an Unco variable set at apply time.",
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
			"triggers": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Arbitrary key/value pairs that re-run the delete check when changed.",
			},
		},
	}
}

func (r *VariableEntryDeleteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *VariableEntryDeleteResource) deleteEntry(ctx context.Context, data *VariableEntryDeleteResourceModel) error {
	wh := data.Warehouse.ValueString()
	setID := data.SetID.ValueString()
	entryName := data.Name.ValueString()

	_, err := r.client.MutateSet(ctx, wh, setID, func(set *VariableSet, exists bool) (bool, error) {
		if !exists {
			return false, nil
		}
		if _, ok := set.Variables[entryName]; !ok {
			return false, nil
		}
		delete(set.Variables, entryName)
		return true, nil
	})
	if err != nil {
		return err
	}

	data.ID = types.StringValue(fmt.Sprintf("%s/%s/%s", wh, setID, entryName))
	return nil
}

func (r *VariableEntryDeleteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryDeleteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteEntry(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to delete Unco variable entry", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryDeleteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data VariableEntryDeleteResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryDeleteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryDeleteResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.deleteEntry(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to delete Unco variable entry", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryDeleteResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}
