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

// Package ansiblerun wires the shared AnsibleRun controller core to the
// namespaced ansible.m.crossplane.io API group.
package ansiblerun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	"github.com/spf13/afero"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/crossplane-contrib/provider-ansible/apis/namespaced/v1alpha1"
	shared "github.com/crossplane-contrib/provider-ansible/apis/shared/v1alpha1"
	"github.com/crossplane-contrib/provider-ansible/internal/ansible"
	ansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/ansibleRun"
	"github.com/crossplane-contrib/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane-contrib/provider-ansible/pkg/runnerutil"
	"github.com/crossplane-contrib/provider-ansible/pkg/shardutil"
)

const (
	errNoPCRef      = "spec.providerConfigRef is not set"
	errUnknownPCRef = "unknown provider config kind"
)

// resolveProviderConfig returns the spec of the provider config an AnsibleRun
// refers to. A namespaced ProviderConfig is read from the AnsibleRun's own
// namespace; a ClusterProviderConfig is read cluster-scoped.
//
// The shared core (internal/controller/ansibleRun/core.go) wraps whatever
// error this returns with "cannot get ProviderConfig: %w", so the messages
// here deliberately do not add that prefix themselves — doing so would
// duplicate it in the message an operator sees.
func resolveProviderConfig(ctx context.Context, kube client.Client, cr *v1alpha1.AnsibleRun) (*shared.ProviderConfigSpec, error) {
	ref := cr.GetProviderConfigReference()
	if ref == nil {
		return nil, errors.New(errNoPCRef)
	}

	switch ref.Kind {
	case v1alpha1.ProviderConfigKind:
		pc := &v1alpha1.ProviderConfig{}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: cr.GetNamespace(), Name: ref.Name}, pc); err != nil {
			return nil, err
		}
		return &pc.Spec, nil
	case v1alpha1.ClusterProviderConfigKind:
		cpc := &v1alpha1.ClusterProviderConfig{}
		if err := kube.Get(ctx, client.ObjectKey{Name: ref.Name}, cpc); err != nil {
			return nil, err
		}
		return &cpc.Spec, nil
	default:
		return nil, fmt.Errorf("%s %q", errUnknownPCRef, ref.Kind)
	}
}

// secretNamespace confines a namespaced AnsibleRun to Secrets in its own
// namespace. The second return value reports whether an explicit, differing
// secretRef.namespace is permitted; for a namespaced resource it never is.
func secretNamespace(cr *v1alpha1.AnsibleRun) (string, bool) {
	return cr.GetNamespace(), false
}

// NewScope returns the per-API-group behaviour the shared AnsibleRun
// controller core needs in order to reconcile the namespaced
// ansible.m.crossplane.io group. It is exported so the namespaced group's
// behaviour can be asserted directly rather than only through Setup, which
// needs a manager.
func NewScope(tracker resource.ModernTracker) ansiblerun.Scope[*v1alpha1.AnsibleRun] {
	return ansiblerun.Scope[*v1alpha1.AnsibleRun]{
		Track: func(ctx context.Context, cr *v1alpha1.AnsibleRun) error {
			return tracker.Track(ctx, cr)
		},
		ResolveProviderConfig: resolveProviderConfig,
		// A namespaced resource may only read Secrets in its own namespace.
		SecretNamespace: secretNamespace,
		// No BeforeObserve hook: unlike the legacy type there is no
		// deletionPolicy to force, so the runtime calls Delete and the
		// playbook runs with state=absent. Opt out with spec.managementPolicies.
		BeforeObserve: nil,
	}
}

// reconcilerOptions returns the managed.Reconciler options used to
// reconcile namespaced AnsibleRun resources, before Setup appends any
// metrics options that need a live ctrl.Manager. It is a separate,
// dependency-light function so that Setup's option set — in particular,
// that this controller enables the beta management policies feature — can
// be asserted directly by a test rather than only through Setup, which
// needs a manager.
//
// This type has no deletionPolicy field (unlike the legacy cluster-scoped
// v1 AnsibleRun), so at reconcile time crossplane-runtime routes it to
// managed.ManagementPoliciesResolver rather than
// managed.LegacyManagementPoliciesResolver. Critically,
// ManagementPoliciesResolver.ShouldDelete unconditionally returns true when
// the management-policies feature is disabled — it has no deletionPolicy to
// fall back on the way the legacy resolver does. Without
// managed.WithManagementPolicies() here, the README's documented opt-out
// (dropping "Delete" from spec.managementPolicies to skip running the
// playbook on delete) would be silently inert: the object would still be
// deleted as if every policy were still in effect.
//
// managed.WithManagementPolicies() only sets a flag on the particular
// *managed.Reconciler instance it is passed to (via that Reconciler's own
// feature.Flags), not a package- or process-wide flag, so enabling it here
// does not affect the legacy cluster-scoped controller's reconciler. That
// controller must keep the feature disabled: it relies on
// LegacyManagementPoliciesResolver deciding ShouldDelete from
// spec.deletionPolicy != Orphan, which is only reached when the feature is
// disabled.
func reconcilerOptions(c managed.TypedExternalConnector[*v1alpha1.AnsibleRun], log logging.Logger, timeout time.Duration, rec event.Recorder) []managed.ReconcilerOption {
	return []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(c),
		managed.WithLogger(log),
		managed.WithTimeout(timeout),
		managed.WithRecorder(rec),
		managed.WithManagementPolicies(),
	}
}

// Setup adds a controller that reconciles namespaced AnsibleRun managed
// resources.
func Setup(mgr ctrl.Manager, o controller.Options, s ansiblerun.SetupOptions) error {
	name := managed.ControllerName(v1alpha1.AnsibleRunGroupKind)

	fs := afero.Afero{Fs: afero.NewOsFs()}

	galaxyBinary, err := galaxyutil.GalaxyBinary()
	if err != nil {
		return err
	}
	runnerBinary, err := runnerutil.RunnerBinary()
	if err != nil {
		return err
	}

	tracker := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})

	scope := NewScope(tracker)

	c, err := ansiblerun.NewConnector(mgr.GetClient(), fs, func(dir string) ansiblerun.Params {
		return ansible.Parameters{
			WorkingDirPath:        dir,
			GalaxyBinary:          galaxyBinary,
			RunnerBinary:          runnerBinary,
			CollectionsPath:       s.AnsibleCollectionsPath,
			RolesPath:             s.AnsibleRolesPath,
			ArtifactsHistoryLimit: s.ArtifactsHistoryLimit,
		}
	}, scope)
	if err != nil {
		return err
	}

	opts := reconcilerOptions(c, o.Logger.WithValues("controller", name), s.Timeout, event.NewAPIRecorder(mgr.GetEventRecorderFor(name)))

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
		if o.MetricOptions.MRStateMetrics != nil {
			rec := statemetrics.NewMRStateRecorder(mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.AnsibleRunList{}, o.MetricOptions.PollStateMetricInterval)
			if err := mgr.Add(rec); err != nil {
				return fmt.Errorf("cannot register MR state metrics recorder for kind namespaced v1alpha1.AnsibleRunList: %w", err)
			}
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.AnsibleRunGroupVersionKind), opts...)

	// s.Shard was acquired once for this whole process before any per-API-group
	// Setup ran; this controller only filters on it.
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		For(&v1alpha1.AnsibleRun{}).
		WithEventFilter(shardutil.IsNamespacedResourceForShard(s.Shard, s.ReplicasCount)).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}
