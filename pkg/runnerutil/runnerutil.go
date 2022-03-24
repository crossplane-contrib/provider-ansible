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

package runnerutil

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/crossplane/provider-ansible/apis/v1alpha1"
)

const (
	// PlaybookYml contains the inline playbook(s)
	PlaybookYml = "playbook.yml"

	// RoleYml contains the inline role(s) and/or collection
	RoleYml = "role.yml"
)

// RunnerBinary searches for ansible-runner binary in the directories named by the PATH environment variable
func RunnerBinary() (string, error) {
	return exec.LookPath("ansible-runner")
}

// AnsibleVerbosityString is from https://github.com/operator-framework/operator-sdk/blob/v1.18.1/internal/ansible/runner/runner.go#L63-L71
func AnsibleVerbosityString(verbosity int) string {
	if verbosity > 0 {
		// the default verbosity is 0
		// more info: https://docs.ansible.com/ansible/latest/reference_appendices/config.html#default-verbosity
		return fmt.Sprintf("-%v", strings.Repeat("v", verbosity))
	}
	// Return default verbosity
	return ""
}

// GetFullPath returns the absolute path of role/playbook in working directory
func GetFullPath(workingDir, path string) string {
	return filepath.Join(workingDir, path)
}

// ConvertKVVarsToMap converts {"key":"testKey", "value":"testValue"} to {"testKey":"testValue"}
func ConvertKVVarsToMap(values []v1alpha1.Var) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
	}
	return result, nil
}
