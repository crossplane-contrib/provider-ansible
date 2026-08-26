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
	"reflect"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/crossplane-contrib/provider-ansible/apis/namespaced/v1alpha1"
	shared "github.com/crossplane-contrib/provider-ansible/apis/shared/v1alpha1"
	ansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/ansibleRun"
)

func run(ns, name, pcKind, pcName string) *v1alpha1.AnsibleRun {
	return &v1alpha1.AnsibleRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1alpha1.AnsibleRunSpec{
			ManagedResourceSpec: xpv2.ManagedResourceSpec{
				ProviderConfigReference: &xpv2.ProviderConfigReference{Kind: pcKind, Name: pcName},
			},
		},
	}
}

// TestResolveProviderConfig pins both halves of what resolveProviderConfig
// returns: where each provider config kind is read from, and - critically -
// the secret-namespace policy that travels with it.
//
// AllowExplicitSecretNamespace is the security seam. It must stay false for a
// namespaced ProviderConfig, whose credentials are confined to the
// AnsibleRun's namespace, and true for a cluster-scoped
// ClusterProviderConfig, whose credentials an administrator writes once to
// serve every namespace. Flipping either value silently changes what Secrets
// the provider will read, which is why each case asserts it explicitly rather
// than relying on the zero value.
func TestResolveProviderConfig(t *testing.T) {
	reqs := "collections:\n  - name: community.general\n"
	spec := shared.ProviderConfigSpec{Requirements: &reqs}

	cases := map[string]struct {
		cr      *v1alpha1.AnsibleRun
		get     func(t *testing.T) test.MockGetFn
		want    ansiblerun.ResolvedProviderConfig
		wantErr bool
	}{
		"NamespacedConfigIsReadFromTheResourceNamespace": {
			cr: run("team-a", "r", v1alpha1.ProviderConfigKind, "pc"),
			get: func(t *testing.T) test.MockGetFn {
				return func(_ context.Context, key client.ObjectKey, o client.Object) error {
					if key.Namespace != "team-a" || key.Name != "pc" {
						t.Errorf("Get(...): want team-a/pc, got %q/%q", key.Namespace, key.Name)
					}
					pc, ok := o.(*v1alpha1.ProviderConfig)
					if !ok {
						t.Fatalf("Get(...): want *ProviderConfig, got %T", o)
					}
					pc.Spec = spec
					return nil
				}
			},
			// A namespaced ProviderConfig lives in the AnsibleRun's own
			// namespace, so its credentials stay confined to it.
			want: ansiblerun.ResolvedProviderConfig{Spec: &spec, AllowExplicitSecretNamespace: false},
		},
		"ClusterConfigIsReadWithoutANamespace": {
			cr: run("team-a", "r", v1alpha1.ClusterProviderConfigKind, "cpc"),
			get: func(t *testing.T) test.MockGetFn {
				return func(_ context.Context, key client.ObjectKey, o client.Object) error {
					if key.Namespace != "" || key.Name != "cpc" {
						t.Errorf("Get(...): want cluster-scoped cpc, got %q/%q", key.Namespace, key.Name)
					}
					cpc, ok := o.(*v1alpha1.ClusterProviderConfig)
					if !ok {
						t.Fatalf("Get(...): want *ClusterProviderConfig, got %T", o)
					}
					cpc.Spec = spec
					return nil
				}
			},
			// A ClusterProviderConfig is cluster-scoped and written by an
			// administrator, so its own credentials may name any namespace
			// and one config serves AnsibleRuns everywhere.
			want: ansiblerun.ResolvedProviderConfig{Spec: &spec, AllowExplicitSecretNamespace: true},
		},
		"UnknownKindIsRejected": {
			cr: run("team-a", "r", "Nonsense", "pc"),
			get: func(t *testing.T) test.MockGetFn {
				return func(_ context.Context, _ client.ObjectKey, _ client.Object) error {
					t.Error("Get(...): must not be called for an unknown provider config kind")
					return nil
				}
			},
			wantErr: true,
		},
		"MissingReferenceIsRejected": {
			cr: &v1alpha1.AnsibleRun{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "r"}},
			get: func(t *testing.T) test.MockGetFn {
				return func(_ context.Context, _ client.ObjectKey, _ client.Object) error {
					t.Error("Get(...): must not be called when providerConfigRef is unset")
					return nil
				}
			},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kube := &test.MockClient{MockGet: tc.get(t)}

			got, err := resolveProviderConfig(context.Background(), kube, tc.cr)
			if tc.wantErr {
				if err == nil {
					t.Fatal("resolveProviderConfig(...): expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProviderConfig(...): unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("resolveProviderConfig(...): -want, +got:\n%s", diff)
			}
			// Asserted separately from the diff above so a regression names
			// the policy that changed rather than only showing a struct diff.
			if got.AllowExplicitSecretNamespace != tc.want.AllowExplicitSecretNamespace {
				t.Errorf("resolveProviderConfig(...).AllowExplicitSecretNamespace: want %t, got %t; this decides whether this provider config's own credentials may name a namespace other than the AnsibleRun's",
					tc.want.AllowExplicitSecretNamespace, got.AllowExplicitSecretNamespace)
			}
		})
	}
}

