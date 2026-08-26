package shardutil

import (
	"hash/fnv"

	"sigs.k8s.io/controller-runtime/pkg/client"
	event "sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Define a predicate function to filter resources based on consistent hashing
func IsResourceForShard(targetShard, totalShards uint32) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isResourceForShardHelper(e.Object, targetShard, totalShards)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isResourceForShardHelper(e.ObjectNew, targetShard, totalShards)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return isResourceForShardHelper(e.Object, targetShard, totalShards)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isResourceForShardHelper(e.Object, targetShard, totalShards)
		},
	}
}

// Helper function to check if the resource belongs to the current shard
func isResourceForShardHelper(obj client.Object, targetShard, totalShards uint32) bool {
	// Calculate a hash of the resource name
	hash := hashString(obj.GetName())
	// Perform modulo operation to determine the shard
	shard := hash % totalShards
	// Check if the shard matches the target shard
	return shard == targetShard
}

// Helper function to hash a string using FNV-1a
func hashString(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// IsNamespacedResourceForShard filters namespaced resources onto a shard. It
// hashes namespace/name rather than the bare name, so that identically named
// resources in different namespaces spread across replicas.
//
// Cluster-scoped resources keep using IsResourceForShard: switching them to
// this key would rehash every existing object and reshuffle a live
// multi-replica deployment on upgrade.
func IsNamespacedResourceForShard(targetShard, totalShards uint32) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isNamespacedResourceForShardHelper(e.Object, targetShard, totalShards)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isNamespacedResourceForShardHelper(e.ObjectNew, targetShard, totalShards)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return isNamespacedResourceForShardHelper(e.Object, targetShard, totalShards)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isNamespacedResourceForShardHelper(e.Object, targetShard, totalShards)
		},
	}
}

func isNamespacedResourceForShardHelper(obj client.Object, targetShard, totalShards uint32) bool {
	return hashString(obj.GetNamespace()+"/"+obj.GetName())%totalShards == targetShard
}
