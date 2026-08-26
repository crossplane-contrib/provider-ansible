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
	"testing"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-cmp/cmp"
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
