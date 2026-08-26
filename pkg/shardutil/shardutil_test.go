package shardutil

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

func obj(ns, name string) client.Object {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

// shardOf returns the single shard that p assigns o to. A predicate that
// accepts an object into none or several shards is a bug, and fails the test.
func shardOf(t *testing.T, p func(target, total uint32) predicate.Predicate, o client.Object, total uint32) uint32 {
	t.Helper()

	got := make([]uint32, 0, 1)
	for i := uint32(0); i < total; i++ {
		if p(i, total).Create(event.CreateEvent{Object: o}) {
			got = append(got, i)
		}
	}
	if len(got) != 1 {
		t.Fatalf("predicate accepted %s/%s into %d shards, want exactly 1", o.GetNamespace(), o.GetName(), len(got))
	}
	return got[0]
}

func TestIsResourceForShardIgnoresNamespace(t *testing.T) {
	// The legacy predicate must keep hashing bare names, so that upgrading
	// does not reshuffle shards for existing cluster-scoped resources.
	a := shardOf(t, IsResourceForShard, obj("team-a", "run"), 8)
	b := shardOf(t, IsResourceForShard, obj("team-b", "run"), 8)
	if a != b {
		t.Errorf("IsResourceForShard(...): namespace changed the shard: %d vs %d", a, b)
	}
}

func TestIsNamespacedResourceForShardSeparatesNamespaces(t *testing.T) {
	// The same name in different namespaces must be able to land on different
	// shards, otherwise namespaced resources cluster onto one replica. Any
	// single pair may collide by chance, so require that not all of them do.
	names := []string{"run", "deploy", "bootstrap", "configure", "migrate", "seed"}

	same := 0
	for _, n := range names {
		a := shardOf(t, IsNamespacedResourceForShard, obj("team-a", n), 8)
		b := shardOf(t, IsNamespacedResourceForShard, obj("team-b", n), 8)
		if a == b {
			same++
		}
	}
	if same == len(names) {
		t.Error("IsNamespacedResourceForShard(...): namespace never affected the shard")
	}
}

func TestIsNamespacedResourceForShardIsStable(t *testing.T) {
	a := shardOf(t, IsNamespacedResourceForShard, obj("team-a", "run"), 8)
	b := shardOf(t, IsNamespacedResourceForShard, obj("team-a", "run"), 8)
	if a != b {
		t.Errorf("IsNamespacedResourceForShard(...): not deterministic: %d vs %d", a, b)
	}
}
