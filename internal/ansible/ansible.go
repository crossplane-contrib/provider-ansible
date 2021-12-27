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

	"github.com/apenella/go-ansible/pkg/playbook"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/spf13/afero"
)

// A PlaybookOption configures an AnsiblePlaybookCmd.
type PlaybookOption func(*playbook.AnsiblePlaybookCmd)

// PbClient is a playbook client
type PbClient struct {
	Playbook *playbook.AnsiblePlaybookCmd
}

// WithPlaybooks initializes Playbooks list.
func WithPlaybooks(playbooks []string) PlaybookOption {
	return func(ap *playbook.AnsiblePlaybookCmd) {
		ap.Playbooks = append(ap.Playbooks, playbooks...)
	}
}

// NewAnsiblePlaybook returns a pbClient that will be used as ansible-playbook client
func NewAnsiblePlaybook(ctx context.Context, o ...PlaybookOption) *PbClient {

	pb := &playbook.AnsiblePlaybookCmd{
		Playbooks: []string{},
	}

	for _, fn := range o {
		fn(pb)
	}

	return &PbClient{Playbook: pb}
}

// ReadDir read names of all files in folders
func ReadDir(dir string, l logging.Logger) ([]string, error) {
	fs := afero.Afero{Fs: afero.NewOsFs()}
	file, err := fs.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			l.Debug("Cannot close file", "error", err)
		}
	}()

	files, err := file.Readdirnames(0)
	if err != nil {
		return nil, err
	}

	return files, nil
}
