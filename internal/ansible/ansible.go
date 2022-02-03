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

	"github.com/apenella/go-ansible/pkg/playbook"
	"github.com/apenella/go-ansible/pkg/stdoutcallback/results"
	"github.com/spf13/afero"
)

// Parameters are minimal needed Parameters to initializes ansible-playbook command
type Parameters struct {
	// Dir in which to execute the ansible-playbook binary.
	Dir string
}

// A PlaybookOption configures an AnsiblePlaybookCmd.
type PlaybookOption func(*playbook.AnsiblePlaybookCmd)

// PbCmd is a playbook cmd
type PbCmd struct {
	PlaybookCmd *playbook.AnsiblePlaybookCmd
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

// WithOptions defines the ansible's playbook options.
func WithOptions(options *playbook.AnsiblePlaybookOptions) PlaybookOption {
	return func(ap *playbook.AnsiblePlaybookCmd) {
		ap.Options = options
	}
}

// Init initializes pbCmd from parameters
func (p Parameters) Init(ctx context.Context) (*PbCmd, error) {
	// Read playbooks filename from dir
	pbList, err := readDir(p.Dir)
	if err != nil {
		return nil, err
	}
	return NewAnsiblePlaybook(WithPlaybooks(pbList),
		// `ansible-playbook` cmd output JSON Serialization
		WithStdoutCallback("json"),
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

func Create(){

}
// readDir read names of all files in folders
func readDir(dir string) ([]string, error) {
	fs := afero.Afero{Fs: afero.NewOsFs()}
	file, err := fs.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Println("cannot close file: %w", err)
		}
	}()

	files, err := file.Readdirnames(0)
	if err != nil {
		return nil, err
	}

	return files, nil
}

// findChanges parse 'ansible-playbook --check' results to determine whether there is a diff between
// the desired and the actual state of the configuration. It returns true if
// there is a diff.
// TODO we should handle EXTRA_VARS as we invoke the Diff func
func (p *PbCmd) findChanges(ctx context.Context) bool {

	var changes bool
	// check changes for all hosts
	for _, stats := range p.res.Stats {
		if stats.Changed != 0 {
			changes = true
			break
		}
	}

	return changes
}

// exists must be true if a corresponding external resource exists
func exists(ctx context.Context, res *results.AnsiblePlaybookJSONResults) bool {

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

// ParseResultsWithMode play `ansible-playbook` then parse JSON stream results with selected mode
func (pbCmd *PbCmd) Apply(ctx context.Context, mode string) (bool, error) {

	switch mode {
	case "check":
		// Enable the check flag
		// Check don't make any changes; instead, try to predict some of the changes that may occur
		pbCmd.Playbook.Options.Check = true
	default:
	}

	go func(ctx context.Context, pbCmd *PbCmd) {
		_ = pbCmd.Playbook.Run(ctx)
	}(ctx, pbCmd)

	res, err := results.ParseJSONResultsStream(os.Stdout)
	if err != nil {
		return nil, err
	}

	c := findChanges(res)
	return c, nil

}
