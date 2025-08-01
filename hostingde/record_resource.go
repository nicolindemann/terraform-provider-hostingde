package hostingde

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &recordResource{}
	_ resource.ResourceWithConfigure   = &recordResource{}
	_ resource.ResourceWithImportState = &recordResource{}
)

func normalizeRecordContent(content string) string {
	newContent := strings.ReplaceAll(content, "\" \"", "");
	return strings.ReplaceAll(newContent, "\"", "");
}

// NewRecordResource is a helper function to simplify the provider implementation.
func NewRecordResource() resource.Resource {
	return &recordResource{}
}

// recordResource is the resource implementation.
type recordResource struct {
	client *Client
}

// recordResourceModel maps the DNSRecord resource schema data.
type recordResourceModel struct {
	ID       types.String `tfsdk:"id"`
	ZoneID   types.String `tfsdk:"zone_id"`
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	Content  types.String `tfsdk:"content"`
	TTL      types.Int64  `tfsdk:"ttl"`
	Priority types.Int64  `tfsdk:"priority"`
	Comments types.String `tfsdk:"comments"`
}

// Metadata returns the resource type name.
func (r *recordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_record"
}

// Schema defines the schema for the resource.
func (r *recordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "DNS record ID",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"zone_id": schema.StringAttribute{
				Description: "ID of DNS zone that the record belongs to.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "Name of the record. Example: mail.example.com.",
				Required:    true,
			},
			"type": schema.StringAttribute{
				Description: "Type of the DNS record. Valid types are A, AAAA, ALIAS, CAA, CERT, CNAME, DNSKEY, DS, MX, NS, NSEC, NSEC3, NSEC3PARAM, NULLMX, OPENPGPKEY, PTR, RRSIG, SRV, SSHFP, TLSA, and TXT.",
				Required:    true,
			},
			"content": schema.StringAttribute{
				Description: "Content of the DNS record.",
				Required:    true,
			},
			"ttl": schema.Int64Attribute{
				Description: "TTL of the DNS record in seconds. Minimum is 60, maximum is 31556926. Defaults to 3600.",
				Computed:    true,
				Required:    false,
				Optional:    true,
				Default:     int64default.StaticInt64(3600),
				Validators: []validator.Int64{
					int64validator.Between(60, 31556926),
				},
			},
			"priority": schema.Int64Attribute{
				Description: "Priority of MX and SRV records.",
				Computed:    true,
				Required:    false,
				Optional:    true,
			},
			"comments": schema.StringAttribute{
				Description: "Comment to the record.",
				Required:    false,
				Optional:    true,
			},
		},
	}
}

