package resources

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bartei/terraform-provider-salt/pkg/salt"
	"github.com/bartei/terraform-provider-salt/pkg/ssh"
)

var (
	_ resource.Resource                = &SaltStateResource{}
	_ resource.ResourceWithConfigure   = &SaltStateResource{}
	_ resource.ResourceWithImportState = &SaltStateResource{}
)

type SaltStateResource struct {
	defaultSaltVersion string
}

type SaltStateModel struct {
	ID              types.String `tfsdk:"id"`
	Host            types.String `tfsdk:"host"`
	Port            types.Int64  `tfsdk:"port"`
	User            types.String `tfsdk:"user"`
	PrivateKey      types.String `tfsdk:"private_key"`
	SaltVersion     types.String `tfsdk:"salt_version"`
	States          types.Map    `tfsdk:"states"`
	Pillar          types.Map    `tfsdk:"pillar"`
	DestroyStates   types.Map    `tfsdk:"destroy_states"`
	DestroyPillar   types.Map    `tfsdk:"destroy_pillar"`
	Triggers        types.Map    `tfsdk:"triggers"`
	SSHTimeout      types.Int64  `tfsdk:"ssh_timeout"`
	SaltTimeout     types.Int64  `tfsdk:"salt_timeout"`
	KeepRemoteFiles types.Bool   `tfsdk:"keep_remote_files"`
	AppliedHash     types.String `tfsdk:"applied_hash"`
	StateOutput     types.String `tfsdk:"state_output"`
}

func NewSaltStateResource() func() resource.Resource {
	return func() resource.Resource {
		return &SaltStateResource{}
	}
}

func (r *SaltStateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	saltVersion, ok := req.ProviderData.(string)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			"Expected string for salt_version, got something else.",
		)
		return
	}
	r.defaultSaltVersion = saltVersion
}

func (r *SaltStateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_state"
}

func (r *SaltStateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Applies Salt states to a remote host in masterless mode via SSH.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Resource identifier.",
				Computed:    true,
			},
			"host": schema.StringAttribute{
				Description: "Target host address.",
				Required:    true,
			},
			"port": schema.Int64Attribute{
				Description: "SSH port. Defaults to 22.",
				Optional:    true,
			},
			"user": schema.StringAttribute{
				Description: "SSH user.",
				Required:    true,
			},
			"private_key": schema.StringAttribute{
				Description: "SSH private key contents.",
				Required:    true,
				Sensitive:   true,
			},
			"salt_version": schema.StringAttribute{
				Description: "Salt version to ensure is installed on the target. Use a version number like \"3007\" or \"latest\" to install whatever version the bootstrap script provides.",
				Optional:    true,
				Validators: []validator.String{
					saltVersionValidator{},
				},
			},
			"states": schema.MapAttribute{
				Description: "Map of state file paths to their contents (e.g. \"k3s/init.sls\" = file(\"...\")).",
				Required:    true,
				ElementType: types.StringType,
			},
			"pillar": schema.MapAttribute{
				Description: "Pillar data to pass to Salt states.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"destroy_states": schema.MapAttribute{
				Description: "Map of state file paths to their contents, applied during terraform destroy before cleaning up remote files. Use this to reverse the effects of the main states (e.g. unmount filesystems, stop services).",
				Optional:    true,
				ElementType: types.StringType,
			},
			"destroy_pillar": schema.MapAttribute{
				Description: "Pillar data to pass to destroy states.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"triggers": schema.MapAttribute{
				Description: "Arbitrary map of values that, when changed, trigger re-application of states.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"ssh_timeout": schema.Int64Attribute{
				Description: "SSH connection timeout in seconds. Defaults to 30.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(30),
			},
			"salt_timeout": schema.Int64Attribute{
				Description: "Timeout in seconds for salt-call execution. Defaults to 300 (5 minutes). Set to 0 for no timeout.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(300),
			},
			"keep_remote_files": schema.BoolAttribute{
				Description: "If true, keep Salt state files on the remote host after destroy. Defaults to false.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"applied_hash": schema.StringAttribute{
				Description: "Hash of the last successful salt-call output.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					unknownOnAnyChange{inputsChanged: saltStateInputsChanged},
				},
			},
			"state_output": schema.StringAttribute{
				Description: "Raw JSON output from the last salt-call run.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					unknownOnAnyChange{inputsChanged: saltStateInputsChanged},
					unknownWhenHashCleared{},
				},
			},
		},
	}
}

