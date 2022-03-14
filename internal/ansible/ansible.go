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
	"path/filepath"

	"github.com/apenella/go-ansible/pkg/options"
	"github.com/apenella/go-ansible/pkg/playbook"
	"github.com/apenella/go-ansible/pkg/stdoutcallback/results"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/spf13/afero"
)

// Parameters are minimal needed Parameters to initializes ansible-playbook command
type Parameters struct {
	// Dir in which to execute the ansible-playbook binary.
	Dir string

	// File to be ignored when executing the ansible-playbook binary.
	ExcludedFiles []string
}

// A PlaybookOption configures an AnsiblePlaybookCmd.
type PlaybookOption func(*playbook.AnsiblePlaybookCmd)

// PbCmd is a playbook cmd
type PbCmd struct {
	Playbook *playbook.AnsiblePlaybookCmd
}

// WithPlaybooks initializes Playbooks list.
func WithPlaybooks(playbooks []string) PlaybookOption {
	return func(ap *playbook.AnsiblePlaybookCmd) {
		ap.Playbooks = append(ap.Playbooks, playbooks...)
	}
}

// WithStdoutCallback defines which is the stdout callback method.
func WithStdoutCallback(stdoutCallback string) PlaybookOption {
	return func(ap *playbook.AnsiblePlaybookCmd) {
		ap.StdoutCallback = stdoutCallback
	}
}

// WithConnectionOptions defines the ansible's playbook connection options.
func WithConnectionOptions(options *options.AnsibleConnectionOptions) PlaybookOption {
	return func(ap *playbook.AnsiblePlaybookCmd) {
		ap.ConnectionOptions = options
	}
}

// WithOptions defines the ansible's playbook options.
func WithOptions(options *playbook.AnsiblePlaybookOptions) PlaybookOption {
	return func(ap *playbook.AnsiblePlaybookCmd) {
		ap.Options = options
	}
}

// Init initializes pbCmd from parameters
func (p Parameters) Init(ctx context.Context) (*PbCmd, error) {

	// Read playbooks filename from dir
	pbList, err := readDir(p.Dir, p.ExcludedFiles)
	if err != nil {
		return nil, err
	}
	return NewAnsiblePlaybook(WithPlaybooks(pbList),
		// `ansible-playbook` cmd output JSON Serialization
		WithStdoutCallback("json"),
		// e.g options.AnsibleConnectionOptions{Connection: "local"}
		WithConnectionOptions(&options.AnsibleConnectionOptions{}),
		// e.g playbook.AnsiblePlaybookOptions{Inventory: "127.0.0.1,"}
		WithOptions(&playbook.AnsiblePlaybookOptions{})), nil
}

// NewAnsiblePlaybook returns a pbCmd that will be used as ansible-playbook client
func NewAnsiblePlaybook(o ...PlaybookOption) *PbCmd {

	pb := &playbook.AnsiblePlaybookCmd{
		Playbooks: []string{},
	}

	for _, fn := range o {
		fn(pb)
	}

	return &PbCmd{Playbook: pb}
}

// contains string in an array
func contains(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

// readDir reads names of all files in playbookSet UID folder with the exclusion of excludedFiles
func readDir(dir string, excludedFiles []string) ([]string, error) {
	fs := afero.Afero{Fs: afero.NewOsFs()}
	playbookSetUIDdir, err := fs.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := playbookSetUIDdir.Close(); err != nil {
			fmt.Println("cannot close file: %w", err)
		}
	}()

	var filePaths []string
	if err := filepath.Walk(playbookSetUIDdir.Name(), func(path string, f os.FileInfo, err error) error {
		if !contains(path, excludedFiles) && !f.IsDir() {
			filePaths = append(filePaths, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return filePaths, nil
}

// Changes parse 'ansible-playbook --check' results to determine whether there is a diff between
// the desired and the actual state of the configuration. It returns true if
// there is a diff.
// TODO we should handle EXTRA_VARS as we invoke the Diff func
func diff(res *results.AnsiblePlaybookJSONResults) (bool, bool) {

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
		/* We assume that if stats.Ok == stats.Changed { 0 resourcesexists }
		 */
		if stats.Ok-stats.Changed > 0 {
			resourcesExists = true
			break
		}
	}
	return resourcesExists
}

// ParseResults play `ansible-playbook` then parse JSON stream results with check mode
func (pbCmd *PbCmd) ParseResults(ctx context.Context, mg resource.Managed) (bool, bool, error) {
	// Enable the check flag
	// Check don't make any changes; instead, try to predict some of the changes that may occur
	pbCmd.Playbook.Options.Check = true
	result, err := runAndParsePlaybook(ctx, pbCmd)
	if err != nil {
		return false, false, err
	}
	changes, re := diff(result)
	return changes, re, nil
}

// CreateOrUpdate run playbook during  update or create
func (pbCmd *PbCmd) CreateOrUpdate(ctx context.Context, mg resource.Managed) error {
	err := pbCmd.Playbook.Run(ctx)
	return err
}

// run playbook and parse result
func runAndParsePlaybook(ctx context.Context, pbCmd *PbCmd) (*results.AnsiblePlaybookJSONResults, error) {
	go func(ctx context.Context, pbCmd *PbCmd) {
		_ = pbCmd.Playbook.Run(ctx)
	}(ctx, pbCmd)

	res, err := results.ParseJSONResultsStream(os.Stdout)
	return res, err
}