// TestSecretNamespaceConfinesToOwnNamespace covers what a namespaced
// AnsibleRun declares for itself - spec.forProvider.inventories. It is
// deliberately unaffected by the ClusterProviderConfig relaxation: the
// AnsibleRun below references a ClusterProviderConfig and is still confined.
// A provider config's own credentials are governed by
// ResolvedProviderConfig.AllowExplicitSecretNamespace instead, asserted in
// TestResolveProviderConfig.
func TestSecretNamespaceConfinesToOwnNamespace(t *testing.T) {
	ns, allowExplicit := secretNamespace(run("team-a", "r", v1alpha1.ClusterProviderConfigKind, "cpc"))

	if ns != "team-a" {
		t.Errorf("secretNamespace(...): want team-a, got %q", ns)
	}
	if allowExplicit {
		t.Error("secretNamespace(...): a namespaced resource's own selectors must never be allowed a foreign secretRef namespace")
	}
}

// TestSecretNamespacePolicyComposesWithResolveSecretSelectors feeds
// secretNamespace's two return values straight into the core's
// ResolveSecretSelectors, exactly as the shared reconcile loop does for
// spec.forProvider.inventories at internal/controller/ansibleRun/core.go.
// Testing each half in isolation (secretNamespace above,
// ResolveSecretSelectors in the core's own TestResolveSecretSelectors) does
// not pin the composition: this test is what actually stops a namespaced
// AnsibleRun's own selectors from ever reading a Secret outside its
// namespace, whichever kind of provider config it references.
func TestSecretNamespacePolicyComposesWithResolveSecretSelectors(t *testing.T) {
	cr := run("team-a", "r", v1alpha1.ClusterProviderConfigKind, "cpc")
	ns, allowExplicit := secretNamespace(cr)

	cases := map[string]struct {
		sel     xpv2.CommonCredentialSelectors
		want    xpv2.CommonCredentialSelectors
		wantErr bool
	}{
		"EmptyNamespaceIsFilledInWithTheResourceNamespace": {
			sel:  xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Name: "creds"}, Key: "k"}},
			want: xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-a", Name: "creds"}, Key: "k"}},
		},
		"MatchingNamespaceIsAcceptedUnchanged": {
			sel:  xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-a", Name: "creds"}, Key: "k"}},
			want: xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-a", Name: "creds"}, Key: "k"}},
		},
		"ForeignNamespaceIsRejectedNotRewritten": {
			sel:     xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: "team-b", Name: "creds"}, Key: "k"}},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ansiblerun.ResolveSecretSelectors(tc.sel, ns, allowExplicit)
			if tc.wantErr {
				if err == nil {
					t.Fatal("ResolveSecretSelectors(secretNamespace(cr)): expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSecretSelectors(secretNamespace(cr)): unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ResolveSecretSelectors(secretNamespace(cr)): -want, +got:\n%s", diff)
			}
		})
	}
}

