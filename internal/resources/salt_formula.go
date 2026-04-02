package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/stefanob/terraform-provider-salt/pkg/salt"
	"github.com/stefanob/terraform-provider-salt/pkg/ssh"
)

var (
	_ resource.Resource                = &SaltFormulaResource{}
	_ resource.ResourceWithConfigure   = &SaltFormulaResource{}
	_ resource.ResourceWithImportState = &SaltFormulaResource{}
)

const formulaDir = "/var/lib/salt-tf/formula"

type SaltFormulaResource struct {
	defaultSaltVersion string
}

type SaltFormulaModel struct {
	ID              types.String `tfsdk:"id"`
	Host            types.String `tfsdk:"host"`
	Port            types.Int64  `tfsdk:"port"`
	User            types.String `tfsdk:"user"`
	PrivateKey      types.String `tfsdk:"private_key"`
	SaltVersion     types.String `tfsdk:"salt_version"`
	RepoURL         types.String `tfsdk:"repo_url"`
	Ref             types.String `tfsdk:"ref"`
	Pillar          types.Map    `tfsdk:"pillar"`
	Triggers        types.Map    `tfsdk:"triggers"`
	SSHTimeout      types.Int64  `tfsdk:"ssh_timeout"`
	SaltTimeout     types.Int64  `tfsdk:"salt_timeout"`
	KeepRemoteFiles types.Bool   `tfsdk:"keep_remote_files"`
	AppliedHash     types.String `tfsdk:"applied_hash"`
	StateOutput     types.String `tfsdk:"state_output"`
}

func NewSaltFormulaResource() func() resource.Resource {
	return func() resource.Resource {
		return &SaltFormulaResource{}
	}
}

func (r *SaltFormulaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	saltVersion, ok := req.ProviderData.(string)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", "Expected string for salt_version.")
		return
	}
	r.defaultSaltVersion = saltVersion
}

func (r *SaltFormulaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_formula"
}

func (r *SaltFormulaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Clones a Salt formula from a git repository and applies it to a remote host in masterless mode.",
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
				Description: "Salt version to ensure is installed.",
				Optional:    true,
			},
			"repo_url": schema.StringAttribute{
				Description: "Git repository URL for the Salt formula (e.g. https://github.com/saltstack-formulas/nginx-formula.git).",
				Required:    true,
			},
			"ref": schema.StringAttribute{
				Description: "Git ref to checkout (branch, tag, or commit SHA). Defaults to the repo's default branch.",
				Optional:    true,
			},
			"pillar": schema.MapAttribute{
				Description: "Pillar data to pass to the formula.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"triggers": schema.MapAttribute{
				Description: "Arbitrary map of values that, when changed, trigger re-application.",
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
				Description: "Timeout in seconds for salt-call execution. Defaults to 300.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(300),
			},
			"keep_remote_files": schema.BoolAttribute{
				Description: "If true, keep formula files on the remote host after destroy.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"applied_hash": schema.StringAttribute{
				Description: "Hash of the last successful salt-call output.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					reapplyOnDrift{},
				},
			},
			"state_output": schema.StringAttribute{
				Description: "Raw JSON output from the last salt-call run.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					unknownOnDrift{},
				},
			},
		},
	}
}

