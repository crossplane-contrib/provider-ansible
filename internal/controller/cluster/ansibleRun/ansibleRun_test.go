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

	"github.com/crossplane-contrib/provider-ansible/apis/cluster/v1alpha1"
)

// The legacy ansible.crossplane.io group must keep its runtime behaviour
// exactly. These two Scope fields are the ones whose loss would silently change
// it, so they are asserted against the Scope that Setup actually wires up.

func TestNewScopeBeforeObserveOrphans(t *testing.T) {
	scope := NewScope(nil, nil)

	if scope.BeforeObserve == nil {
		t.Fatal("NewScope(...).BeforeObserve: must not be nil; without it the legacy group would stop orphaning its external resource on delete")
	}

	cr := &v1alpha1.AnsibleRun{}
	scope.BeforeObserve(cr)

	if got := cr.GetDeletionPolicy(); got != xpv2.DeletionOrphan {
		t.Errorf("NewScope(...).BeforeObserve(cr): deletion policy: want %q, got %q", xpv2.DeletionOrphan, got)
	}
}

func TestNewScopeSecretNamespaceAllowsAnyNamespace(t *testing.T) {
	scope := NewScope(nil, nil)

	if scope.SecretNamespace == nil {
		t.Fatal("NewScope(...).SecretNamespace: must not be nil")
	}

	ns, allowExplicit := scope.SecretNamespace(&v1alpha1.AnsibleRun{})

	if ns != "" {
		t.Errorf("NewScope(...).SecretNamespace(cr): namespace: want %q, got %q", "", ns)
	}
	if !allowExplicit {
		t.Error("NewScope(...).SecretNamespace(cr): allowExplicit: want true, got false; cluster-scoped resources must keep reading Secrets in any namespace")
	}
}