// Create a new resource
func (r *recordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan recordResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Generate API request body from plan
	record := DNSRecord{
		Name:     plan.Name.ValueString(),
		ZoneID:   plan.ZoneID.ValueString(),
		Type:     plan.Type.ValueString(),
		Content:  plan.Content.ValueString(),
		TTL:      int(plan.TTL.ValueInt64()),
		Priority: int(plan.Priority.ValueInt64()),
		Comments: plan.Comments.ValueString(),
	}

	recordReq := RecordsUpdateRequest{
		BaseRequest:  &BaseRequest{},
		ZoneConfigId: plan.ZoneID.ValueString(),
		RecordsToAdd: []DNSRecord{record},
	}

	recordResp, err := r.client.updateRecords(recordReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating records",
			"Could not update records, unexpected error: "+err.Error(),
		)
		return
	}

	var returnedRecord DNSRecord
	for _, responseRecord := range recordResp.Response.Records {
		if responseRecord.Name == record.Name && responseRecord.Type == record.Type {
			if responseRecord.Content == record.Content {
				returnedRecord = responseRecord
				break;
			} 

			normalizedContent := normalizeRecordContent(responseRecord.Content);
			if normalizedContent == record.Content {
				returnedRecord = responseRecord
				returnedRecord.Content = normalizedContent
				break;
			} 
		}
	}

	// Overwrite DNS record with refreshed state
	plan.ZoneID = types.StringValue(recordResp.Response.ZoneConfig.ID)
	plan.ID = types.StringValue(returnedRecord.ID)
	plan.Name = types.StringValue(returnedRecord.Name)
	plan.Type = types.StringValue(returnedRecord.Type)
	plan.Content = types.StringValue(returnedRecord.Content)
	plan.TTL = types.Int64Value(int64(returnedRecord.TTL))
	plan.Priority = types.Int64Value(int64(returnedRecord.Priority))
	plan.Comments = types.StringValue(returnedRecord.Comments)

	// Set state to fully populated data
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Read refreshes the Terraform state with the latest data.
func (r *recordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Get current state
	var state recordResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	recordReq := RecordsFindRequest{
		BaseRequest: &BaseRequest{},
		Filter: FilterOrChain{Filter: Filter{
			Field: "RecordId",
			Value: state.ID.ValueString(),
		}},
		Limit: 1,
		Page:  1,
	}

	// Get refreshed DNS record from hostingde
	recordResp, err := r.client.listRecords(recordReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Reading hosting.de DNS zone",
			"Could not read hosting.de DNS zone ID "+state.ID.ValueString()+": "+err.Error(),
		)
		return
	}

	returnedRecord := recordResp.Response.Data[0]
	// Overwrite DNS record with refreshed state
	state.ZoneID = types.StringValue(returnedRecord.ZoneID)
	state.ID = types.StringValue(returnedRecord.ID)
	state.Name = types.StringValue(returnedRecord.Name)
	state.Type = types.StringValue(returnedRecord.Type)
	state.Content = types.StringValue(normalizeRecordContent(returnedRecord.Content))
	state.TTL = types.Int64Value(int64(returnedRecord.TTL))
	state.Priority = types.Int64Value(int64(returnedRecord.Priority))
	state.Comments = types.StringValue(returnedRecord.Comments)

	// Set refreshed state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *recordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Retrieve values from plan
	var plan recordResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Generate API request body from plan
	record := DNSRecord{
		Name:     plan.Name.ValueString(),
		ID:       plan.ID.ValueString(),
		ZoneID:   plan.ZoneID.ValueString(),
		Type:     plan.Type.ValueString(),
		Content:  plan.Content.ValueString(),
		TTL:      int(plan.TTL.ValueInt64()),
		Priority: int(plan.Priority.ValueInt64()),
		Comments: plan.Comments.ValueString(),
	}

	recordReq := RecordsUpdateRequest{
		BaseRequest:     &BaseRequest{},
		ZoneConfigId:    plan.ZoneID.ValueString(),
		RecordsToModify: []DNSRecord{record},
	}

	recordResp, err := r.client.updateRecords(recordReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating records",
			"Could not update records, unexpected error: "+err.Error(),
		)
		return
	}

	var returnedRecord DNSRecord
	for _, responseRecord := range recordResp.Response.Records {
		if responseRecord.Name == record.Name && responseRecord.Type == record.Type {
			if responseRecord.Content == record.Content {
				returnedRecord = responseRecord
				break;
			}

			normalizedContent := normalizeRecordContent(responseRecord.Content);
			if normalizedContent == record.Content {
				returnedRecord = responseRecord
				returnedRecord.Content = normalizedContent
				break;
			}
		}
	}

	// Overwrite DNS record with refreshed state
	plan.ZoneID = types.StringValue(recordResp.Response.ZoneConfig.ID)
	plan.ID = types.StringValue(returnedRecord.ID)
	plan.Name = types.StringValue(returnedRecord.Name)
	plan.Type = types.StringValue(returnedRecord.Type)
	plan.Content = types.StringValue(returnedRecord.Content)
	plan.TTL = types.Int64Value(int64(returnedRecord.TTL))
	plan.Priority = types.Int64Value(int64(returnedRecord.Priority))
	plan.Comments = types.StringValue(returnedRecord.Comments)

	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *recordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Retrieve values from state
	var state recordResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Generate API request body from plan
	record := DNSRecord{
		ID:   state.ID.ValueString(),
		Name: state.Name.ValueString(),
		Type: state.Type.ValueString(),
	}

	recordReq := RecordsUpdateRequest{
		BaseRequest:     &BaseRequest{},
		ZoneConfigId:    state.ZoneID.ValueString(),
		RecordsToDelete: []DNSRecord{record},
	}

	// Delete existing record
	_, err := r.client.updateRecords(recordReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Deleting Record",
			"Could not delete record, unexpected error: "+err.Error(),
		)
		return
	}
}

// Configure adds the provider configured client to the resource.
func (r *recordResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	r.client = req.ProviderData.(*Client)
}

func (r *recordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Retrieve import ID and save to id attribute
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *recordResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	// Retrieve values from config
	var configData recordResourceModel
	diags := req.Config.Get(ctx, &configData)
	resp.Diagnostics.Append(diags...)

	if resp.Diagnostics.HasError() {
		return
	}

	// If Type is MX or SRV, return without warning.
	if configData.Type.ValueString() == "MX" || configData.Type.ValueString() == "SRV" {
		if configData.Priority.IsNull() {
			resp.Diagnostics.AddAttributeError(
				path.Root("Priority"),
				"Missing attribute",
				"Setting priority is required for records of type MX or SRV. "+
					"Please add a priority to the resource, for example priority = 0.",
			)
		}
		return
	}

	// If Priority is not configured, return without warning.
	if configData.Priority.IsNull() || configData.Priority.IsUnknown() {
		return
	}

	resp.Diagnostics.AddAttributeError(
		path.Root("Type"),
		"Unexpected combination of attributes",
		"Priority is only relevant for records of type MX or SRV. "+
			"Please remove priority from the resource or change its type.",
	)
}
