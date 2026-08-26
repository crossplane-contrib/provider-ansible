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
	"fmt"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	coordinationv1 "k8s.io/api/coordination/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	leaseNameTemplate           = "provider-ansible-lease-%d"
	leaseDurationSeconds        = 30
	leaseRenewalInterval        = 5 * time.Second
	leaseAcquireAttemptInterval = 5 * time.Second
)

// A ShardLeaser acquires and holds the Lease that entitles this provider
// process to reconcile one shard of the managed resources.
//
// Sharding is a property of the process, not of an API group: one process
// acquires exactly one shard however many groups it serves, and every one of
// its controllers then filters on that same shard index. Leasing is therefore
// deliberately not part of Connector, which exists once per API group.
type ShardLeaser struct {
	replicaID      string
	leaseNamespace string
	logger         logging.Logger
}

// NewShardLeaser returns a ShardLeaser identifying itself as replicaID and
// writing its Leases to leaseNamespace.
func NewShardLeaser(replicaID, leaseNamespace string, logger logging.Logger) *ShardLeaser {
	return &ShardLeaser{
		replicaID:      replicaID,
		leaseNamespace: leaseNamespace,
		logger:         logger,
	}
}

func (c *ShardLeaser) generateLeaseName(index uint32) string {
	return fmt.Sprintf(leaseNameTemplate, index)
}

func (c *ShardLeaser) releaseLease(ctx context.Context, kube client.Client, index uint32) error {
	leaseName := c.generateLeaseName(index)

	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: c.leaseNamespace, Name: leaseName},
	}

	return kube.Delete(ctx, lease)
}

// Attempts to acquire or renew a lease for the current replica ID
// Returns an error when unable to obtain the lease
func (c *ShardLeaser) acquireLease(ctx context.Context, kube client.Client, index uint32) error {
	lease := &coordinationv1.Lease{}
	leaseName := c.generateLeaseName(index)
	leaseDurationSeconds := ptr.To(int32(leaseDurationSeconds))

	if err := kube.Get(ctx, client.ObjectKey{Namespace: c.leaseNamespace, Name: leaseName}, lease); err != nil {
		if !kerrors.IsNotFound(err) {
			return err
		}

		// Create a new Lease
		lease = &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      leaseName,
				Namespace: c.leaseNamespace,
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &c.replicaID,
				RenewTime:            &metav1.MicroTime{Time: time.Now()},
				LeaseDurationSeconds: leaseDurationSeconds,
			},
		}
		if err := kube.Create(ctx, lease); err != nil {
			return err
		}
		c.logger.Debug("created lease", "lease", lease)
		return nil
	}

	// Check if the lease is held by another replica and is not expired
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != c.replicaID {
		if lease.Spec.RenewTime != nil && time.Since(lease.Spec.RenewTime.Time) < time.Duration(*lease.Spec.LeaseDurationSeconds)*time.Second {
			// Lease is held by another replica and is not expired
			return fmt.Errorf("lease is still held by %s", *lease.Spec.HolderIdentity)
		}
	}

	// Update the lease to acquire it
	lease.Spec.HolderIdentity = ptr.To(c.replicaID)
	lease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
	lease.Spec.LeaseDurationSeconds = leaseDurationSeconds
	if err := kube.Update(ctx, lease); err != nil {
		if kerrors.IsConflict(err) {
			// Another replica updated the lease concurrently, retry
			return err
		}
		return fmt.Errorf("failed to update lease: %w", err)
	}

	c.logger.Debug("updated lease", "lease", lease)
	return nil
}

// AcquireAndHoldShard finds an available shard and acquires a lease for it. Will attempt to obtain one indefinitely.
// This will also start a background go-routine to renew the lease continuously and release it when the process receives a shutdown signal.
// When ReplicasCount is 1, lease acquisition is skipped and shard 0 is returned immediately.
func (c *ShardLeaser) AcquireAndHoldShard(o controller.Options, s SetupOptions) (uint32, error) {
	if s.ReplicasCount == 1 {
		return 0, nil
	}

	ctx := s.ProviderCtx

	cfg := ctrl.GetConfigOrDie()
	kube, err := client.New(cfg, client.Options{})
	if err != nil {
		return 0, err
	}

	for {
		if shard, ok := c.tryAcquireShard(ctx, kube, o, s); ok {
			return shard, nil
		}
	}
}

func (c *ShardLeaser) tryAcquireShard(ctx context.Context, kube client.Client, o controller.Options, s SetupOptions) (uint32, bool) {
	for i := uint32(0); i < s.ReplicasCount; i++ {
		if err := c.acquireLease(ctx, kube, i); err != nil {
			o.Logger.Debug("cannot acquire lease", "id", i, "err", err)
			time.Sleep(leaseAcquireAttemptInterval)
			continue
		}
		o.Logger.Debug("acquired lease", "id", i)
		go c.holdShard(ctx, kube, i, o, s)
		return i, true
	}
	return 0, false
}

func (c *ShardLeaser) holdShard(ctx context.Context, kube client.Client, i uint32, o controller.Options, s SetupOptions) {
	sigHandler := ctrl.SetupSignalHandler()
	for {
		select {
		case <-time.After(leaseRenewalInterval):
			if err := c.acquireLease(ctx, kube, i); err != nil {
				o.Logger.Info("failed to renew lease", "id", i, "err", err)
				s.ProviderCancel()
			} else {
				o.Logger.Debug("renewed lease", "id", i)
			}
		case <-sigHandler.Done():
			o.Logger.Info("controller is shutting down, releasing lease")
			if err := c.releaseLease(ctx, kube, i); err != nil {
				o.Logger.Info("failed to release lease", "lease", err)
			}
			o.Logger.Debug("released lease")
			s.ProviderCancel()
			return
		}
	}
}
