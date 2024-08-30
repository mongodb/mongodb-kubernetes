package mdbmulti

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"

	v1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MultiReplicaSetBuilder struct {
	*MongoDBMultiCluster
}

const (
	TestProjectConfigMapName  = om.TestGroupName
	TestCredentialsSecretName = "my-credentials"
	TestNamespace             = "my-namespace"
)

func DefaultMultiReplicaSetBuilder() *MultiReplicaSetBuilder {
	spec := MongoDBMultiSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Connectivity: &mdbv1.MongoDBConnectivity{},
			Version:      "7.0.0",
			Persistent:   util.BooleanRef(false),
			ConnectionSpec: mdbv1.ConnectionSpec{
				SharedConnectionSpec: mdbv1.SharedConnectionSpec{
					OpsManagerConfig: &mdbv1.PrivateCloudConfig{
						ConfigMapRef: mdbv1.ConfigMapRef{
							Name: TestProjectConfigMapName,
						},
					},
				},
				Credentials: TestCredentialsSecretName,
			},
			ResourceType: mdbv1.ReplicaSet,
			Security: &mdbv1.Security{
				TLSConfig: &mdbv1.TLSConfig{},
				Authentication: &mdbv1.Authentication{
					Modes: []mdbv1.AuthMode{},
				},
				Roles: []mdbv1.MongoDbRole{},
			},
		},
		DuplicateServiceObjects: util.BooleanRef(false),
	}

	mrs := &MongoDBMultiCluster{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "temple", Namespace: TestNamespace}}
	return &MultiReplicaSetBuilder{mrs}
}

func (m *MultiReplicaSetBuilder) Build() *MongoDBMultiCluster {
	// initialize defaults
	res := m.MongoDBMultiCluster.DeepCopy()
	res.InitDefaults()
	return res
}

func (m *MultiReplicaSetBuilder) SetSecurity(s *mdbv1.Security) *MultiReplicaSetBuilder {
	m.Spec.Security = s
	return m
}

func (m *MultiReplicaSetBuilder) SetClusterSpecList(clusters []string) *MultiReplicaSetBuilder {
	randFive, err := rand.Int(rand.Reader, big.NewInt(5))
	if err != nil {
		panic(err)
	}

	randFiveAsInt := int(randFive.Int64())

	for _, e := range clusters {
		m.Spec.ClusterSpecList = append(m.Spec.ClusterSpecList, mdbv1.ClusterSpecItem{
			ClusterName: e,
			Members:     randFiveAsInt + 1, // number of cluster members b/w 1 to 5
		})
	}
	return m
}

func (m *MultiReplicaSetBuilder) SetExternalAccess(configuration mdbv1.ExternalAccessConfiguration, externalDomainTemplate *string) *MultiReplicaSetBuilder {
	m.Spec.ExternalAccessConfiguration = &configuration

	for i := range m.Spec.ClusterSpecList {
		if externalDomainTemplate != nil {
			s := fmt.Sprintf(*externalDomainTemplate, i)
			m.Spec.ClusterSpecList[i].ExternalAccessConfiguration = &mdbv1.ExternalAccessConfiguration{
				ExternalDomain: &s,
			}
		}
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

func (m *MultiReplicaSetBuilder) SetPodSpecTemplate(spec corev1.PodTemplateSpec) *MultiReplicaSetBuilder {
	if m.Spec.StatefulSetConfiguration == nil {
		m.Spec.StatefulSetConfiguration = &v1.StatefulSetConfiguration{}
	}
	m.Spec.StatefulSetConfiguration.SpecWrapper.Spec.Template = spec
	return m
}

func (m *MultiReplicaSetBuilder) SetName(name string) *MultiReplicaSetBuilder {
	m.Name = name
	return m
}
