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

package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/statemetrics"
	"gopkg.in/alecthomas/kingpin.v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/crossplane-contrib/provider-ansible/apis"
	ansible "github.com/crossplane-contrib/provider-ansible/internal/controller"
	ansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/ansibleRun"
)

func main() {
	var (
		app                     = kingpin.New(filepath.Base(os.Args[0]), "Template support for Crossplane.")
		debug                   = app.Flag("debug", "Run with debug logging.").Short('d').Bool()
		ansibleCollectionsPath  = app.Flag("ansible-collections-path", "Path where ansible collections are installed.").String()
		ansibleRolesPath        = app.Flag("ansible-roles-path", "Path where role(s) exists.").String()
		syncPeriod              = app.Flag("sync", "Controller manager sync period such as 300ms, 1.5h, or 2h45m").Short('s').Default("1h").Duration()
		pollInterval            = app.Flag("poll", "Poll interval controls how often an individual resource should be checked for drift.").Default("1m").Duration()
		timeout                 = app.Flag("timeout", "Controls how long Ansible processes may run before they are killed.").Default("20m").Duration()
		leaderElection          = app.Flag("leader-election", "Use leader election for the controller manager.").Short('l').Default("false").OverrideDefaultFromEnvar("LEADER_ELECTION").Bool()
		maxReconcileRate        = app.Flag("max-reconcile-rate", "The maximum number of concurrent reconciliation operations.").Default("1").Int()
		artifactsHistoryLimit   = app.Flag("artifacts-history-limit", "Each attempt to run the playbook/role generates a set of artifacts on disk. This settings limits how many of these to keep.").Default("10").Int()
		pollStateMetricInterval = app.Flag("poll-state-metric", "State metric recording interval").Default("5s").Duration()
		replicasCount           = app.Flag("replicas", "Amount of replicas configured for the provider. When using more than 1 replica, reconciles will be sharded across them based on a modular hash.").Default("1").Int()
	)
	kingpin.MustParse(app.Parse(os.Args[1:]))

	zl := zap.New(zap.UseDevMode(*debug))
	log := logging.NewLogrLogger(zl.WithName("provider-ansible"))
	if *debug {
		// The controller-runtime runs with a no-op logger by default. It is
		// *very* verbose even at info level, so we only provide it a real
		// logger when we're running in debug mode.
		ctrl.SetLogger(zl)
	}

	log.Debug("Starting", "sync-period", syncPeriod.String())

	cfg, err := ctrl.GetConfig()
	kingpin.FatalIfError(err, "Cannot get API server rest config")

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		LeaderElection:   *leaderElection,
		LeaderElectionID: "crossplane-leader-election-provider-ansible",
		Cache: cache.Options{
			SyncPeriod: syncPeriod,
		},
	})
	kingpin.FatalIfError(err, "Cannot create controller manager")

	kingpin.FatalIfError(apis.AddToScheme(mgr.GetScheme()), "Cannot add Ansible APIs to scheme")
	mm := managed.NewMRMetricRecorder()
	sm := statemetrics.NewMRStateMetrics()
	metrics.Registry.MustRegister(mm)
	metrics.Registry.MustRegister(sm)

	o := controller.Options{
		Logger:                  log,
		MaxConcurrentReconciles: *maxReconcileRate,
		PollInterval:            *pollInterval,
		GlobalRateLimiter:       ratelimiter.NewGlobal(*maxReconcileRate),
		Features:                &feature.Flags{},
		MetricOptions: &controller.MetricOptions{
			PollStateMetricInterval: *pollStateMetricInterval,
			MRMetrics:               mm,
			MRStateMetrics:          sm,
		},
	}

	providerCtx, cancel := context.WithCancel(context.Background())
	ansibleOpts := ansiblerun.SetupOptions{
		AnsibleCollectionsPath: *ansibleCollectionsPath,
		AnsibleRolesPath:       *ansibleRolesPath,
		Timeout:                *timeout,
		ArtifactsHistoryLimit:  *artifactsHistoryLimit,
		ReplicasCount:          *replicasCount,
		ProviderCtx:            providerCtx,
		ProviderCancel:         cancel,
	}
	kingpin.FatalIfError(ansible.Setup(mgr, o, ansibleOpts), "Cannot setup Ansible controllers")
	kingpin.FatalIfError(mgr.Start(providerCtx), "Cannot start controller manager")
}
