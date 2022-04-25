package mdbmulti

import (
	"math/rand"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MultiReplicaSetBuilder struct {
	*MongoDBMulti
}

func DefaultMultiReplicaSetBuilder() *MultiReplicaSetBuilder {
	spec := MongoDBMultiSpec{
		Version:                 "5.0.0",
		DuplicateServiceObjects: util.BooleanRef(false),
		Persistent:              util.BooleanRef(false),
		ConnectionSpec: mdbv1.ConnectionSpec{
			OpsManagerConfig: &mdbv1.PrivateCloudConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{
					Name: mock.TestProjectConfigMapName,
				},
			},
			Credentials: mock.TestCredentialsSecretName,
		},
		ResourceType: mdbv1.ReplicaSet,
		Security: &mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{},
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
			Roles: []mdbv1.MongoDbRole{},
		},
	}

	mrs := &MongoDBMulti{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: mock.TestNamespace}}
	return &MultiReplicaSetBuilder{mrs}
}

func (m *MultiReplicaSetBuilder) Build() *MongoDBMulti {
	// initialize defaults
	res := m.MongoDBMulti.DeepCopy()
	res.InitDefaults()
	return res
}

func (m *MultiReplicaSetBuilder) SetSecurity(s *mdbv1.Security) *MultiReplicaSetBuilder {
	m.Spec.Security = s
	return m
}

func (m *MultiReplicaSetBuilder) SetClusterSpecList(clusters []string) *MultiReplicaSetBuilder {
	rand.Seed(time.Now().UnixNano())

	for _, e := range clusters {
		m.Spec.ClusterSpecList.ClusterSpecs = append(m.Spec.ClusterSpecList.ClusterSpecs, ClusterSpecItem{
			ClusterName: e,
			Members:     rand.Intn(5) + 1, // number of cluster members b/w 1 to 5
		})
	}
	return m
}

func (m *MultiReplicaSetBuilder) SetConnectionSpec(spec mdbv1.ConnectionSpec) *MultiReplicaSetBuilder {
	m.Spec.ConnectionSpec = spec
	return m
}

func (m *MultiReplicaSetBuilder) SetBackup(backupSpec mdbv1.Backup) *MultiReplicaSetBuilder {
	m.Spec.Backup = &backupSpec
	return m
}
