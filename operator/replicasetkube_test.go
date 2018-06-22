package operator

import (
	"github.com/10gen/ops-manager-kubernetes/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ReplicaSetBuilder struct {
	*v1.MongoDbReplicaSet
}

func DefaultReplicaSetBuilder() *ReplicaSetBuilder {
	spec := &v1.MongoDbReplicaSetSpec{
		Version:     "3.6.4",
		Persistent:  util.BooleanRef(false),
		Project:     "my-project",
		Credentials: "my-credentials",
		Members:     3,
	}
	rs := &v1.MongoDbReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "dublin", Namespace: "mongodb"},
		Spec:       *spec}
	return &ReplicaSetBuilder{rs}
}

func (b *ReplicaSetBuilder) SetName(name string) *ReplicaSetBuilder {
	b.Name = name
	return b
}
func (b *ReplicaSetBuilder) SetVersion(version string) *ReplicaSetBuilder {
	b.Spec.Version = version
	return b
}
func (b *ReplicaSetBuilder) SetPersistent(p *bool) *ReplicaSetBuilder {
	b.Spec.Persistent = p
	return b
}
func (b *ReplicaSetBuilder) SetMembers(m int) *ReplicaSetBuilder {
	b.Spec.Members = m
	return b
}
func (b *ReplicaSetBuilder) Build() *v1.MongoDbReplicaSet {
	return b.MongoDbReplicaSet
}

func createDeploymentFromReplicaSet(rs *v1.MongoDbReplicaSet) om.Deployment {
	helper := createStatefulHelperFromReplicaSet(rs)

	d := om.NewDeployment()
	d.MergeReplicaSet(buildReplicaSetFromStatefulSet(helper.BuildStatefulSet(), rs.Spec.ClusterName, rs.Spec.Version), nil)
	return d
}

func createStatefulHelperFromReplicaSet(sh *v1.MongoDbReplicaSet) *StatefulSetHelper {
	return defaultSetHelper().SetName(sh.Name).SetService(sh.ServiceName()).SetReplicas(sh.Spec.Members)
}
