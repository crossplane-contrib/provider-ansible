/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ansible

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"

	"github.com/pkg/errors"

	"github.com/apenella/go-ansible/pkg/stdoutcallback/results"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/provider-ansible/apis/v1alpha1"
	"github.com/crossplane/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane/provider-ansible/pkg/runnerutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// AnsibleRolesPath is the key defined by the user
	AnsibleRolesPath = "ANSIBLE_ROLE_PATH"
	// AnsibleCollectionsPath is key defined by the user
	AnsibleCollectionsPath = "ANSIBLE_COLLECTION_PATH"
)

const (
	errMarshalContentVars = "cannot marshal ContentVars into yaml document"
)

const (
	// AnnotationKeyPolicyRun is the name of an annotation which instructs
	// the provider how to run the corresponding Ansible contents
	AnnotationKeyPolicyRun = "ansible.crossplane.io/runPolicy"
)

// Parameters are minimal needed Parameters to initializes ansible command(s)
type Parameters struct {
	// ansible-galaxy binary path.
	GalaxyBinary string
	// ansible-runner binary path.
	RunnerBinary string
	// WorkingDirPath in which to execute the ansible-runner binary.
	WorkingDirPath  string
	CollectionsPath string
	// The source of this filed is either controller flag `--ansible-roles-path` or the env vars : `ANSIBLE_ROLES_PATH` , DEFAULT_ROLES_PATH`
	RolesPath string
}

// RunPolicy represents the run policies of Ansible.
type RunPolicy struct {
	Name string
}

// newRunPolicy creates a run Policy with the specified Name.
// supports the following run policies:
// - ObserveAndDelete
// - CheckWhenObserve
// For more details about RunPolicy : https://github.com/multicloudlab/crossplane-provider-ansible/blob/main/docs/design.md#ansible-run-policy
func newRunPolicy(rPolicy string) (*RunPolicy, error) {
	switch rPolicy {
	case "", "ObserveAndDelete":
		if rPolicy == "" {
			rPolicy = "ObserveAndDelete"
		}
	case "CheckWhenObserve":
	default:
		return nil, fmt.Errorf("run policy %q not supported", rPolicy)
	}
	return &RunPolicy{
		Name: rPolicy,
	}, nil
}

// GetPolicyRun returns the ansible run policy annotation value on the resource.
func GetPolicyRun(o metav1.Object) string {
	return o.GetAnnotations()[AnnotationKeyPolicyRun]
}

// SetPolicyRun sets the ansible run policy annotation of the resource.
func SetPolicyRun(o metav1.Object, name string) {
	meta.AddAnnotations(o, map[string]string{AnnotationKeyPolicyRun: name})
}

// A runnerOption configures a Runner.
type runnerOption func(*Runner)

// withPath initializes a runner path.
func withPath(path string) runnerOption {
	return func(r *Runner) {
		r.Path = path
	}
}

// withCmdFunc defines the runner CmdFunc.
func withCmdFunc(cmdFunc cmdFuncType) runnerOption {
	return func(r *Runner) {
		r.cmdFunc = cmdFunc
	}
}

// withAnsibleVerbosity set the runner verbosity.
func withAnsibleVerbosity(verbosity int) runnerOption {
	return func(r *Runner) {
		r.ansibleVerbosity = verbosity
	}
}

// withAnsibleGathering set the runner default policy of fact gathering.
func withAnsibleGathering(gathering string) runnerOption {
	return func(r *Runner) {
		r.ansibleGathering = gathering
	}
}

// withAnsibleHosts set the runner hosts to execute against.
func withAnsibleHosts(hosts string) runnerOption {
	return func(r *Runner) {
		r.ansibleHosts = hosts
	}
}

// withAnsibleEnvDir set the runner env/extravars dir.
func withAnsibleEnvDir(dir string) runnerOption {
	return func(r *Runner) {
		r.AnsibleEnvDir = dir
	}
}

// withAnsibleRunPolicy set the runner Policy to execute against.
func withAnsibleRunPolicy(p *RunPolicy) runnerOption {
	return func(r *Runner) {
		r.AnsibleRunPolicy = p
	}
}

type cmdFuncType func(gathering string, hosts string, verbosity int, checkMode bool) *exec.Cmd