func (r *SaltFormulaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data SaltFormulaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, diags := r.applyFormula(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(fmt.Sprintf("%s:%s", data.Host.ValueString(), data.RepoURL.ValueString()))
	data.AppliedHash = types.StringValue(result.Hash)
	data.StateOutput = types.StringValue(result.RawJSON)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *SaltFormulaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data SaltFormulaModel
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
		resp.State.RemoveResource(ctx)
		return
	}
	defer client.Close()

	// Clone/update the formula for testing
	if err := r.cloneFormula(client, &data); err != nil {
		resp.Diagnostics.AddWarning("Drift detection failed",
			fmt.Sprintf("Could not clone formula on %s: %s", data.Host.ValueString(), err.Error()))
		data.AppliedHash = types.StringValue("")
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	pillar := extractStringMap(ctx, data.Pillar)
	if err := salt.UploadPillar(client, pillar, formulaDir); err != nil {
		resp.Diagnostics.AddWarning("Drift detection failed",
			fmt.Sprintf("Could not upload pillar on %s: %s", data.Host.ValueString(), err.Error()))
		data.AppliedHash = types.StringValue("")
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	saltTimeout := int64(300)
	if !data.SaltTimeout.IsNull() && !data.SaltTimeout.IsUnknown() {
		saltTimeout = data.SaltTimeout.ValueInt64()
	}

	result, err := salt.Test(client, pillar, formulaDir, int(saltTimeout))
	if err != nil {
		resp.Diagnostics.AddWarning("Drift detection failed",
			fmt.Sprintf("Could not run salt-call test on %s: %s", data.Host.ValueString(), err.Error()))
		data.AppliedHash = types.StringValue("")
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	if !result.InSync {
		resp.Diagnostics.AddWarning(
			fmt.Sprintf("Drift detected on %s", data.Host.ValueString()),
			result.Summary(),
		)
		data.AppliedHash = types.StringValue("")
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *SaltFormulaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data SaltFormulaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	result, diags := r.applyFormula(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(fmt.Sprintf("%s:%s", data.Host.ValueString(), data.RepoURL.ValueString()))
	data.AppliedHash = types.StringValue(result.Hash)
	data.StateOutput = types.StringValue(result.RawJSON)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *SaltFormulaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data SaltFormulaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if data.KeepRemoteFiles.ValueBool() {
		return
	}

	client, err := r.connect(&data)
	if err != nil {
		return
	}
	defer client.Close()

	_, _ = client.Run(fmt.Sprintf("sudo rm -rf %s", formulaDir))
}

func (r *SaltFormulaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *SaltFormulaResource) connect(data *SaltFormulaModel) (*ssh.Client, error) {
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

func (r *SaltFormulaResource) cloneFormula(client *ssh.Client, data *SaltFormulaModel) error {
	repoURL := data.RepoURL.ValueString()

	// Ensure git is installed
	if _, err := client.Run("which git || sudo apt-get install -y git 2>/dev/null || sudo yum install -y git 2>/dev/null"); err != nil {
		return fmt.Errorf("git not available: %w", err)
	}

	// Ensure base directory exists and is writable
	_, _ = client.Run(fmt.Sprintf("sudo mkdir -p %s && sudo chmod 777 %s", salt.BaseDir, salt.BaseDir))

	// Clone or update
	checkCmd := fmt.Sprintf("test -d %s/.git", formulaDir)
	if _, err := client.Run(checkCmd); err != nil {
		// Fresh clone
		cmd := fmt.Sprintf("rm -rf %s && git clone %s %s", formulaDir, repoURL, formulaDir)
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("git clone failed: %w", err)
		}
	} else {
		// Update existing
		cmd := fmt.Sprintf("cd %s && git fetch origin && git reset --hard origin/HEAD", formulaDir)
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("git update failed: %w", err)
		}
	}

	// Checkout specific ref if provided
	if !data.Ref.IsNull() && !data.Ref.IsUnknown() {
		ref := data.Ref.ValueString()
		cmd := fmt.Sprintf("cd %s && git checkout %s", formulaDir, ref)
		if _, err := client.Run(cmd); err != nil {
			return fmt.Errorf("git checkout %s failed: %w", ref, err)
		}
	}

	return nil
}

func (r *SaltFormulaResource) applyFormula(ctx context.Context, data *SaltFormulaModel) (*salt.Result, diag.Diagnostics) {
	var diags diag.Diagnostics
	host := data.Host.ValueString()

	client, err := r.connect(data)
	if err != nil {
		diags.AddError(fmt.Sprintf("SSH connection to %s failed", host), err.Error())
		return nil, diags
	}
	defer client.Close()

	// Determine Salt version
	saltVersion := r.defaultSaltVersion
	if !data.SaltVersion.IsNull() && !data.SaltVersion.IsUnknown() {
		saltVersion = data.SaltVersion.ValueString()
	}

	if saltVersion != "" {
		if err := salt.EnsureVersion(client, saltVersion); err != nil {
			diags.AddError(
				fmt.Sprintf("Salt bootstrap failed on %s", host),
				fmt.Sprintf("Failed to install Salt version %q.\n\n%s", saltVersion, err.Error()),
			)
			return nil, diags
		}
	}

	// Clone the formula
	if err := r.cloneFormula(client, data); err != nil {
		diags.AddError(fmt.Sprintf("Formula clone failed on %s", host), err.Error())
		return nil, diags
	}

	// Upload pillar data
	pillar := extractStringMap(ctx, data.Pillar)
	if err := salt.UploadPillar(client, pillar, formulaDir); err != nil {
		diags.AddError(fmt.Sprintf("Pillar upload to %s failed", host), err.Error())
		return nil, diags
	}

	// Generate a top.sls for the formula — discover state files in the cloned repo
	topContent, err := r.generateFormulaTop(client)
	if err != nil {
		diags.AddError(fmt.Sprintf("Failed to generate top.sls on %s", host), err.Error())
		return nil, diags
	}
	if err := client.Upload(formulaDir+"/top.sls", []byte(topContent)); err != nil {
		diags.AddError(fmt.Sprintf("Failed to upload top.sls on %s", host), err.Error())
		return nil, diags
	}

	saltTimeout := int64(300)
	if !data.SaltTimeout.IsNull() && !data.SaltTimeout.IsUnknown() {
		saltTimeout = data.SaltTimeout.ValueInt64()
	}

	result, err := salt.Apply(client, pillar, formulaDir, int(saltTimeout))
	if err != nil {
		diags.AddError(fmt.Sprintf("Salt apply failed on %s", host), err.Error())
		return nil, diags
	}

	if !result.Success {
		diags.AddError(fmt.Sprintf("Salt formula failed on %s", host), result.FailedStates())
		return nil, diags
	}

	if result.Stderr != "" {
		stderr := salt.CleanStderr(result.Stderr)
		if stderr != "" {
			diags.AddWarning(fmt.Sprintf("Salt warnings on %s", host), stderr)
		}
	}

	return result, diags
}

// generateFormulaTop discovers init.sls files in the formula dir and generates a top.sls.
func (r *SaltFormulaResource) generateFormulaTop(client *ssh.Client) (string, error) {
	// Find directories containing init.sls (standard formula layout)
	out, err := client.Run(fmt.Sprintf(
		"find %s -name init.sls -not -path '*/test/*' -not -path '*/.git/*' | head -20",
		formulaDir,
	))
	if err != nil {
		return "", fmt.Errorf("finding state files: %w", err)
	}

	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		// Convert /var/lib/salt-tf/formula/nginx/init.sls → nginx
		rel := strings.TrimPrefix(line, formulaDir+"/")
		name := strings.TrimSuffix(rel, "/init.sls")
		if name != "" && name != rel {
			names = append(names, name)
		}
	}

	if len(names) == 0 {
		return "", fmt.Errorf("no init.sls files found in %s — is this a valid Salt formula?", formulaDir)
	}

	var b strings.Builder
	b.WriteString("base:\n  '*':\n")
	for _, name := range names {
		b.WriteString(fmt.Sprintf("    - %s\n", name))
	}
	return b.String(), nil
}
