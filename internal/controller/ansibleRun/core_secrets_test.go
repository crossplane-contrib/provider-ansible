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
	"testing"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane-contrib/provider-ansible/apis/cluster/v1alpha1"
	shared "github.com/crossplane-contrib/provider-ansible/apis/shared/v1alpha1"
)

func TestResolveSecretSelectors(t *testing.T) {
	cases := map[string]struct {
		sel           xpv2.CommonCredentialSelectors
		ns            string
		allowExplicit bool
		want          xpv2.CommonCredentialSelectors
		wantErr       bool
	}{
		"ClusterScopeLeavesExplicitNamespaceAlone": {
			sel:           xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "other", Name: "creds"}, Key: "k"}},
			ns:            "",
			allowExplicit: true,
			want:          xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "other", Name: "creds"}, Key: "k"}},
		},
		"NamespacedScopeFillsInOwnNamespace": {
			sel:           xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Name: "creds"}, Key: "k"}},
			ns:            "team-a",
			allowExplicit: false,
			want:          xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-a", Name: "creds"}, Key: "k"}},
		},
		"NamespacedScopeAcceptsMatchingNamespace": {
			sel:           xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-a", Name: "creds"}, Key: "k"}},
			ns:            "team-a",
			allowExplicit: false,
			want:          xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-a", Name: "creds"}, Key: "k"}},
		},
		"NamespacedScopeRejectsForeignNamespace": {
			sel:           xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-b", Name: "creds"}, Key: "k"}},
			ns:            "team-a",
			allowExplicit: false,
			wantErr:       true,
		},
		"NoSecretRefIsUntouched": {
			sel:           xpv2.CommonCredentialSelectors{Env: &xpv2.EnvSelector{Name: "TOKEN"}},
			ns:            "team-a",
			allowExplicit: false,
			want:          xpv2.CommonCredentialSelectors{Env: &xpv2.EnvSelector{Name: "TOKEN"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveSecretSelectors(tc.sel, tc.ns, tc.allowExplicit)
			if tc.wantErr {
				if err == nil {
					t.Fatal("ResolveSecretSelectors(...): expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSecretSelectors(...): unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ResolveSecretSelectors(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestNewConnectorRejectsIncompleteScope(t *testing.T) {
	complete := Scope[*v1alpha1.AnsibleRun]{
		Track: func(_ context.Context, _ *v1alpha1.AnsibleRun) error { return nil },
		ResolveProviderConfig: func(_ context.Context, _ client.Client, _ *v1alpha1.AnsibleRun) (*shared.ProviderConfigSpec, error) {
			return nil, nil
		},
		SecretNamespace: func(_ *v1alpha1.AnsibleRun) (string, bool) { return "", true },
		BeforeObserve:   func(_ *v1alpha1.AnsibleRun) {},
	}

	cases := map[string]struct {
		scope   Scope[*v1alpha1.AnsibleRun]
		wantErr bool
	}{
		"Complete": {
			scope: complete,
		},
		"CompleteWithoutBeforeObserve": {
			// BeforeObserve is the one optional field: the core nil-guards it.
			scope: func() Scope[*v1alpha1.AnsibleRun] { s := complete; s.BeforeObserve = nil; return s }(),
		},
		"MissingTrack": {
			scope:   func() Scope[*v1alpha1.AnsibleRun] { s := complete; s.Track = nil; return s }(),
			wantErr: true,
		},
		"MissingResolveProviderConfig": {
			scope:   func() Scope[*v1alpha1.AnsibleRun] { s := complete; s.ResolveProviderConfig = nil; return s }(),
			wantErr: true,
		},
		"MissingSecretNamespace": {
			scope:   func() Scope[*v1alpha1.AnsibleRun] { s := complete; s.SecretNamespace = nil; return s }(),
			wantErr: true,
		},
		"MissingEverything": {
			scope:   Scope[*v1alpha1.AnsibleRun]{},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := NewConnector(nil, afero.Afero{Fs: afero.NewMemMapFs()}, nil, tc.scope)
			if tc.wantErr {
				if err == nil {
					t.Fatal("NewConnector(...): expected an error naming the missing Scope fields, got none")
				}
				if c != nil {
					t.Error("NewConnector(...): expected a nil Connector alongside the error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewConnector(...): unexpected error: %v", err)
			}
			if c == nil {
				t.Error("NewConnector(...): expected a Connector, got nil")
			}
		})
	}
}