// playbookCmdFunc mimics https://github.com/operator-framework/operator-sdk/blob/707240f006ecfc0bc86e5c21f6874d302992d598/internal/ansible/runner/runner.go#L75-L90
func (p Parameters) playbookCmdFunc(ctx context.Context, playbookName string, path string) cmdFuncType {
	return func(_ string, _ string, verbosity int, checkMode bool) *exec.Cmd {
		cmdArgs := []string{"run", path}
		cmdOptions := []string{
			"-p", playbookName,
		}
		// enable check mode via cmdline https://github.com/ansible/ansible-runner/issues/580
		if checkMode {
			cmdOptions = append(cmdOptions, "--cmdline", "\\--check")
		}
		// check the verbosity since the exec.Command will fail if an arg as "" or " " be informed
		if verbosity > 0 {
			cmdOptions = append(cmdOptions, runnerutil.AnsibleVerbosityString(verbosity))
		}
		// gosec is disabled here because of G204. We should pay attention that user can't
		// make command injection via command argument
		return exec.CommandContext(ctx, p.RunnerBinary, append(cmdArgs, cmdOptions...)...) //nolint:gosec
	}
}

// roleCmdFunc mimics https://github.com/operator-framework/operator-sdk/blob/707240f006ecfc0bc86e5c21f6874d302992d598/internal/ansible/runner/runner.go#L92-L118
func (p Parameters) roleCmdFunc(ctx context.Context, roleName string, path string) cmdFuncType {
	return func(gathering string, hosts string, verbosity int, checkMode bool) *exec.Cmd {
		cmdOptions := []string{
			"--role", roleName,
			"--hosts", hosts,
		}
		cmdArgs := []string{"run", filepath.Join(path, roleName)}
		// enable check mode via cmdline https://github.com/ansible/ansible-runner/issues/580
		if checkMode {
			cmdOptions = append(cmdOptions, "--cmdline", "\\--check")
		}
		// check the verbosity since the exec.Command will fail if an arg as "" or " " be informed
		if verbosity > 0 {
			cmdOptions = append(cmdOptions, runnerutil.AnsibleVerbosityString(verbosity))
		}
		// ansibleGathering := os.Getenv("ANSIBLE_GATHERING")

		// When running a role directly, ansible-runner does not respect the ANSIBLE_GATHERING
		// environment variable, so we need to skip fact collection manually
		if gathering == "explicit" {
			cmdOptions = append(cmdOptions, "--role-skip-facts")
		}

		// gosec is disabled here because of G204. We should pay attention that user can't
		// make command injection via command argument
		return exec.CommandContext(ctx, p.RunnerBinary, append(cmdArgs, cmdOptions...)...) //nolint:gosec
	}
}

// GalaxyInstall Install non-exists collections/roles with ansible-galaxy cli
func (p Parameters) GalaxyInstall(ctx context.Context, behaviorVars map[string]string, requirementsType string) error {
	requirementsFilePath := runnerutil.GetFullPath(p.WorkingDirPath, galaxyutil.RequirementsFile)
	var cmdArgs []string
	var cmdOptions []string
	switch requirementsType {
	case "collection":
		cmdArgs = []string{"collection", "install"}
		cmdOptions = []string{
			"--requirements-file", requirementsFilePath,
		}
	case "role":
		cmdArgs = []string{"role", "install"}
		cmdOptions = []string{
			"--role-file", requirementsFilePath,
		}
		/* By default, Ansible downloads roles to the first writable directory in the default list of paths:
		   ~/.ansible/roles:/usr/share/ansible/roles:/etc/ansible/roles
		   this can be override by behaviorVars[AnsibleRolesPath] or p.RolesPath
		*/
		if behaviorVars[AnsibleRolesPath] != "" {
			cmdOptions = append(cmdOptions, []string{"--roles-path", behaviorVars[AnsibleRolesPath]}...)
		} else if p.RolesPath != "" {
			cmdOptions = append(cmdOptions, []string{"--roles-path", p.RolesPath}...)
		}

	}
	// ansible-galaxy is by default verbose
	cmdOptions = append(cmdOptions, "--verbose")

	// gosec is disabled here because of G204. We should pay attention that user can't
	// make command injection via command argument
	dc := exec.CommandContext(ctx, p.GalaxyBinary, append(cmdArgs, cmdOptions...)...) //nolint:gosec

	out, err := dc.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install galaxy collections/roles: %v: %w", string(out), err)
	}
	return nil
}

