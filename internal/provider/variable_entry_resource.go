package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &VariableEntryResource{}
	_ resource.ResourceWithImportState = &VariableEntryResource{}
)

func NewVariableEntryResource() resource.Resource {
	return &VariableEntryResource{}
}

type VariableEntryResource struct {
	client *ProviderClient
}

type VariableEntryResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Warehouse types.String `tfsdk:"warehouse"`
	SetID     types.String `tfsdk:"set_id"`
	Name      types.String `tfsdk:"name"`
	Kind      types.String `tfsdk:"kind"`
	Target    types.String `tfsdk:"target"`
	Value     types.String `tfsdk:"value"`
}

func (r *VariableEntryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_variable_entry"
}

func (r *VariableEntryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a single named entry inside an Unco variable set, leaving sibling entries untouched.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite identifier `<warehouse>/<set_id>/<name>`.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"warehouse": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Unco warehouse name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"set_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Variable set ID within the warehouse.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Entry name within the variable set.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kind": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(KindString),
				MarkdownDescription: "Entry kind (`string`, `bytes`, or a warehouse custom kind). Defaults to `string`.",
			},
			"target": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(TargetUnexported),
				MarkdownDescription: "Entry export target (`unexported`, `tfvar`, `env`, `file`, `envfile`, `kubefile`, `tfvarsfile`). Defaults to `unexported`.",
			},
			"value": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Entry value (plain string for `string`/`bytes` kinds, or JSON string for custom kinds).",
			},
		},
	}
}

func (r *VariableEntryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *VariableEntryResource) upsert(ctx context.Context, data *VariableEntryResourceModel) error {
	rawVal, err := encodeEntryValue(data.Kind.ValueString(), data.Value.ValueString())
	if err != nil {
		return err
	}
	desired := VariableValue{
		Kind:   data.Kind.ValueString(),
		Target: data.Target.ValueString(),
		Value:  rawVal,
	}

	_, err = r.client.MutateSet(ctx, data.Warehouse.ValueString(), data.SetID.ValueString(), func(set *VariableSet, _ bool) (bool, error) {
		if cur, ok := set.Variables[data.Name.ValueString()]; ok {
			if cur.KindOrDefault() == desired.KindOrDefault() &&
				cur.TargetOrDefault() == desired.TargetOrDefault() &&
				equalJSONStrings(string(cur.Value), string(desired.Value)) {
				return false, nil
			}
		}
		set.Variables[data.Name.ValueString()] = desired
		return true, nil
	})
	if err != nil {
		return err
	}

	data.ID = types.StringValue(fmt.Sprintf("%s/%s/%s", data.Warehouse.ValueString(), data.SetID.ValueString(), data.Name.ValueString()))
	return nil
}

func (r *VariableEntryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.upsert(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to create Unco variable entry", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	set, err := r.client.GetSet(ctx, data.Warehouse.ValueString(), data.SetID.ValueString())
	if errors.Is(err, ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Unco variable set", err.Error())
		return
	}

	entry, ok := set.Variables[data.Name.ValueString()]
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}

	data.Kind = types.StringValue(entry.KindOrDefault())
	data.Target = types.StringValue(entry.TargetOrDefault())
	decoded := decodeEntryValue(entry)
	if !isBuiltinKind(entry.KindOrDefault()) && !data.Value.IsNull() && equalJSONStrings(data.Value.ValueString(), decoded) {
	} else {
		data.Value = types.StringValue(decoded)
	}
	data.ID = types.StringValue(fmt.Sprintf("%s/%s/%s", data.Warehouse.ValueString(), data.SetID.ValueString(), data.Name.ValueString()))

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.upsert(ctx, &data); err != nil {
		resp.Diagnostics.AddError("Failed to update Unco variable entry", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *VariableEntryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data VariableEntryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.MutateSet(ctx, data.Warehouse.ValueString(), data.SetID.ValueString(), func(set *VariableSet, exists bool) (bool, error) {
		if !exists {
			return false, nil
		}
		if _, ok := set.Variables[data.Name.ValueString()]; !ok {
			return false, nil
		}
		delete(set.Variables, data.Name.ValueString())
		return true, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete Unco variable entry", err.Error())
	}
}

func (r *VariableEntryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format <warehouse>/<set_id>/<name>, got %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warehouse"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("set_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[2])...)
}
