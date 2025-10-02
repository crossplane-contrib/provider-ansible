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

package ansiblerun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"errors"
	"fmt"
	"io"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	"github.com/crossplane-contrib/provider-ansible/apis/v1alpha1"
	"github.com/crossplane-contrib/provider-ansible/internal/ansible"
	"github.com/crossplane-contrib/provider-ansible/pkg/runnerutil"
)

const (
	uid = types.UID("no-you-id")
)

type ErrFs struct {
	afero.Fs
	mkdirErrs map[string]error
	writeErrs map[string]error
	chmodErrs map[string]error
}

func (e *ErrFs) MkdirAll(path string, perm os.FileMode) error {
	if err := e.mkdirErrs[path]; err != nil {
		return err
	}
	return e.Fs.MkdirAll(path, perm)
}

// Called by afero.WriteFile
func (e *ErrFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if err := e.writeErrs[name]; err != nil {
		return nil, err
	}
	return e.Fs.OpenFile(name, flag, perm)
}

func (e *ErrFs) Chmod(name string, mode os.FileMode) error {
	if err := e.chmodErrs[name]; err != nil {
		return err
	}
	return e.Fs.Chmod(name, mode)
}

type MockPs struct {
	MockInit          func(ctx context.Context, cr *v1alpha1.AnsibleRun, behaviorVars map[string]string) (*ansible.Runner, error)
	MockGalaxyInstall func(ctx context.Context, behaviorVars map[string]string, requirementsType string) error
	MockAddFile       func(path string, content []byte) error
}

func (ps MockPs) Init(ctx context.Context, cr *v1alpha1.AnsibleRun, behaviorVars map[string]string) (*ansible.Runner, error) {
	return ps.MockInit(ctx, cr, behaviorVars)
}

func (ps MockPs) GalaxyInstall(ctx context.Context, behaviorVars map[string]string, requirementsType string) error {
	return ps.MockGalaxyInstall(ctx, behaviorVars, requirementsType)
}

func (ps MockPs) AddFile(path string, content []byte) error {
	return ps.MockAddFile(path, content)
}

type MockRunner struct {
	MockRun              func(ctx context.Context) (io.Reader, error)
	MockWriteExtraVar    func(extraVar map[string]interface{}) error
	MockAnsibleRunPolicy func() *ansible.RunPolicy
	MockEnableCheckMode  func(checkMode bool)
	MockFailureReason    func() (string, error)
}

func (r MockRunner) Run(ctx context.Context) (io.Reader, error) {
	return r.MockRun(ctx)
}

func (r MockRunner) WriteExtraVar(extraVar map[string]interface{}) error {
	return r.MockWriteExtraVar(extraVar)
}

func (r MockRunner) GetAnsibleRunPolicy() *ansible.RunPolicy {
	return r.MockAnsibleRunPolicy()
}

func (r MockRunner) EnableCheckMode(checkMode bool) {
	r.MockEnableCheckMode(checkMode)
}