// Init initializes a new runner from parameters
// nolint: gocyclo
func (p Parameters) Init(ctx context.Context, cr *v1alpha1.AnsibleRun, pc *v1alpha1.ProviderConfig, behaviorVars map[string]string) (*Runner, error) {
	var cmdFunc cmdFuncType
	var err error
	var path, ansibleEnvDir string

	switch {
	case cr.Spec.ForProvider.PlaybookInline == nil && len(cr.Spec.ForProvider.Roles) == 0:
		return nil, errors.New("at least a Playbook or Role should be provided")
	case cr.Spec.ForProvider.PlaybookInline != nil && len(cr.Spec.ForProvider.Roles) != 0:
		return nil, errors.New("cannot execute Playbook(s) and Role(s) at the same time, please respect Mutual Exclusion")
	case cr.Spec.ForProvider.PlaybookInline != nil:
		// For inline mode playbook is stored in the predefined playbookYml file
		path = p.WorkingDirPath
		cmdFunc = p.playbookCmdFunc(ctx, runnerutil.PlaybookYml, path)
		// init ansible env dir
		ansibleEnvDir = filepath.Clean(filepath.Join(path, "env"))
	case len(cr.Spec.ForProvider.Roles) != 0:
		var err error
		path, err = addRolePath(p, behaviorVars)
		if err != nil {
			return nil, err
		}
		// TODO support multiple roles execution
		cmdFunc = p.roleCmdFunc(ctx, cr.Spec.ForProvider.Roles[0].Name, path)
		// init ansible env dir
		ansibleEnvDir = filepath.Clean(filepath.Join(path, cr.Spec.ForProvider.Roles[0].Name, "env"))
	}

	// prepare ansible runner extravars
	// create extravars file even empty. We need the extravars file later to handle status variables
	if err := os.MkdirAll(ansibleEnvDir, 0700); resource.Ignore(os.IsExist, err) != nil {
		return nil, errors.Wrap(err, "cannot make Playbook directory")
	}
	contentVarsBytes, err := cr.Spec.ForProvider.Vars.MarshalJSON()
	if err != nil {
		return nil, errors.Wrap(err, errMarshalContentVars)
	}
	if string(contentVarsBytes) == "null" {
		contentVarsBytes = nil
	}
	if err := addFile(filepath.Join(ansibleEnvDir, "extravars"), contentVarsBytes); err != nil {
		return nil, err
	}

	rPolicy, err := newRunPolicy(GetPolicyRun(cr))
	if err != nil {
		return nil, err
	}
	return new(withPath(path),
		withCmdFunc(cmdFunc),
		// TODO add verbosity filed to the API, now it is ignored by (0) value
		withAnsibleVerbosity(0),
		withAnsibleGathering(behaviorVars["ANSIBLE_GATHERING"]),
		// TODO hosts should be handled via configuration vars e.g: vars["hosts"]
		withAnsibleHosts("localhost"),
		withAnsibleRunPolicy(rPolicy),
		withAnsibleEnvDir(ansibleEnvDir),
	), nil
}

// Runner struct
type Runner struct {
	Path             string // absolute path on disk to a playbook or role depending on what cmdFunc expects
	behaviorVars     map[string]string
	cmdFunc          cmdFuncType // returns a Cmd that runs ansible-runner
	ansibleVerbosity int
	ansibleGathering string
	ansibleHosts     string
	AnsibleEnvDir    string
	checkMode        bool
	AnsibleRunPolicy *RunPolicy
}

// new returns a runner that will be used as ansible-runner client
func new(o ...runnerOption) *Runner {

	r := &Runner{}

	for _, fn := range o {
		fn(r)
	}

	return r
}

// GetAnsibleRunPolicy to retrieve Ansible RunPolicy
func (r *Runner) GetAnsibleRunPolicy() *RunPolicy {
	return r.AnsibleRunPolicy
}

// Run execute the appropriate cmdFunc
func (r *Runner) Run() (*exec.Cmd, error) {
	dc := r.cmdFunc(r.ansibleGathering, r.ansibleHosts, r.ansibleVerbosity, r.checkMode)
	behaviorVarsSlice := runnerutil.ConvertMapToSlice(r.behaviorVars)
	// Append current environment since setting dc.Env to anything other than nil overwrites current env
	// Some behaviorVars are not assessed because they are actually passed to cmd as flag
	dc.Env = append(dc.Env, os.Environ()...)
	dc.Env = append(dc.Env, behaviorVarsSlice...)

	dc.Stdout = os.Stdout
	dc.Stderr = os.Stderr

	err := dc.Start()
	if err != nil {
		return nil, err
	}

	return dc, nil
}