func (r *SaltStateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data SaltStateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceID := computeID(data.Host.ValueString(), data.States)
	result, diags := r.applySalt(ctx, &data, resourceID)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(resourceID)
	data.AppliedHash = types.StringValue(result.Hash)
	data.StateOutput = types.StringValue(result.RawJSON)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *SaltStateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data SaltStateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// After import, only id is set — skip drift detection and mark for apply
	if data.Host.IsNull() || data.PrivateKey.IsNull() {
		data.AppliedHash = types.StringValue("")
		data.StateOutput = types.StringValue("")
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	client, err := r.connect(&data)
	if err != nil {
		// Host unreachable — remove from state so it gets recreated
		resp.State.RemoveResource(ctx)
		return
	}
	defer func() { _ = client.Close() }()

	workDir := salt.WorkDir(data.ID.ValueString())
	states := extractStringMap(ctx, data.States)
	pillar := extractStringMap(ctx, data.Pillar)

	if err := salt.UploadStates(client, states, workDir); err != nil {
		resp.Diagnostics.AddError("Failed to upload states for drift check", err.Error())
		return
	}

	if err := salt.UploadPillar(client, pillar, workDir); err != nil {
		resp.Diagnostics.AddError("Failed to upload pillar for drift check", err.Error())
		return
	}

	saltTimeout := int64(300)
	if !data.SaltTimeout.IsNull() && !data.SaltTimeout.IsUnknown() {
		saltTimeout = data.SaltTimeout.ValueInt64()
	}

	result, err := salt.Test(client, pillar, workDir, int(saltTimeout))
	if err != nil {
		// Can't determine drift — mark as needing update and warn the user
		resp.Diagnostics.AddWarning(
			"Drift detection failed",
			fmt.Sprintf("Could not run salt-call test mode on %s — the resource will be re-applied on next apply.\n\n%s",
				data.Host.ValueString(), err.Error()),
		)
		data.AppliedHash = types.StringValue("")
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	if !result.InSync {
		// Drift detected — clear hash so plan shows a diff
		resp.Diagnostics.AddWarning(
			fmt.Sprintf("Drift detected on %s", data.Host.ValueString()),
			result.Summary(),
		)
		data.AppliedHash = types.StringValue("")
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *SaltStateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data SaltStateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resourceID := computeID(data.Host.ValueString(), data.States)
	result, diags := r.applySalt(ctx, &data, resourceID)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(resourceID)
	data.AppliedHash = types.StringValue(result.Hash)
	data.StateOutput = types.StringValue(result.RawJSON)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *SaltStateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data SaltStateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	destroyStates := extractStringMap(ctx, data.DestroyStates)

	// If no destroy states and keep_remote_files is set, nothing to do
	if len(destroyStates) == 0 && data.KeepRemoteFiles.ValueBool() {
		return
	}

	client, err := r.connect(&data)
	if err != nil {
		// Host gone — nothing to clean up
		return
	}
	defer func() { _ = client.Close() }()

	// Apply destroy states if configured
	if len(destroyStates) > 0 {
		diags := r.applyDestroyStates(ctx, client, &data, destroyStates)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	if !data.KeepRemoteFiles.ValueBool() {
		// Clean up this resource's working directory
		_, _ = client.Run(fmt.Sprintf("sudo rm -rf %s", salt.WorkDir(data.ID.ValueString())))
	}
}

func (r *SaltStateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The import ID is used as the resource ID directly.
	// The user must write the full resource block in HCL first, then import.
	// Read() will run drift detection on the next plan.
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *SaltStateResource) connect(data *SaltStateModel) (*ssh.Client, error) {
	port := int64(22)
	if !data.Port.IsNull() && !data.Port.IsUnknown() {
		port = data.Port.ValueInt64()
	}

	sshTimeout := 30 * time.Second
	if !data.SSHTimeout.IsNull() && !data.SSHTimeout.IsUnknown() {
		sshTimeout = time.Duration(data.SSHTimeout.ValueInt64()) * time.Second
	}

	return ssh.NewClientWithRetry(ssh.ConnectConfig{
		Host:          data.Host.ValueString(),
		Port:          int(port),
		User:          data.User.ValueString(),
		PrivateKey:    data.PrivateKey.ValueString(),
		Timeout:       sshTimeout,
		MaxRetries:    3,
		RetryInterval: 5 * time.Second,
	})
}

func (r *SaltStateResource) applySalt(ctx context.Context, data *SaltStateModel, resourceID string) (*salt.Result, diag.Diagnostics) {
	var diags diag.Diagnostics

	host := data.Host.ValueString()

	client, err := r.connect(data)
	if err != nil {
		diags.AddError(
			fmt.Sprintf("SSH connection to %s failed", host),
			err.Error(),
		)
		return nil, diags
	}
	defer func() { _ = client.Close() }()

	// Determine Salt version
	saltVersion := r.defaultSaltVersion
	if !data.SaltVersion.IsNull() && !data.SaltVersion.IsUnknown() {
		saltVersion = data.SaltVersion.ValueString()
	}

	// Bootstrap Salt if needed (default to "latest" if no version specified)
	if saltVersion == "" {
		saltVersion = "latest"
	}
	if err := salt.EnsureVersion(client, saltVersion); err != nil {
		diags.AddError(
			fmt.Sprintf("Salt bootstrap failed on %s", host),
			fmt.Sprintf("Failed to install Salt version %q.\n\n%s", saltVersion, err.Error()),
		)
		return nil, diags
	}

	workDir := salt.WorkDir(resourceID)
	states := extractStringMap(ctx, data.States)
	pillar := extractStringMap(ctx, data.Pillar)

	// Upload state files
	if err := salt.UploadStates(client, states, workDir); err != nil {
		diags.AddError(
			fmt.Sprintf("State upload to %s failed", host),
			err.Error(),
		)
		return nil, diags
	}

	// Upload pillar data
	if err := salt.UploadPillar(client, pillar, workDir); err != nil {
		diags.AddError(
			fmt.Sprintf("Pillar upload to %s failed", host),
			err.Error(),
		)
		return nil, diags
	}

	// Run salt-call --local
	saltTimeout := int64(300)
	if !data.SaltTimeout.IsNull() && !data.SaltTimeout.IsUnknown() {
		saltTimeout = data.SaltTimeout.ValueInt64()
	}

	result, err := salt.Apply(client, pillar, workDir, int(saltTimeout))
	if err != nil {
		diags.AddError(
			fmt.Sprintf("Salt apply failed on %s", host),
			err.Error(),
		)
		return nil, diags
	}

	if !result.Success {
		diags.AddError(
			fmt.Sprintf("Salt states failed on %s", host),
			result.FailedStates(),
		)
		return nil, diags
	}

	// Surface Salt warnings (e.g. deprecations, non-fatal issues) even on success
	if result.Stderr != "" {
		stderr := salt.CleanStderr(result.Stderr)
		if stderr != "" {
			diags.AddWarning(
				fmt.Sprintf("Salt warnings on %s", host),
				stderr,
			)
		}
	}

	return result, diags
}

// applyDestroyStates uploads and applies the destroy_states on the remote host
// during terraform destroy. It uses a separate working directory to avoid
// interfering with the main states.
func (r *SaltStateResource) applyDestroyStates(ctx context.Context, client *ssh.Client, data *SaltStateModel, destroyStates map[string]string) diag.Diagnostics {
	var diags diag.Diagnostics
	host := data.Host.ValueString()

	// Determine Salt version
	saltVersion := r.defaultSaltVersion
	if !data.SaltVersion.IsNull() && !data.SaltVersion.IsUnknown() {
		saltVersion = data.SaltVersion.ValueString()
	}
	if saltVersion == "" {
		saltVersion = "latest"
	}
	if err := salt.EnsureVersion(client, saltVersion); err != nil {
		diags.AddError(
			fmt.Sprintf("Salt bootstrap failed on %s during destroy", host),
			fmt.Sprintf("Failed to install Salt version %q.\n\n%s", saltVersion, err.Error()),
		)
		return diags
	}

	// Use a separate workdir for destroy states so they don't collide with
	// the main states (which may still be on disk if keep_remote_files is true)
	workDir := salt.WorkDir(data.ID.ValueString()) + "-destroy"
	destroyPillar := extractStringMap(ctx, data.DestroyPillar)

	if err := salt.UploadStates(client, destroyStates, workDir); err != nil {
		diags.AddError(
			fmt.Sprintf("Destroy state upload to %s failed", host),
			err.Error(),
		)
		return diags
	}

	if err := salt.UploadPillar(client, destroyPillar, workDir); err != nil {
		diags.AddError(
			fmt.Sprintf("Destroy pillar upload to %s failed", host),
			err.Error(),
		)
		return diags
	}

	saltTimeout := int64(300)
	if !data.SaltTimeout.IsNull() && !data.SaltTimeout.IsUnknown() {
		saltTimeout = data.SaltTimeout.ValueInt64()
	}

	result, err := salt.Apply(client, destroyPillar, workDir, int(saltTimeout))
	if err != nil {
		diags.AddError(
			fmt.Sprintf("Salt destroy failed on %s", host),
			err.Error(),
		)
		return diags
	}

	if !result.Success {
		diags.AddError(
			fmt.Sprintf("Salt destroy states failed on %s", host),
			result.FailedStates(),
		)
		return diags
	}

	// Clean up the destroy workdir
	_, _ = client.Run(fmt.Sprintf("sudo rm -rf %s", workDir))

	if result.Stderr != "" {
		stderr := salt.CleanStderr(result.Stderr)
		if stderr != "" {
			diags.AddWarning(
				fmt.Sprintf("Salt warnings during destroy on %s", host),
				stderr,
			)
		}
	}

	return diags
}

func extractStringMap(ctx context.Context, m types.Map) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	result := make(map[string]string, len(m.Elements()))
	for k, v := range m.Elements() {
		if sv, ok := v.(types.String); ok {
			result[k] = sv.ValueString()
		}
	}
	return result
}

// saltStateInputsChanged returns true if any input attributes of SaltStateModel
// differ between state and plan.
func saltStateInputsChanged(ctx context.Context, stateRaw, planRaw tfsdk.State) bool {
	var state, plan SaltStateModel
	if diags := stateRaw.Get(ctx, &state); diags.HasError() {
		return true // err on the side of re-applying
	}
	if diags := planRaw.Get(ctx, &plan); diags.HasError() {
		return true
	}
	return !state.States.Equal(plan.States) ||
		!state.Pillar.Equal(plan.Pillar) ||
		!state.Triggers.Equal(plan.Triggers) ||
		!state.Host.Equal(plan.Host) ||
		!state.User.Equal(plan.User) ||
		!state.PrivateKey.Equal(plan.PrivateKey) ||
		!state.SaltVersion.Equal(plan.SaltVersion)
}

// unknownOnAnyChange marks a computed attribute as unknown whenever the resource
// will be updated. This prevents the "Provider produced inconsistent result
// after apply" error that occurs when plan predicts a concrete value for
// applied_hash/state_output but apply produces a different one (because
// salt-call test=True output differs from actual apply output).
//
// It accepts an optional inputsChanged function that compares plan vs state
// to detect config changes. If nil, the modifier only detects drift.
type unknownOnAnyChange struct {
	inputsChanged func(ctx context.Context, state, plan tfsdk.State) bool
}

func (m unknownOnAnyChange) Description(_ context.Context) string {
	return "Marks attribute as unknown when resource will be updated, preserves state otherwise."
}

func (m unknownOnAnyChange) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m unknownOnAnyChange) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// On create, there's no prior state — leave as unknown (the default for computed attrs)
	if req.StateValue.IsNull() {
		return
	}

	// If drift was detected, Read() clears applied_hash to "" — mark unknown
	// so Terraform sees a diff and schedules an Update.
	if req.StateValue.ValueString() == "" {
		resp.PlanValue = types.StringUnknown()
		return
	}

	// Check if any input attributes changed between state and plan.
	// We need to wrap the plan as a tfsdk.State so both can be read uniformly.
	if m.inputsChanged != nil {
		planAsState := tfsdk.State{Schema: req.State.Schema, Raw: req.Plan.Raw}
		if m.inputsChanged(ctx, req.State, planAsState) {
			resp.PlanValue = types.StringUnknown()
			return
		}
	}

	// Nothing changed — preserve the current state value
	resp.PlanValue = req.StateValue
}

// unknownWhenHashCleared marks state_output as unknown when applied_hash was
// cleared to "" by Read() (drift detected). Without this, the plan preserves
// the old state_output value but apply produces a new one → inconsistent result.
type unknownWhenHashCleared struct{}

func (m unknownWhenHashCleared) Description(_ context.Context) string {
	return "Marks state_output as unknown when applied_hash was cleared (drift detected)."
}

func (m unknownWhenHashCleared) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m unknownWhenHashCleared) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() {
		return
	}

	var state SaltStateModel
	if diags := req.State.Get(ctx, &state); diags.HasError() {
		return
	}
	if state.AppliedHash.ValueString() == "" {
		resp.PlanValue = types.StringUnknown()
	}
}

// saltVersionValidator validates the salt_version attribute format.
type saltVersionValidator struct{}

var saltVersionPattern = regexp.MustCompile(`^(latest|\d+(\.\d+)*)$`)

func (v saltVersionValidator) Description(_ context.Context) string {
	return "Must be a version number (e.g. \"3007\", \"3007.1\") or \"latest\"."
}

func (v saltVersionValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v saltVersionValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	value := req.ConfigValue.ValueString()
	if !saltVersionPattern.MatchString(value) {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Salt version format",
			fmt.Sprintf("Expected a version number like \"3007\" or \"3007.1\", or \"latest\". Got: %q", value),
		)
	}
}

func computeID(host string, states types.Map) string {
	var keys []string
	for k := range states.Elements() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	raw := fmt.Sprintf("%s:%s", host, strings.Join(keys, ","))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:16]
}
