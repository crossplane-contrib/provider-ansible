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
	"os"
	"path/filepath"
	"testing"

	"github.com/crossplane/provider-ansible/apis/v1alpha1"
	"github.com/crossplane/provider-ansible/pkg/runnerutil"
	"gotest.tools/v3/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	baseWorkingDir = "ansibleDir"
	uid            = types.UID("definitely-a-uuid")
	name           = "testApp"
	requirements   = `---
                    collections:`
)

var (
	ctx        = context.Background()
	objectMeta = metav1.ObjectMeta{Name: name, UID: uid}
)

func prepareAnsibleContext(dir string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "ansible-init-test")
	if err != nil {
		return "", err
	}

	ansibleDir := filepath.Join(tmpDir, dir)
	if err := os.MkdirAll(ansibleDir, 0750); err != nil {
		return "", err
	}

	if err = os.WriteFile(filepath.Join(ansibleDir, "requirements.yml"), []byte(requirements), 0644); err != nil {
		return "", err
	}

	if err = os.WriteFile(filepath.Join(ansibleDir, "playbook.yml"), nil, 0644); err != nil {
		return "", err
	}

	roleDir := filepath.Join(ansibleDir, "roles")
	if err := os.Mkdir(roleDir, 0750); err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(roleDir, "role.yml"), nil, 0644); err != nil {
		return "", err
	}
	return ansibleDir, nil
}

func TestInit(t *testing.T) {
	validPlaybook := "playbook.yml"
	validRole := "role.yml"

	testCases := []struct {
		name     string
		playbook string
		role     string
	}{
		{
			name:     "basic runner with playbook",
			playbook: validPlaybook,
		},
		{
			name: "basic runner with role",
			role: validRole,
		},
	}

	dir := filepath.Join(baseWorkingDir, string(uid))
	ansibleCtx, err := prepareAnsibleContext(dir)
	assert.NilError(t, err)
	defer os.RemoveAll(ansibleCtx)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			firstCr := v1alpha1.AnsibleRun{ObjectMeta: objectMeta}
			firstCr.Spec.ForProvider.Playbook = filepath.Join(ansibleCtx, tc.playbook)
			firstCr.Spec.ForProvider.Role = filepath.Join(filepath.Join(ansibleCtx, "roles"), tc.role)

			ps := Parameters{
				WorkingDir: ansibleCtx,
			}

			secondCr := v1alpha1.AnsibleRun{ObjectMeta: objectMeta}
			secondCr.Spec.ForProvider.Playbook = tc.playbook
			secondCr.Spec.ForProvider.Role = tc.role

			testRunner, err := ps.Init(ctx, &secondCr)
			if err != nil {
				t.Fatalf("Error occurred unexpectedly: %v", err)
			}

			switch {
			case tc.playbook != "":
				if testRunner.Path != firstCr.Spec.ForProvider.Playbook {
					t.Fatalf("Unexpected path %v expected path %v", testRunner.Path, firstCr.Spec.ForProvider.Playbook)
				}
			case tc.role != "":
				if testRunner.Path != firstCr.Spec.ForProvider.Role {
					t.Fatalf("Unexpected path %v expected path %v", testRunner.Path, firstCr.Spec.ForProvider.Role)
				}
			}

		})
	}
}

// TestAnsibleVerbosityString from https://github.com/operator-framework/operator-sdk/blob/v1.18.1/internal/ansible/runner/runner_test.go#L228-L246
func TestAnsibleVerbosityString(t *testing.T) {
	testCases := []struct {
		verbosity      int
		expectedString string
	}{
		{verbosity: -1, expectedString: ""},
		{verbosity: 0, expectedString: ""},
		{verbosity: 1, expectedString: "-v"},
		{verbosity: 2, expectedString: "-vv"},
		{verbosity: 7, expectedString: "-vvvvvvv"},
	}

	for _, tc := range testCases {
		gotString := runnerutil.AnsibleVerbosityString(tc.verbosity)
		if tc.expectedString != gotString {
			t.Fatalf("Unexpected string %v for  expected %v from verbosity %v", gotString, tc.expectedString, tc.verbosity)
		}
	}
}
