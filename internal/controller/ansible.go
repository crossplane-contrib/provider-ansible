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

package controller

import (
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/google/uuid"
	ctrl "sigs.k8s.io/controller-runtime"

	ansiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/ansibleRun"
	clusteransiblerun "github.com/crossplane-contrib/provider-ansible/internal/controller/cluster/ansibleRun"
	clusterconfig "github.com/crossplane-contrib/provider-ansible/internal/controller/cluster/config"
)

// Setup creates all Template controllers with the supplied logger and adds them to
// the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options, s ansiblerun.SetupOptions) error {
	// Acquire this process's shard once, here, before any per-API-group Setup
	// runs. Sharding is a property of the process: N replicas divide the
	// managed resources into N shards, and every controller this process runs
	// filters on the one shard it owns. Acquiring per API group instead would
	// put one holder per group into contention for the same N shards, so with
	// more than one group and more than one replica some process would spin in
	// tryAcquireShard forever and never reach mgr.Start.
	leaser := ansiblerun.NewShardLeaser(uuid.New().String(), s.LeaseNamespace, o.Logger)
	shard, err := leaser.AcquireAndHoldShard(o, s)
	if err != nil {
		return fmt.Errorf("cannot acquire and hold shard: %w", err)
	}
	s.Shard = shard

	if err := clusterconfig.Setup(mgr, o); err != nil {
		return err
	}

	return clusteransiblerun.Setup(mgr, o, s)
}