func (r MockRunner) FailureReason() (string, error) {
	return r.MockFailureReason()
}

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")
	pbCreds := "credentials"
	requirements := "fakeRequirements"
	inlineYaml := "IamYaml"
	myRole := v1alpha1.Role{Name: "MyRole"}

	type fields struct {
		kube    client.Client
		usage   resource.Tracker
		fs      afero.Afero
		ansible func(dir string) params
	}

	type args struct {
		ctx context.Context
		cr  *v1alpha1.AnsibleRun
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"MakeDirError": {
			reason: "We should return any error encountered while making a directory for our configuration",
			fields: fields{
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:        afero.NewMemMapFs(),
						mkdirErrs: map[string]error{filepath.Join(baseWorkingDir, string(uid)): errBoom},
					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
				},
			},
			want: fmt.Errorf("%s: %s: %w", baseWorkingDir, errMkdir, errBoom),
		},
		"TrackUsageError": {
			reason: "We should return any error encountered while tracking ProviderConfig usage",
			fields: fields{
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return errBoom }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
				},
			},
			want: fmt.Errorf("%s: %w", errTrackPCUsage, errBoom),
		},
		"GetProviderConfigError": {
			reason: "We should return any error encountered while getting our ProviderConfig",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(errBoom),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: fmt.Errorf("%s: %w", errGetPC, errBoom),
		},
		"GetProviderConfigCredentialsError": {
			reason: "We should return any error encountered while getting our ProviderConfig credentials",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1alpha1.ProviderConfig); ok {
							// We're testing through CommonCredentialsExtractor
							// here. We cause an error to be returned by asking
							// for credentials from the environment, but not
							// specifying an environment variable.
							pc.Spec.Credentials = []v1alpha1.ProviderCredentials{{
								Source: xpv1.CredentialsSourceEnvironment,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: fmt.Errorf("%s: %w", errGetCreds, errors.New("cannot extract from environment variable when none specified")),
		},
		"WriteProviderConfigCredentialsError": {
			reason: "We should return any error encountered while writing our ProviderConfig credentials to a file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1alpha1.ProviderConfig); ok {
							pc.Spec.Credentials = []v1alpha1.ProviderCredentials{{
								Filename: pbCreds,
								Source:   xpv1.CredentialsSourceNone,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:        afero.NewMemMapFs(),
						writeErrs: map[string]error{filepath.Join(baseWorkingDir, string(uid), pbCreds): errBoom},
					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: fmt.Errorf("%s: %w", errWriteCreds, errBoom),
		},
		"WriteProviderGitCredentialsError": {
			reason: "We should return any error encountered while writing our git credentials to a file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1alpha1.ProviderConfig); ok {
							pc.Spec.Credentials = []v1alpha1.ProviderCredentials{{
								Filename: ".git-credentials",
								Source:   xpv1.CredentialsSourceNone,
							}}
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:        afero.NewMemMapFs(),
						writeErrs: map[string]error{filepath.Join("/tmp", baseWorkingDir, string(uid), ".git-credentials"): errBoom},
					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.AnsibleRunParameters{
							Roles: []v1alpha1.Role{
								myRole,
							},
						},
					},
				},
			},
			want: fmt.Errorf("%s: %w", errWriteGitCreds, errBoom),
		},
		"WritePlaybookError": {
			reason: "We should return any error encountered while writing our playbook.yml file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:        afero.NewMemMapFs(),
						writeErrs: map[string]error{filepath.Join(baseWorkingDir, string(uid), runnerutil.PlaybookYml): errBoom},
					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.AnsibleRunParameters{
							PlaybookInline: &inlineYaml,
						},
					},
				},
			},
			want: fmt.Errorf("%s: %w", errWriteAnsibleRun, errBoom),
		},
		"WriteInventoryError": {
			reason: "We should return any error encountered while writing our Inventory file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:        afero.NewMemMapFs(),
						writeErrs: map[string]error{filepath.Join(baseWorkingDir, string(uid), runnerutil.Hosts): errBoom},
					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.AnsibleRunParameters{
							InventoryInline: &inlineYaml,
						},
					},
				},
			},
			want: fmt.Errorf("%s %s: %w", errWriteInventory, runnerutil.Hosts, errBoom),
		},
		"ChmodInventoryError": {
			reason: "We should return any error encountered while changing permissions on our Inventory file",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:        afero.NewMemMapFs(),
						chmodErrs: map[string]error{filepath.Join(baseWorkingDir, string(uid), runnerutil.Hosts): errBoom},
					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.AnsibleRunParameters{
							InventoryInline: &inlineYaml,
						},
					},
				},
			},
			want: fmt.Errorf("%s %s: %w", errChmodInventory, runnerutil.Hosts, errBoom),
		},
		"AnsibleInitError": {
			reason: "We should return any error encountered while initializing ansible-runner cli",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				ansible: func(_ string) params {
					return MockPs{
						MockInit: func(_ context.Context, cr *v1alpha1.AnsibleRun, behaviorVars map[string]string) (*ansible.Runner, error) {
							return nil, errBoom
						},
						MockGalaxyInstall: func(_ context.Context, _ map[string]string, requirementsType string) error {
							return nil
						},
						MockAddFile: func(_ string, _ []byte) error {
							return nil
						},
					}
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: fmt.Errorf("%s: %w", errInit, errBoom),
		},
		"AnsibleGalaxyError": {
			reason: "We should return any error encountered while installing ansible requirements",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						if pc, ok := obj.(*v1alpha1.ProviderConfig); ok {
							pc.Spec.Requirements = &requirements
						}
						return nil
					}),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				ansible: func(_ string) params {
					return MockPs{
						MockInit: func(_ context.Context, _ *v1alpha1.AnsibleRun, _ map[string]string) (*ansible.Runner, error) {
							return nil, nil
						},
						MockGalaxyInstall: func(_ context.Context, _ map[string]string, _ string) error {
							return errBoom
						},
						MockAddFile: func(_ string, _ []byte) error {
							return nil
						},
					}
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errBoom,
		},
		"Success": {
			reason: "We should not return an error when we successfully 'connect' to Ansible",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				ansible: func(_ string) params {
					return MockPs{
						MockInit: func(_ context.Context, _ *v1alpha1.AnsibleRun, _ map[string]string) (*ansible.Runner, error) {
							return nil, nil
						},
						MockGalaxyInstall: func(_ context.Context, _ map[string]string, _ string) error {
							return nil
						},
						MockAddFile: func(_ string, _ []byte) error {
							return nil
						},
					}
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.AnsibleRunSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := connector{
				kube:    tc.fields.kube,
				usage:   tc.fields.usage,
				fs:      tc.fields.fs,
				ansible: tc.fields.ansible,
			}
			_, err := c.Connect(tc.args.ctx, tc.args.cr)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Connect(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		kube   client.Client
		runner ansibleRunner
	}

	type args struct {
		ctx context.Context
		cr  *v1alpha1.AnsibleRun
	}

	type want struct {
		o          managed.ExternalObservation
		err        error
		conditions []xpv1.Condition
	}

	testPlaybook := "fake playbook"
	testRun := v1alpha1.AnsibleRun{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				v1.LastAppliedConfigAnnotation: fmt.Sprintf(`{"playbookInline":"%s"}`, testPlaybook),
			},
		},
		Spec: v1alpha1.AnsibleRunSpec{
			ForProvider: v1alpha1.AnsibleRunParameters{
				PlaybookInline: &testPlaybook,
			},
		},
	}

	testRunWithReconcileSuccess := testRun.DeepCopy()
	testRunWithReconcileSuccess.SetConditions(xpv1.ReconcileSuccess())

	testRunWithReconcileError := testRun.DeepCopy()
	testRunWithReconcileError.SetConditions(xpv1.ReconcileError(errors.New("fake error")))

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"PolicyNotSupported": {
			reason: "We should do no action if the supplied AnsibleRunPolicy is not supported",
			args: args{
				cr: &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				runner: &ansible.Runner{
					AnsibleRunPolicy: &ansible.RunPolicy{
						Name: "LOL",
					},
				},
			},
			want: want{},
		},
		"GetObservedErrorWhenObserveAndDeletePolicy": {
			reason: "We should return any error we encounter getting observed resource",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(errBoom),
				},
				runner: &ansible.Runner{
					AnsibleRunPolicy: &ansible.RunPolicy{
						Name: "ObserveAndDelete",
					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{},
			},
			want: want{
				err:        fmt.Errorf("%s: %w", errGetAnsibleRun, errBoom),
				conditions: []xpv1.Condition{xpv1.Unavailable()},
			},
		},
		"UnchangedWithObserveAndDeletePolicy": {
			reason: "We should not run ansible when spec has not changed and last sync was successful",
			fields: fields{
				kube: &test.MockClient{
					MockGet:          test.NewMockGetFn(nil),
					MockUpdate:       test.NewMockUpdateFn(nil),
					MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
				},
				runner: &MockRunner{
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "ObserveAndDelete",
						}
					},
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return nil
					},
					MockRun: func(ctx context.Context) (io.Reader, error) {
						return nil, fmt.Errorf("run should not have been called")
					},
				},
			},
			args: args{
				cr: testRunWithReconcileSuccess,
			},
			want: want{
				o: managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
			},
		},
		"RetryFailedWithObserveAndDeletePolicy": {
			reason: "We should run ansible when spec has not changed but last sync was unsuccessful",
			fields: fields{
				kube: &test.MockClient{
					MockGet:          test.NewMockGetFn(nil),
					MockUpdate:       test.NewMockUpdateFn(nil),
					MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
				},
				runner: &MockRunner{
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "ObserveAndDelete",
						}
					},
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return nil
					},
					MockRun: func(ctx context.Context) (io.Reader, error) {
						cmd := exec.Command("ls") //nolint:noctx
						cmd.Start()
						return nil, cmd.Wait()
					},
				},
			},
			args: args{
				cr: testRunWithReconcileError,
			},
			want: want{
				o: managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
			},
		},
		"GetObservedErrorWhenCheckWhenObservePolicy": {
			reason: "We should return any error we encounter getting observed resource",
			fields: fields{
				runner: &MockRunner{
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "CheckWhenObserve",
						}
					},
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return nil
					},
					MockRun: func(context.Context) (io.Reader, error) {
						return nil, errBoom
					},
					MockEnableCheckMode: func(checkMode bool) {

					},
				},
			},
			args: args{
				cr: &v1alpha1.AnsibleRun{},
			},
			want: want{
				err: errBoom,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{runner: tc.fields.runner, kube: tc.fields.kube}
			got, err := e.Observe(tc.args.ctx, tc.args.cr)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreateOrUpdate(t *testing.T) {
	errBoom := errors.New("boom")
	unavaliableCond := xpv1.Unavailable()
	unavaliableCond.Message = errBoom.Error()

	type fields struct {
		kube   client.Client
		runner ansibleRunner
	}

	type args struct {
		ctx context.Context
		cr  *v1alpha1.AnsibleRun
	}

	type want struct {
		o          managed.ExternalCreation
		err        error
		conditions []xpv1.Condition
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"RunErrorWithObserveAndDeletePolicy": {
			reason: "We should return any error we encounter when running the runner",
			args: args{
				cr: &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				kube: &test.MockClient{
					MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
				},
				runner: &MockRunner{
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "ObserveAndDelete",
						}
					},
					MockEnableCheckMode: func(checkMode bool) {},
					MockRun: func(context.Context) (io.Reader, error) {
						return nil, errBoom
					},
				},
			},
			want: want{
				err:        fmt.Errorf("running ansible: %w", errBoom),
				conditions: []xpv1.Condition{unavaliableCond},
			},
		},
		"SuccessObserveAndDelete": {
			reason: "We should not return an error when we successfully delete the AnsibleRun resource",
			args: args{
				ctx: context.Background(),
				cr:  &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				kube: &test.MockClient{
					MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
				},
				runner: &MockRunner{
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "ObserveAndDelete",
						}
					},
					MockEnableCheckMode: func(checkMode bool) {},
					MockRun: func(ctx context.Context) (io.Reader, error) {
						cmd := exec.CommandContext(ctx, "ls")
						cmd.Start()
						return nil, cmd.Wait()
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{xpv1.Available()},
			},
		},
		"RunErrorWithCheckWhenObservePolicy": {
			reason: "We should return any error we encounter when running the runner",
			args: args{
				ctx: context.Background(),
				cr:  &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				kube: &test.MockClient{
					MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
				},
				runner: &MockRunner{
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "CheckWhenObserve",
						}
					},
					MockEnableCheckMode: func(checkMode bool) {},
					MockRun: func(context.Context) (io.Reader, error) {
						return nil, errBoom
					},
				},
			},
			want: want{
				err:        fmt.Errorf("running ansible: %w", errBoom),
				conditions: []xpv1.Condition{unavaliableCond},
			},
		},
		"SuccessCheckWhenObserve": {
			reason: "We should not return an error when we successfully delete the AnsibleRun resource",
			args: args{
				ctx: context.Background(),
				cr:  &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				kube: &test.MockClient{
					MockStatusUpdate: test.NewMockSubResourceUpdateFn(nil),
				},
				runner: &MockRunner{
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "CheckWhenObserve",
						}
					},
					MockEnableCheckMode: func(checkMode bool) {},
					MockRun: func(ctx context.Context) (io.Reader, error) {
						cmd := exec.CommandContext(ctx, "ls")
						cmd.Start()
						return nil, cmd.Wait()
					},
				},
			},
			want: want{
				conditions: []xpv1.Condition{xpv1.Available()},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{runner: tc.fields.runner, kube: tc.fields.kube}
			got, err := e.Create(tc.args.ctx, tc.args.cr)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}

			if tc.args.cr == nil {
				return
			}

			if diff := cmp.Diff(
				tc.want.conditions,
				tc.args.cr.Status.Conditions,
				cmpopts.IgnoreFields(xpv1.Condition{}, "LastTransitionTime"),
			); diff != "" {
				t.Errorf("ansiblerun conditions: (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		kube   client.Client
		runner ansibleRunner
	}

	type args struct {
		ctx context.Context
		cr  *v1alpha1.AnsibleRun
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"writeExtraVarErrorWithObserveAndDeletePolicy": {
			reason: "We should return any error we encounter writing env variable env/extravars",
			args: args{
				cr: &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				runner: &MockRunner{
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return errBoom
					},
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "ObserveAndDelete",
						}
					},
				},
			},
			want: errBoom,
		},
		"RunErrorWithObserveAndDeletePolicy": {
			reason: "We should return any error we encounter when running the runner",
			args: args{
				ctx: context.Background(),
				cr:  &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				runner: &MockRunner{
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return nil
					},
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "ObserveAndDelete",
						}
					},
					MockRun: func(context.Context) (io.Reader, error) {
						return nil, errBoom
					},
				},
			},
			want: errBoom,
		},
		"SuccessObserveAndDelete": {
			reason: "We should not return an error when we successfully delete the AnsibleRun resource",
			args: args{
				ctx: context.Background(),
				cr:  &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				runner: &MockRunner{
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return nil
					},
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "ObserveAndDelete",
						}
					},
					MockRun: func(ctx context.Context) (io.Reader, error) {
						cmd := exec.CommandContext(ctx, "ls")
						cmd.Start()
						return nil, cmd.Wait()
					},
				},
			},
			want: nil,
		},
		"RunErrorWithCheckWhenObservePolicy": {
			reason: "We should return any error we encounter when running the runner",
			args: args{
				ctx: context.Background(),
				cr:  &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				runner: &MockRunner{
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return nil
					},
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "CheckWhenObserve",
						}
					},
					MockRun: func(context.Context) (io.Reader, error) {
						return nil, errBoom
					},
				},
			},
			want: errBoom,
		},
		"SuccessCheckWhenObserve": {
			reason: "We should not return an error when we successfully delete the AnsibleRun resource",
			args: args{
				ctx: context.Background(),
				cr:  &v1alpha1.AnsibleRun{},
			},
			fields: fields{
				runner: &MockRunner{
					MockWriteExtraVar: func(extraVar map[string]interface{}) error {
						return nil
					},
					MockAnsibleRunPolicy: func() *ansible.RunPolicy {
						return &ansible.RunPolicy{
							Name: "CheckWhenObserve",
						}
					},
					MockRun: func(ctx context.Context) (io.Reader, error) {
						cmd := exec.CommandContext(ctx, "ls")
						cmd.Start()
						return nil, cmd.Wait()
					},
				},
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{runner: tc.fields.runner, kube: tc.fields.kube}
			_, err := e.Delete(tc.args.ctx, tc.args.cr)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}
