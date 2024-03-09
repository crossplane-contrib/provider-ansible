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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crossplane-contrib/provider-ansible/apis/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
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

func TestAnsibleRunPolicyInit(t *testing.T) {
	testCases := []struct {
		policy string
	}{
		{
			policy: "ObserveAndDelete",
		},
		{
			policy: "CheckWhenObserve",
		},
	}

	dir := filepath.Join(baseWorkingDir, string(uid))
	ansibleCtx, err := prepareAnsibleContext(dir)
	assert.NilError(t, err)
	defer os.RemoveAll(ansibleCtx)

	for _, tc := range testCases {
		t.Run(tc.policy, func(t *testing.T) {
			objectMeta.Annotations = map[string]string{AnnotationKeyPolicyRun: tc.policy}
			myRole := v1alpha1.Role{Name: "MyRole"}
			cr := v1alpha1.AnsibleRun{
				ObjectMeta: objectMeta,
				Spec: v1alpha1.AnsibleRunSpec{
					ForProvider: v1alpha1.AnsibleRunParameters{
						Roles: []v1alpha1.Role{myRole},
					},
				},
			}

			ps := Parameters{
				WorkingDirPath: ansibleCtx,
			}

			testRunner, err := ps.Init(ctx, &cr, nil)
			if err != nil {
				t.Fatalf("Error occurred unexpectedly: %v", err)
			}

			switch {
			case tc.policy == "ObserveAndDelete":
				if testRunner.AnsibleRunPolicy.Name != "ObserveAndDelete" {
					t.Fatalf("Unexpected policy %v expected %v", testRunner.AnsibleRunPolicy.Name, "ObserveAndDelete")
				}
			case tc.policy == "CheckWhenObserve":
				if testRunner.AnsibleRunPolicy.Name != "CheckWhenObserve" {
					t.Fatalf("Unexpected policy %v expected %v", testRunner.AnsibleRunPolicy.Name, "CheckWhenObserve")
				}
			}

		})
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()

	fakePlaybook := "fake playbook"
	run := &v1alpha1.AnsibleRun{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationKeyPolicyRun: "ObserveAndDelete",
			},
		},
		Spec: v1alpha1.AnsibleRunSpec{
			ForProvider: v1alpha1.AnsibleRunParameters{
				PlaybookInline: &fakePlaybook,
			},
		},
	}

	params := Parameters{
		RunnerBinary:          "fake-runner",
		WorkingDirPath:        dir,
		ArtifactsHistoryLimit: 3,
	}

	expectedRunner := &Runner{
		Path:                  dir,
		cmdFunc:               params.playbookCmdFunc(context.Background(), "playbook.yml", dir),
		workDir:               dir,
		AnsibleRunPolicy:      &RunPolicy{"ObserveAndDelete"},
		artifactsHistoryLimit: 3,
	}

	runner, err := params.Init(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("Unexpected Init() error: %v", err)
	}

	if diff := cmp.Diff(expectedRunner, runner, cmpopts.IgnoreUnexported(Runner{})); diff != "" {
		t.Errorf("Unexpected Runner -want, +got:\n%s\n", diff)
	}
	// check fields that couldn't be compared with cmp.Diff
	if runner.artifactsHistoryLimit != expectedRunner.artifactsHistoryLimit {
		t.Errorf("Unexpected Runner.artifactsHistoryLimit %v expected %v", runner.artifactsHistoryLimit, expectedRunner.artifactsHistoryLimit)
	}
	if runner.checkMode != expectedRunner.checkMode {
		t.Errorf("Unexpected Runner.checkMode %v expected %v", runner.checkMode, expectedRunner.checkMode)
	}
	if runner.workDir != expectedRunner.workDir {
		t.Errorf("Unexpected Runner.workDir %v expected %v", runner.workDir, expectedRunner.workDir)
	}

	expectedCmd := expectedRunner.cmdFunc(nil, false)
	cmd := runner.cmdFunc(nil, false)
	if cmd.String() != expectedCmd.String() {
		t.Errorf("Unexpected Runner.cmdFunc output %q expected %q", expectedCmd.String(), cmd.String())
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()

	runner := &Runner{
		Path: dir,
		cmdFunc: func(_ map[string]string, _ bool) *exec.Cmd {
			// echo works well for testing cause it will just print all the args and flags it doesn't recognize and return success,
			// therefore checking its output also checks the args passed to it are correct
			return exec.CommandContext(context.Background(), "echo")
		},
		AnsibleRunPolicy:      &RunPolicy{"ObserveAndDelete"},
		artifactsHistoryLimit: 3,
	}

	expectedID := "217b3830-68fa-461b-90d1-1fb87c685010"
	expectedArgs := []string{"--rotate-artifacts", "3", "--ident", expectedID}

	generateUUID = func() uuid.UUID { return uuid.MustParse(expectedID) }

	testCases := map[string]struct {
		checkMode      bool
		expectedOutput string
	}{
		"WithoutCheckMode": {
			expectedOutput: "",
		},
		"WithCheckMode": {
			checkMode:      true,
			expectedOutput: strings.Join(expectedArgs, " ") + "\n",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			runner.checkMode = tc.checkMode
			outBuf, err := runner.Run(context.Background())
			if err != nil {
				t.Fatalf("Unexpected Run() error: %v", err)
			}

			out, err := io.ReadAll(outBuf)
			if err != nil {
				t.Fatalf("Unexpected error reading command buffer: %v", err)
			}

			if string(out) != tc.expectedOutput {
				t.Errorf("Unexpected output in the command buffer %q, want %q", string(out), tc.expectedOutput)
			}
		})
	}
}

func TestExtractFailureReason(t *testing.T) {
	playbookStartEvt := `
	{
		"uuid": "63a52ed5-a403-4512-a430-c95f62fa3424",
		"event": "playbook_on_start",
		"event_data": {
			"playbook": "playbook.yml"
		}
	}
	`

	runnerFailedEvt := `
	{
		"uuid": "7097758b-1109-4fd9-af59-f545633794dd",
		"event": "runner_on_failed",
		"event_data": {
			"play": "test",
			"task": "file",
			"host": "testhost",
			"res": {"msg": "fake error"}
		}
	}
	`

	runnerUnreachableEvt := `
	{
		"uuid": "ded6289b-e557-48c1-88e1-88eb630aec21",
		"event": "runner_on_unreachable",
		"event_data": {
			"play": "test",
			"task": "Gathering Facts",
			"host": "testhost",
			"res": {"msg": "Failed to connect to the host via ssh"}
		}
	}
	`

	cases := map[string]struct {
		events         []string
		expectedReason string
	}{
		"NoEvents": {},
		"NoFailedEvents": {
			events: []string{playbookStartEvt},
		},
		"FailedEvent": {
			events:         []string{playbookStartEvt, runnerFailedEvt},
			expectedReason: `Failed on play "test", task "file", host "testhost": fake error`,
		},
		"UnreachableEvent": {
			events:         []string{playbookStartEvt, runnerUnreachableEvt},
			expectedReason: `Unreachable on play "test", task "Gathering Facts", host "testhost": Failed to connect to the host via ssh`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for i, evt := range tc.events {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", i)), []byte(evt), 0600); err != nil {
					t.Fatalf("Writing test event to file: %v", err)
				}
			}

			reason, err := extractFailureReason(context.Background(), dir)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if reason != tc.expectedReason {
				t.Errorf("Unexpected reason %v, expected %v", reason, tc.expectedReason)
			}
		})
	}
}
