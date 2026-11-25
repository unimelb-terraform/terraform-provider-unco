package provider

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &PlanActionResource{}
	_ resource.ResourceWithImportState = &PlanActionResource{}
)

func NewPlanActionResource() resource.Resource {
	return &PlanActionResource{}
}

type PlanActionResource struct {
	client *ProviderClient
}

type PlanActionResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Warehouse    types.String `tfsdk:"warehouse"`
	Stack        types.String `tfsdk:"stack"`
	DelaySeconds types.Int64  `tfsdk:"delay_seconds"`
	Ref          types.String `tfsdk:"ref"`
	Targets      types.List   `tfsdk:"targets"`
	Destroy      types.Bool   `tfsdk:"destroy"`
	Triggers     types.Map    `tfsdk:"triggers"`
}

func (r *PlanActionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plan_action"
}

func (r *PlanActionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Queues a remote `plan` action on an Unco stack at apply time, optionally delayed by 0..900 seconds.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"warehouse": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Target Unco warehouse name.",
			},
			"stack": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Target Unco stack name.",
			},
			"delay_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Delivery delay in seconds (0..900).",
			},
			"ref": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional source/package ref to plan.",
			},
			"targets": schema.ListAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Optional resource addresses to pass via `-target`.",
			},
			"destroy": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Whether to plan a destroy operation.",
			},
			"triggers": schema.MapAttribute{
				ElementType:         types.StringType,
				Optional:            true,
				MarkdownDescription: "Arbitrary key/value pairs that re-trigger the plan action when changed.",
			},
		},
	}
}

func (r *PlanActionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *PlanActionResource) sendPlan(ctx context.Context, data *PlanActionResourceModel, isCreate bool) error {
	var actionReq ActionRequest
	if !data.DelaySeconds.IsNull() && !data.DelaySeconds.IsUnknown() {
		delay := int(data.DelaySeconds.ValueInt64())
		if delay < 0 || delay > MaxActionDelaySeconds {
			return fmt.Errorf("delay_seconds must be between 0 and %d, got %d", MaxActionDelaySeconds, delay)
		}
		actionReq.DelaySeconds = delay
	}
	if !data.Ref.IsNull() && !data.Ref.IsUnknown() {
		actionReq.Ref = data.Ref.ValueString()
	}
	if !data.Destroy.IsNull() && !data.Destroy.IsUnknown() {
		actionReq.Destroy = data.Destroy.ValueBool()
	}
	if !data.Targets.IsNull() && !data.Targets.IsUnknown() {
		var targets []string
		diags := data.Targets.ElementsAs(ctx, &targets, false)
		if diags.HasError() {
			return fmt.Errorf("invalid targets list")
		}
		actionReq.Targets = targets
	}

	if err := r.client.TriggerPlan(ctx, data.Warehouse.ValueString(), data.Stack.ValueString(), actionReq); err != nil {
		return err
	}

	if isCreate || data.ID.IsNull() || data.ID.ValueString() == "" {
		data.ID = types.StringValue(uuid.NewString())
	}
	return nil
}

func (r *PlanActionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data PlanActionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.sendPlan(ctx, &data, true); err != nil {
		resp.Diagnostics.AddError("Failed to trigger Unco plan action", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PlanActionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PlanActionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PlanActionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if r.client == nil {
		resp.Diagnostics.AddError("Provider Not Configured", "The Unco provider client was not configured.")
		return
	}

	var data PlanActionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.sendPlan(ctx, &data, false); err != nil {
		resp.Diagnostics.AddError("Failed to trigger Unco plan action", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PlanActionResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *PlanActionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
