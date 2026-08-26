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
// cluster-scoped ansible.crossplane.io API group.
package ansiblerun

import (
	"context"
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane-contrib/provider-ansible/apis/cluster/v1alpha1"
	shared "github.com/crossplane-contrib/provider-ansible/apis/shared/v1alpha1"
	"github.com/crossplane-contrib/provider-ansible/internal/ansible"
	ansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/ansibleRun"
	"github.com/crossplane-contrib/provider-ansible/pkg/galaxyutil"
	"github.com/crossplane-contrib/provider-ansible/pkg/runnerutil"
	"github.com/crossplane-contrib/provider-ansible/pkg/shardutil"
)

// NewScope returns the per-API-group behaviour the shared AnsibleRun controller
// core needs in order to reconcile the cluster-scoped ansible.crossplane.io
// group. It is exported so that the legacy group's behaviour can be asserted
// directly rather than only through Setup, which needs a manager.
func NewScope(tracker resource.LegacyTracker) ansiblerun.Scope[*v1alpha1.AnsibleRun] {
	return ansiblerun.Scope[*v1alpha1.AnsibleRun]{
		Track: func(ctx context.Context, cr *v1alpha1.AnsibleRun) error {
			return tracker.Track(ctx, cr)
		},
		ResolveProviderConfig: func(ctx context.Context, kube client.Client, cr *v1alpha1.AnsibleRun) (*shared.ProviderConfigSpec, error) {
			pc := &v1alpha1.ProviderConfig{}
			if err := kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
				return nil, err
			}
			return &pc.Spec, nil
		},
		// Cluster-scoped resources may reference a Secret in any namespace,
		// exactly as they always have.
		SecretNamespace: func(_ *v1alpha1.AnsibleRun) (string, bool) { return "", true },
		// Historic behaviour: we cannot observe the external resource, so we
		// orphan it rather than block deletion on it.
		BeforeObserve: func(cr *v1alpha1.AnsibleRun) { cr.SetDeletionPolicy(xpv2.DeletionOrphan) },
	}
}

// Setup adds a controller that reconciles AnsibleRun managed resources.
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

	tracker := resource.NewLegacyProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})

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

	opts := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(c),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithTimeout(s.Timeout),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
		if o.MetricOptions.MRStateMetrics != nil {
			stateMetricsRecorder := statemetrics.NewMRStateRecorder(mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.AnsibleRunList{}, o.MetricOptions.PollStateMetricInterval)
			if err := mgr.Add(stateMetricsRecorder); err != nil {
				return fmt.Errorf("cannot register MR state metrics recorder for kind v1alpha1.AnsibleRunList: %w", err)
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
		WithEventFilter(shardutil.IsResourceForShard(s.Shard, s.ReplicasCount)).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}