// The namespaced ansible.m.crossplane.io group must confine what an AnsibleRun
// declares for itself to its own namespace, so all four fields of the Scope
// that Setup actually wires up are asserted here. SecretNamespace gets a value
// assertion because its loss would silently grant a namespaced AnsibleRun's
// own selectors cross-namespace Secret reads. The one deliberate exception -
// a cluster-scoped ClusterProviderConfig's own credentials - is carried by
// ResolveProviderConfig, not by SecretNamespace, and is asserted in
// TestResolveProviderConfig.

func TestNewScopeIsComplete(t *testing.T) {
	scope := NewScope(nil)

	if scope.Track == nil {
		t.Error("NewScope(...).Track: must not be nil")
	}
	if scope.ResolveProviderConfig == nil {
		t.Error("NewScope(...).ResolveProviderConfig: must not be nil")
	}
	if scope.SecretNamespace == nil {
		t.Error("NewScope(...).SecretNamespace: must not be nil")
	}
	if scope.BeforeObserve != nil {
		t.Error("NewScope(...).BeforeObserve: want nil; the namespaced type has no deletionPolicy to force")
	}
}

func TestNewScopeSecretNamespaceConfinesToOwnNamespace(t *testing.T) {
	scope := NewScope(nil)

	if scope.SecretNamespace == nil {
		t.Fatal("NewScope(...).SecretNamespace: must not be nil")
	}

	ns, allowExplicit := scope.SecretNamespace(run("team-a", "r", v1alpha1.ClusterProviderConfigKind, "cpc"))

	if ns != "team-a" {
		t.Errorf("NewScope(...).SecretNamespace(cr): namespace: want %q, got %q", "team-a", ns)
	}
	if allowExplicit {
		t.Error("NewScope(...).SecretNamespace(cr): allowExplicit: want false, got true; a namespaced resource's own selectors must never be allowed a foreign secretRef namespace")
	}
}

// TestReconcilerOptionsEnablesManagementPolicies guards against a specific
// regression: this type has no deletionPolicy, so at reconcile time
// crossplane-runtime resolves ShouldDelete via
// managed.ManagementPoliciesResolver, not the legacy resolver. That
// resolver's Validate rejects any non-default spec.managementPolicies
// outright while the management-policies feature is disabled, and Validate
// runs before ShouldDelete is ever consulted. Without
// managed.WithManagementPolicies() among this controller's reconciler
// options, the README's documented `managementPolicies` opt-out (dropping
// "Delete" to skip running the playbook on delete) would not be silently
// inert — it would surface as a ManagementPolicyInvalid warning event and a
// ReconcileError condition on every reconcile, and because the reconciler
// returns before ShouldDelete is reached, the finalizer would never be
// removed and the delete would hang. See the comment on reconcilerOptions
// for the full explanation, including why the cluster-scoped controller must
// NOT enable this feature.
//
// ReconcilerOption is just a func(*managed.Reconciler), so options can only
// be told apart by comparing the underlying function pointer of a
// freshly-obtained managed.WithManagementPolicies() value against each
// option actually returned. managed.WithManagementPolicies takes no
// arguments and closes over nothing, so the function value it returns has a
// stable code pointer across calls, making this comparison reliable.
func TestReconcilerOptionsEnablesManagementPolicies(t *testing.T) {
	opts := reconcilerOptions(nil, logging.NewNopLogger(), time.Minute, event.NewNopRecorder())

	want := reflect.ValueOf(managed.WithManagementPolicies()).Pointer()

	found := false
	for _, o := range opts {
		if reflect.ValueOf(o).Pointer() == want {
			found = true
			break
		}
	}
	if !found {
		t.Error("reconcilerOptions(...): missing managed.WithManagementPolicies(); the documented managementPolicies Delete opt-out would surface as a ReconcileError and block deletion, not silently no-op, without it")
	}
}
