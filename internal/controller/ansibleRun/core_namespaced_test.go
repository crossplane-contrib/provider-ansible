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

// This file is deliberately in the external ansiblerun_test package rather
// than in ansiblerun alongside the other core tests. It drives the shared
// core's Connect through the *real* namespaced Scope - the one
// internal/controller/namespaced/ansibleRun.NewScope wires up in production -
// rather than through a local copy of it. A test file inside package
// ansiblerun could not import that package, because it imports the core in
// turn and Go rejects the resulting import cycle. Testing the genuine wiring
// is worth the small cost of only being able to reach the core's exported
// surface from here.

package ansiblerun_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/spf13/afero"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nsv1alpha1 "github.com/crossplane-contrib/provider-ansible/apis/namespaced/v1alpha1"
	shared "github.com/crossplane-contrib/provider-ansible/apis/shared/v1alpha1"
	"github.com/crossplane-contrib/provider-ansible/internal/ansible"
	ansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/ansibleRun"
	nsansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/namespaced/ansibleRun"
)

// noopParams is a Params that lets Connect run to completion, so that these
// tests distinguish "the credential resolved" from "the credential was
// refused" by whether Connect returns an error at all.
type noopParams struct{}

func (noopParams) Init(_ context.Context, _ *shared.AnsibleRunParameters, _ metav1.Object, _ map[string]string) (*ansible.Runner, error) {
	return nil, nil
}

func (noopParams) GalaxyInstall(_ context.Context, _ map[string]string, _ string) error {
	return nil
}

// secretRecorder is a client.Client that serves the provider configs and
// Secrets these tests need, and records every Secret it is asked for. What it
// was asked for matters as much as the returned error: a refused selector must
// never reach the API server at all.
type secretRecorder struct {
	client.Client

	pc   *nsv1alpha1.ProviderConfig
	cpc  *nsv1alpha1.ClusterProviderConfig
	read []types.NamespacedName
}

func (c *secretRecorder) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	switch o := obj.(type) {
	case *nsv1alpha1.ProviderConfig:
		*o = *c.pc
	case *nsv1alpha1.ClusterProviderConfig:
		*o = *c.cpc
	case *v1.Secret:
		// client.ObjectKey is an alias for types.NamespacedName, so key is
		// appended directly.
		c.read = append(c.read, key)
		o.Data = map[string][]byte{"credentials": []byte("secret-material")}
	}
	return nil
}

func secretSelector(ns, name string) xpv2.CommonCredentialSelectors {
	return xpv2.CommonCredentialSelectors{
		SecretRef: &xpv2.SecretKeySelector{
			SecretReference: xpv2.SecretReference{Namespace: ns, Name: name},
			Key:             "credentials",
		},
	}
}