// addRolePath will determines the role path
func addRolePath(p Parameters, behaviorVars map[string]string) (string, error) {
	var rolesPath string
	switch {
	case behaviorVars[AnsibleRolesPath] != "":
		rolesPath = behaviorVars[AnsibleRolesPath]
	case p.RolesPath != "":
		rolesPath = p.RolesPath
	default:
		// default Ansible Configuration
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		rolesPaths := []string{filepath.Clean(filepath.Join(u.HomeDir, ".ansible/roles")), "/usr/share/ansible/roles", "/etc/ansible/roles"}
		for _, possiblePath := range rolesPaths {
			if _, err := os.Stat(possiblePath); err == nil {
				rolesPath = possiblePath
				break
			}
		}
	}
	return rolesPath, nil
}

// addFile micmics https://github.com/operator-framework/operator-sdk/blob/master/internal/ansible/runner/internal/inputdir/inputdir.go#L55-L63
func addFile(path string, content []byte) error {
	if err := os.WriteFile(path, content, 0600); err != nil {
		return err
	}
	return nil
}

// WriteExtraVar write extra var to env/extravars under working directory
// it creates a non-existent env/extravars file
func (r *Runner) WriteExtraVar(extraVar map[string]interface{}) error {
	extraVarsPath := filepath.Join(r.AnsibleEnvDir, "extravars")
	contentVars := make(map[string]interface{})
	data, err := os.ReadFile(filepath.Clean(extraVarsPath))
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	if len(data) != 0 {
		if err := json.Unmarshal(data, &contentVars); err != nil {
			return err
		}
	}
	contentVars["ansible_provider_meta"] = extraVar
	contentVarsB, err := json.Marshal(contentVars)
	if err != nil {
		return err
	}
	if err := os.WriteFile(extraVarsPath, contentVarsB, 0600); err != nil {
		return err
	}
	return nil
}

// Diff parses `ansible-runner --check` json output to determine whether there is a diff between
// the desired and the actual state of the configuration. It returns true if there is a diff.
func Diff(res *results.AnsiblePlaybookJSONResults) (bool, bool) {

	var changes bool
	// check changes for all hosts
	for _, stats := range res.Stats {
		if stats.Changed != 0 {
			changes = true
			break
		}
	}

	return changes, exists(res)
}

// Exists must be true if a corresponding external resource exists
func exists(res *results.AnsiblePlaybookJSONResults) bool {
	var resourcesExists bool
	// check changes for all hosts
	for _, stats := range res.Stats {
		// We assume that if stats.Ok == stats.Changed { 0 resourcesexists }
		if stats.Ok-stats.Changed > 0 {
			resourcesExists = true
			break
		}
	}
	return resourcesExists
}

// EnableCheckMode enable the runner checkMode.
func (r *Runner) EnableCheckMode(m bool) {
	r.checkMode = m
}

// runWithCheckMode plays `ansible-runner` with check mode
// then parse JSON stream results
/*func (r *Runner) runWithCheckMode(ctx context.Context, mg resource.Managed) (bool, bool, error) {
	// Enable the check flag
	// Check don't make any changes; instead, try to predict some of the changes that may occur
	pbCmd.Playbook.Options.Check = true
	result, err := runAndParsePlaybook(ctx, pbCmd)
	if err != nil {
		return false, false, err
	}
	changes, re := diff(result)
	return changes, re, nil
}*/

// CreateOrUpdate run playbook during  update or create
/*func (pbCmd *PbCmd) CreateOrUpdate(ctx context.Context, mg resource.Managed) error {
	err := pbCmd.Playbook.Run(ctx)
	return err
}*/

// ParseCmdJsonOutput parse ansible-runner json output
/*func ParseCmdJsonOutput(ctx context.Context, pbCmd *PbCmd) (*results.AnsiblePlaybookJSONResults, error) {
	res, err := results.ParseJSONResultsStream(os.Stdout)
	return res, err
}*/
