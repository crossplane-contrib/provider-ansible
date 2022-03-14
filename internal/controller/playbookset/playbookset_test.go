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

package playbookset

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	"github.com/crossplane/provider-ansible/internal/ansible"

	"github.com/crossplane/provider-ansible/apis/v1alpha1"
)

type ErrFs struct {
	afero.Fs

	errs map[string]error
}

func (e *ErrFs) MkdirAll(path string, perm os.FileMode) error {
	if err := e.errs[path]; err != nil {
		return err
	}
	return e.Fs.MkdirAll(path, perm)
}

// Called by afero.WriteFile
func (e *ErrFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if err := e.errs[name]; err != nil {
		return nil, err
	}
	return e.Fs.OpenFile(name, flag, perm)
}

type MockPs struct {
	MockInit func(ctx context.Context) (*ansible.PbCmd, error)
}

func (ps MockPs) Init(ctx context.Context) (*ansible.PbCmd, error) {
	return ps.MockInit(ctx)
}

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")
	uid := types.UID("no-you-id")
	pbCreds := "credentials"

	type fields struct {
		kube    client.Client
		usage   resource.Tracker
		fs      afero.Afero
		ansible func(dir string, excludedFiles []string) params
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"NotPlaybookSetError": {
			reason: "We should return an error if the supplied managed resource is not a PlaybookSet",
			fields: fields{},
			args: args{
				mg: nil,
			},
			want: errors.New(errNotPlaybookSet),
		},
		"MakeDirError": {
			reason: "We should return any error encountered while making a directory for our configuration",
			fields: fields{
				fs: afero.Afero{
					Fs: &ErrFs{
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(playbookSetDir, string(uid)): errBoom},
					},
				},
			},
			args: args{
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
				},
			},
			want: errors.Wrap(errBoom, errMkdir),
		},
		"TrackUsageError": {
			reason: "We should return any error encountered while tracking ProviderConfig usage",
			fields: fields{
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return errBoom }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
			},
			args: args{
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
				},
			},
			want: errors.Wrap(errBoom, errTrackPCUsage),
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
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.PlaybookSetSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errGetPC),
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
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.PlaybookSetSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errors.New("cannot extract from environment variable when none specified"), errGetCreds),
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
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(playbookSetDir, string(uid), pbCreds): errBoom},
					},
				},
			},
			args: args{
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.PlaybookSetSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteCreds),
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
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join("/tmp", playbookSetDir, string(uid), ".git-credentials"): errBoom},
					},
				},
			},
			args: args{
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.PlaybookSetSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.PlaybookSetParameters{
							Module: "github.com/crossplane/rocks",
							Source: v1alpha1.ConfigurationSourceRemote,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWriteGitCreds),
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
						Fs:   afero.NewMemMapFs(),
						errs: map[string]error{filepath.Join(playbookSetDir, string(uid), playbookYml): errBoom},
					},
				},
			},
			args: args{
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.PlaybookSetSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.PlaybookSetParameters{
							Module: "I'm Yaml!",
							Source: v1alpha1.ConfigurationSourceInline,
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errWritePlaybookSet),
		},
		"AnsibleInitError": {
			reason: "We should return any error encountered while initializing Playbook Client",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				ansible: func(_ string, _ []string) params {
					return MockPs{
						MockInit: func(ctx context.Context) (*ansible.PbCmd, error) { return nil, errBoom },
					}
				},
			},
			args: args{
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.PlaybookSetSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errInit),
		},
		"Success": {
			reason: "We should not return an error when we successfully 'connect' to Ansible",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(_ context.Context, _ resource.Managed) error { return nil }),
				fs:    afero.Afero{Fs: afero.NewMemMapFs()},
				ansible: func(_ string, _ []string) params {
					return MockPs{
						MockInit: func(ctx context.Context) (*ansible.PbCmd, error) { return nil, nil },
					}
				},
			},
			args: args{
				mg: &v1alpha1.PlaybookSet{
					ObjectMeta: metav1.ObjectMeta{UID: uid},
					Spec: v1alpha1.PlaybookSetSpec{
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
			_, err := c.Connect(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Connect(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}