// TestNamespacedClusterProviderConfigCredentialsAreClusterWide pins the whole
// point of relaxing the secret-namespace policy for ClusterProviderConfig, by
// running both halves of the contrast through the same code path an operator's
// cluster runs:
//
//   - A ClusterProviderConfig is cluster-scoped and administrator-authored, so
//     its OWN credentials may name any namespace. One such config therefore
//     serves AnsibleRuns in every namespace, which is what makes it usable as
//     the default providerConfigRef.kind.
//   - A namespaced AnsibleRun's OWN spec.forProvider.inventories stay confined
//     to the AnsibleRun's namespace, and a namespaced ProviderConfig's
//     credentials stay confined to its namespace. Neither is relaxed.
//
// The distinction is between what an administrator declares on a cluster-scoped
// object and what a tenant writes in their own namespaced object. Losing it in
// either direction is a silent behaviour change: too strict and a
// ClusterProviderConfig serves only one namespace again, too loose and a
// tenant's own manifest can name a foreign namespace.
func TestNamespacedClusterProviderConfigCredentialsAreClusterWide(t *testing.T) {
	const (
		runNS  = "team-a"
		credNS = "crossplane-system"
	)

	cases := map[string]struct {
		cr *nsv1alpha1.AnsibleRun

		pcCreds  []shared.ProviderCredentials
		cpcCreds []shared.ProviderCredentials

		wantErr bool
		// wantRead is the Secret the provider must end up reading. It is empty
		// when the selector must be refused before any read is attempted.
		wantRead *types.NamespacedName
		reason   string
	}{
		"ClusterProviderConfigCredentialMayNameAnotherNamespace": {
			cr: newRun(runNS, nsv1alpha1.ClusterProviderConfigKind, nil),
			cpcCreds: []shared.ProviderCredentials{{
				Filename:                  "gcp-credentials.json",
				Source:                    xpv2.CredentialsSourceSecret,
				CommonCredentialSelectors: secretSelector(credNS, "gcp-credentials"),
			}},
			wantRead: &types.NamespacedName{Namespace: credNS, Name: "gcp-credentials"},
			reason:   "A ClusterProviderConfig is cluster-scoped, so its own credentials must resolve for an AnsibleRun in any namespace; confining them would let one serve only a single namespace.",
		},
		"AnsibleRunInventoryMayNotNameAnotherNamespace": {
			cr: newRun(runNS, nsv1alpha1.ClusterProviderConfigKind, []shared.Inventory{{
				Source:                    xpv2.CredentialsSourceSecret,
				CommonCredentialSelectors: secretSelector(credNS, "hosts"),
			}}),
			wantErr: true,
			reason:  "spec.forProvider.inventories is what the tenant writes in their own AnsibleRun. It stays confined to the AnsibleRun's namespace; relaxing ClusterProviderConfig credentials must not relax this.",
		},
		"NamespacedProviderConfigCredentialMayNotNameAnotherNamespace": {
			cr: newRun(runNS, nsv1alpha1.ProviderConfigKind, nil),
			pcCreds: []shared.ProviderCredentials{{
				Filename:                  "gcp-credentials.json",
				Source:                    xpv2.CredentialsSourceSecret,
				CommonCredentialSelectors: secretSelector(credNS, "gcp-credentials"),
			}},
			wantErr: true,
			reason:  "A namespaced ProviderConfig's credentials stay confined to its own namespace. Only ClusterProviderConfig was relaxed.",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kube := &secretRecorder{
				pc: &nsv1alpha1.ProviderConfig{
					ObjectMeta: metav1.ObjectMeta{Namespace: runNS, Name: "default"},
					Spec:       shared.ProviderConfigSpec{Credentials: tc.pcCreds},
				},
				cpc: &nsv1alpha1.ClusterProviderConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "default"},
					Spec:       shared.ProviderConfigSpec{Credentials: tc.cpcCreds},
				},
			}

			scope := nsansiblerun.NewScope(resource.ModernTrackerFn(func(_ context.Context, _ resource.ModernManaged) error { return nil }))

			c, err := ansiblerun.NewConnector(kube, afero.Afero{Fs: afero.NewMemMapFs()}, func(_ string) ansiblerun.Params { return noopParams{} }, scope)
			if err != nil {
				t.Fatalf("NewConnector(...): unexpected error: %v", err)
			}

			_, err = c.Connect(context.Background(), tc.cr)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Connect(...): expected the selector naming %q to be refused for an AnsibleRun in %q, got no error.\n%s", credNS, runNS, tc.reason)
				}
				// The refusal must name the offending namespace, so an
				// operator can see which selector is at fault.
				if !strings.Contains(err.Error(), credNS) {
					t.Errorf("Connect(...): want an error naming the refused namespace %q, got: %v", credNS, err)
				}
				// A refused selector must never be rewritten and read from
				// the resource's own namespace, and must not reach the API
				// server at all.
				if len(kube.read) != 0 {
					t.Errorf("Connect(...): a refused selector must not be read at all, but these Secrets were fetched: %v", kube.read)
				}
				return
			}

			if err != nil {
				t.Fatalf("Connect(...): unexpected error: %v\n%s", err, tc.reason)
			}
			if tc.wantRead == nil {
				return
			}
			if len(kube.read) != 1 || kube.read[0] != *tc.wantRead {
				t.Errorf("Connect(...): want the Secret %s to be read, got %v.\n%s", tc.wantRead, kube.read, tc.reason)
			}
		})
	}
}

func newRun(ns, pcKind string, inv []shared.Inventory) *nsv1alpha1.AnsibleRun {
	return &nsv1alpha1.AnsibleRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "example", UID: "no-you-id"},
		Spec: nsv1alpha1.AnsibleRunSpec{
			ManagedResourceSpec: xpv2.ManagedResourceSpec{
				ProviderConfigReference: &xpv2.ProviderConfigReference{Kind: pcKind, Name: "default"},
			},
			ForProvider: shared.AnsibleRunParameters{Inventories: inv},
		},
	}
}
