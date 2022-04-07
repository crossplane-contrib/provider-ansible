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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/crossplane/provider-ansible/apis/v1alpha1"
	"github.com/crossplane/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane/provider-ansible/pkg/runnerutil"
)

const (
	// AnsibleRolesPath is the key defined by the user
	AnsibleRolesPath = "ANSIBLE_ROLE_PATH"
	// AnsibleCollectionsPath is key defined by the user
	AnsibleCollectionsPath = "ANSIBLE_COLLECTION_PATH"
)

// Parameters are minimal needed Parameters to initializes ansible-runner command
type Parameters struct {
	// Dir in which to execute the ansible-runner binary.
	WorkingDir      string
	CollectionsPath string
	RolesPath       string
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

// withAnsibleVerbosity set the ansible-runner verbosity.
func withAnsibleVerbosity(verbosity int) runnerOption {
	return func(r *Runner) {
		r.ansibleVerbosity = verbosity
	}
}

// withAnsibleGathering set the ansible-runner default policy of fact gathering.
func withAnsibleGathering(gathering string) runnerOption {
	return func(r *Runner) {
		r.ansibleGathering = gathering
	}
}

// withAnsibleHosts set the ansible-runner hosts to execute against.
func withAnsibleHosts(hosts string) runnerOption {
	return func(r *Runner) {
		r.ansibleHosts = hosts
	}
}

type cmdFuncType func(gathering string, hosts string, verbosity int) *exec.Cmd

// playbookCmdFunc mimics https://github.com/operator-framework/operator-sdk/blob/707240f006ecfc0bc86e5c21f6874d302992d598/internal/ansible/runner/runner.go#L75-L90
func playbookCmdFunc(path string) (cmdFuncType, error) {
	runnerBinary, err := runnerutil.RunnerBinary()
	if err != nil {
		return nil, err
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	return func(_ string, hosts string, verbosity int) *exec.Cmd {
		cmdArgs := []string{"run", wd}
		cmdOptions := []string{
			"-p", path,
			"--hosts", hosts,
		}

		// check the verbosity since the exec.Command will fail if an arg as "" or " " be informed
		if verbosity > 0 {
			cmdOptions = append(cmdOptions, runnerutil.AnsibleVerbosityString(verbosity))
		}
		// gosec is disabled here because of G204. We should pay attention that user can't
		// make command injection via command argument
		return exec.Command(runnerBinary, append(cmdArgs, cmdOptions...)...) //nolint:gosec
	}, nil
}

// roleCmdFunc mimics https://github.com/operator-framework/operator-sdk/blob/707240f006ecfc0bc86e5c21f6874d302992d598/internal/ansible/runner/runner.go#L92-L118
func roleCmdFunc(path string) (cmdFuncType, error) {
	rolePath, roleName := filepath.Split(path)
	runnerBinary, err := runnerutil.RunnerBinary()
	if err != nil {
		return nil, err
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	return func(gathering string, hosts string, verbosity int) *exec.Cmd {
		cmdOptions := []string{
			"--role", roleName,
			"--roles-path", rolePath,
			"--hosts", hosts,
		}
		cmdArgs := []string{"run", wd}

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
		return exec.Command(runnerBinary, append(cmdArgs, cmdOptions...)...) //nolint:gosec
	}, nil
}

// galaxyInstall Install non-exists collections with ansible-galaxy cli
func (p Parameters) galaxyInstall() error {
	galaxyBinary, err := galaxyutil.GalaxyBinary()
	if err != nil {
		return err
	}

	requirementsFilePath := runnerutil.GetFullPath(p.WorkingDir, galaxyutil.RequirementsFile)

	cmdArgs := []string{"collection", "install"}
	cmdOptions := []string{
		"--requirements-file", requirementsFilePath,
	}

	// ansible-galaxy is by default verbose
	cmdOptions = append(cmdOptions, "--verbose")

	// gosec is disabled here because of G204. We should pay attention that user can't
	// make command injection via command argument
	dc := exec.Command(galaxyBinary, append(cmdArgs, cmdOptions...)...) //nolint:gosec

	out, err := dc.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install galaxy collections: %v: %v", string(out), err)
	}
	return nil
}

// Init initializes a new runner from parameters
func (p Parameters) Init(ctx context.Context, cr *v1alpha1.AnsibleRun) (*Runner, error) {
	if err := p.galaxyInstall(); err != nil {
		return nil, err
	}

	vars, err := runnerutil.ConvertKVVarsToMap(cr.Spec.ForProvider.Vars)
	if err != nil {
		return nil, err
	}

	addRolePlaybookPaths(p, vars, cr)

	if cr.Spec.ForProvider.Source == v1alpha1.ConfigurationSourceInline {
		// For inline mode playbook is stored in the predefined playbookYml file
		// override user input if exists
		cr.Spec.ForProvider.Playbook = runnerutil.PlaybookYml
		// TODO handle ansible roles inline mode
		cr.Spec.ForProvider.Role = ""
	}

	var cmdFunc cmdFuncType
	var path string

	switch {
	case cr.Spec.ForProvider.Playbook != "":
		path = cr.Spec.ForProvider.Playbook
		if cmdFunc, err = playbookCmdFunc(path); err != nil {
			return nil, err
		}
	case cr.Spec.ForProvider.Role != "":
		path = cr.Spec.ForProvider.Role
		if cmdFunc, err = roleCmdFunc(path); err != nil {
			return nil, err
		}
	}

	return new(withPath(path),
		withCmdFunc(cmdFunc),
		// TODO add verbosity filed to the API, now it is ignored by (0) value
		withAnsibleVerbosity(0),
		withAnsibleGathering(vars["ANSIBLE_GATHERING"]),
		withAnsibleHosts(vars["hosts"]),
	), nil
}

// Runner struct
type Runner struct {
	Path             string // path on disk to a playbook or role depending on what cmdFunc expects
	Vars             map[string]interface{}
	cmdFunc          cmdFuncType // returns a Cmd that runs ansible-runner
	ansibleVerbosity int
	ansibleGathering string
	ansibleHosts     string
}

// new returns a runner that will be used as ansible-runner client
func new(o ...runnerOption) *Runner {

	r := &Runner{}

	for _, fn := range o {
		fn(r)
	}

	return r
}

// addRolePlaybookPaths will add the full path based on absolute path of cloning dir
// Func from operator SDK
func addRolePlaybookPaths(p Parameters, vars map[string]string, cr *v1alpha1.AnsibleRun) {
	if len(cr.Spec.ForProvider.Playbook) > 0 {
		cr.Spec.ForProvider.Playbook = runnerutil.GetFullPath(p.WorkingDir, cr.Spec.ForProvider.Playbook)
	}

	if len(cr.Spec.ForProvider.Role) > 0 {
		collectionsPath := p.CollectionsPath
		rolesPath := p.RolesPath
		switch {
		case vars[AnsibleRolesPath] != "":
			rolesPath = vars[AnsibleRolesPath]
		case vars[AnsibleCollectionsPath] != "":
			collectionsPath = vars[AnsibleCollectionsPath]
		}

		possibleRolePaths := getPossibleRolePaths(p.WorkingDir, cr.Spec.ForProvider.Role, collectionsPath, rolesPath)
		for _, possiblePath := range possibleRolePaths {
			if _, err := os.Stat(possiblePath); err == nil {
				cr.Spec.ForProvider.Role = possiblePath
				break
			}
		}
	}
}

// getPossibleRolePaths returns list of possible absolute paths derived from a user provided value.
func getPossibleRolePaths(workingDir, path, ansibleRolesPath, ansibleCollectionsPath string) []string {
	possibleRolePaths := []string{}
	if filepath.IsAbs(path) || len(path) == 0 {
		return append(possibleRolePaths, path)
	}
	fqcn := strings.Split(path, ".")
	// If fqcn is a valid fully qualified collection name, it is <namespace>.<collectionName>.<roleName>
	if len(fqcn) == 3 {
		if ansibleCollectionsPath == "" {
			ansibleCollectionsPath = "/usr/share/ansible/collections"
			home, err := os.UserHomeDir()
			if err == nil {
				homeCollections := filepath.Join(home, ".ansible/collections")
				ansibleCollectionsPath = ansibleCollectionsPath + ":" + homeCollections
			}
		}
		for _, possiblePathParent := range strings.Split(ansibleCollectionsPath, ":") {
			possiblePath := filepath.Join(possiblePathParent, "ansible_collections", fqcn[0], fqcn[1], "roles", fqcn[2])
			possibleRolePaths = append(possibleRolePaths, possiblePath)
		}
	}

	// Check for the role where Ansible would. If it exists, use it.
	if ansibleRolesPath != "" {
		for _, possiblePathParent := range strings.Split(ansibleRolesPath, ":") {
			// "roles" is optionally a part of the path. Check with, and without.
			possibleRolePaths = append(possibleRolePaths, filepath.Join(possiblePathParent, path), filepath.Join(possiblePathParent, "roles", path))
		}
	}
	// Roles can also live in the working directory.
	return append(possibleRolePaths, runnerutil.GetFullPath(workingDir, filepath.Join("roles", path)))
}

// Changes parse 'ansible-playbook --check' results to determine whether there is a diff between
// the desired and the actual state of the configuration. It returns true if
// there is a diff.
// TODO we should handle EXTRA_VARS as we invoke the Diff func
/*func diff(res *results.AnsiblePlaybookJSONResults) (bool, bool) {

	var changes bool
	// check changes for all hosts
	for _, stats := range res.Stats {
		if stats.Changed != 0 {
			changes = true
			break
		}
	}

	return changes, exists(res)
}*/

// Exists must be true if a corresponding external resource exists
/*func exists(res *results.AnsiblePlaybookJSONResults) bool {
	var resourcesExists bool
	// check changes for all hosts
	for _, stats := range res.Stats {
		//We assume that if stats.Ok == stats.Changed { 0 resourcesexists }
		if stats.Ok-stats.Changed > 0 {
			resourcesExists = true
			break
		}
	}
	return resourcesExists
}*/

// ParseResults play `ansible-playbook` then parse JSON stream results with check mode
/*func (pbCmd *PbCmd) ParseResults(ctx context.Context, mg resource.Managed) (bool, bool, error) {
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

// run playbook and parse result
/*func runAndParsePlaybook(ctx context.Context, pbCmd *PbCmd) (*results.AnsiblePlaybookJSONResults, error) {
	go func(ctx context.Context, pbCmd *PbCmd) {
		_ = pbCmd.Playbook.Run(ctx)
	}(ctx, pbCmd)

	res, err := results.ParseJSONResultsStream(os.Stdout)
	return res, err
}*/
