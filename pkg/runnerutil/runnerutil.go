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
	"fmt"
	"os/exec"
	"path/filepath"
)

const (
	// PlaybookYml contains the inline playbook(s)
	PlaybookYml = "playbook.yml"

	// Hosts is the inventory filename
	Hosts = "hosts"
)

// RunnerBinary searches for ansible-runner binary in the directories named by the PATH environment variable
func RunnerBinary() (string, error) {
	return exec.LookPath("ansible-runner")
}

// GetFullPath returns the absolute path of role/playbook in working directory
func GetFullPath(workingDir, path string) string {
	return filepath.Join(workingDir, path)
}

// ConvertMapToSlice converts {"testKey1":"testValue1","testKey2":"testValue2"} to {"testKey1=testValue1","testKey2=testValue2"}
func ConvertMapToSlice(values map[string]string) []string {
	result := []string{}
	for k, v := range values {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}
