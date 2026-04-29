package searchcontroller

import (
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
)

// findClusterSpec returns the spec.clusters[i] entry whose ClusterName matches clusterName,
// or nil if no entry matches (or spec.Clusters is unset, or clusterName is empty).
func findClusterSpec(search *searchv1.MongoDBSearch, clusterName string) *searchv1.ClusterSpec {
	if clusterName == "" || search == nil || search.Spec.Clusters == nil {
		return nil
	}
	cs := *search.Spec.Clusters
	for i := range cs {
		if cs[i].ClusterName == clusterName {
			return &cs[i]
		}
	}
	return nil
}

// readPreferenceTagsMod renders spec.clusters[i].syncSourceSelector.matchTags into mongot's
// syncSource.replicaSet.readPreferenceTags so each cluster's mongot fleet pulls only from
// matching-tag external members. NOOP when clusterName is empty (single-cluster), the cluster
// entry has no matchTags, or the cluster entry is missing.
func readPreferenceTagsMod(search *searchv1.MongoDBSearch, clusterName string) mongot.Modification {
	c := findClusterSpec(search, clusterName)
	if c == nil || c.SyncSourceSelector == nil || len(c.SyncSourceSelector.MatchTags) == 0 {
		return mongot.NOOP()
	}
	tags := make(map[string]string, len(c.SyncSourceSelector.MatchTags))
	for k, v := range c.SyncSourceSelector.MatchTags {
		tags[k] = v
	}
	return func(cfg *mongot.Config) {
		cfg.SyncSource.ReplicaSet.ReadPreferenceTags = []map[string]string{tags}
	}
}

// syncSourceHostsMod replaces the rendered ReplicaSet.HostAndPort (and Router.HostAndPort
// when present) with the explicit hosts list from spec.clusters[i].syncSourceSelector.hosts.
// NOOP when clusterName is empty, hosts is empty, or the cluster entry is missing.
// matchTags and hosts are mutually exclusive at type level (B14).
func syncSourceHostsMod(search *searchv1.MongoDBSearch, clusterName string) mongot.Modification {
	c := findClusterSpec(search, clusterName)
	if c == nil || c.SyncSourceSelector == nil || len(c.SyncSourceSelector.Hosts) == 0 {
		return mongot.NOOP()
	}
	hosts := append([]string(nil), c.SyncSourceSelector.Hosts...)
	return func(cfg *mongot.Config) {
		cfg.SyncSource.ReplicaSet.HostAndPort = hosts
		if cfg.SyncSource.Router != nil {
			cfg.SyncSource.Router.HostAndPort = hosts
		}
	}
}
