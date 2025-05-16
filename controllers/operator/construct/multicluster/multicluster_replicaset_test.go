package multicluster

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

func init() {
	mock.InitDefaultEnvVariables()
}

func getMultiClusterMongoDB() mdbmulti.MongoDBMultiCluster {
	spec := mdbmulti.MongoDBMultiSpec{
		DbCommonSpec: mdb.DbCommonSpec{
			Version: "5.0.0",
			ConnectionSpec: mdb.ConnectionSpec{
				SharedConnectionSpec: mdb.SharedConnectionSpec{
					OpsManagerConfig: &mdb.PrivateCloudConfig{
						ConfigMapRef: mdb.ConfigMapRef{
							Name: mock.TestProjectConfigMapName,
						},
					},
				}, Credentials: mock.TestCredentialsSecretName,
			},
			ResourceType: mdb.ReplicaSet,
			Security: &mdb.Security{
				TLSConfig: &mdb.TLSConfig{},
				Authentication: &mdb.Authentication{
					Modes: []mdb.AuthMode{},
				},
				Roles: []mdb.MongoDbRole{},
			},
		},
		ClusterSpecList: mdb.ClusterSpecList{
			{
				ClusterName: "foo",
				Members:     3,
			},
		},
	}

	return mdbmulti.MongoDBMultiCluster{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "pod-aff", Namespace: mock.TestNamespace}}
}

func TestMultiClusterStatefulSet(t *testing.T) {
	t.Run("No override provided", func(t *testing.T) {
		mdbm := getMultiClusterMongoDB()
		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
		)
		sts := MultiClusterStatefulSet(mdbm, opts)

		expectedReplicas := mdbm.Spec.ClusterSpecList[0].Members
		assert.Equal(t, expectedReplicas, int(*sts.Spec.Replicas))
	})

	t.Run("Override provided at clusterSpecList level only", func(t *testing.T) {
		singleClusterOverride := &common.StatefulSetConfiguration{SpecWrapper: common.StatefulSetSpecWrapper{
			Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(4)),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "bar"},
				},
			},
		}}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.ClusterSpecList[0].StatefulSetConfiguration = singleClusterOverride

		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
			WithStsOverride(&singleClusterOverride.SpecWrapper.Spec),
		)

		sts := MultiClusterStatefulSet(mdbm, opts)

		expectedMatchLabels := singleClusterOverride.SpecWrapper.Spec.Selector.MatchLabels
		expectedMatchLabels["app"] = ""
		expectedMatchLabels["pod-anti-affinity"] = mdbm.Name
		expectedMatchLabels[util.OperatorLabelName] = util.OperatorLabelValue

		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.Replicas, sts.Spec.Replicas)
		assert.Equal(t, expectedMatchLabels, sts.Spec.Selector.MatchLabels)
	})

	t.Run("Override provided only at Spec level", func(t *testing.T) {
		stsOverride := &common.StatefulSetConfiguration{
			SpecWrapper: common.StatefulSetSpecWrapper{
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "bar"},
					},
					ServiceName: "overrideservice",
				},
			},
		}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.StatefulSetConfiguration = stsOverride
		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
		)

		sts := MultiClusterStatefulSet(mdbm, opts)

		expectedReplicas := mdbm.Spec.ClusterSpecList[0].Members
		assert.Equal(t, expectedReplicas, int(*sts.Spec.Replicas))

		assert.Equal(t, stsOverride.SpecWrapper.Spec.ServiceName, sts.Spec.ServiceName)
	})

	t.Run("Override provided at both Spec and clusterSpecList level", func(t *testing.T) {
		stsOverride := &common.StatefulSetConfiguration{
			SpecWrapper: common.StatefulSetSpecWrapper{
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "bar"},
					},
					ServiceName: "overrideservice",
				},
			},
		}

		singleClusterOverride := &common.StatefulSetConfiguration{
			SpecWrapper: common.StatefulSetSpecWrapper{
				Spec: appsv1.StatefulSetSpec{
					ServiceName: "clusteroverrideservice",
					Replicas:    ptr.To(int32(4)),
				},
			},
		}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.StatefulSetConfiguration = stsOverride

		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
			WithStsOverride(&singleClusterOverride.SpecWrapper.Spec),
		)

		sts := MultiClusterStatefulSet(mdbm, opts)

		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.ServiceName, sts.Spec.ServiceName)
		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.Replicas, sts.Spec.Replicas)
	})
}

func TestPVCOverride(t *testing.T) {
	tests := []struct {
		inp appsv1.StatefulSetSpec
		out struct {
			Storage    int64
			AccessMode []corev1.PersistentVolumeAccessMode
		}
	}{
		{
			inp: appsv1.StatefulSetSpec{
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "data",
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: construct.ParseQuantityOrZero("20"),
								},
							},
							AccessModes: []corev1.PersistentVolumeAccessMode{},
						},
					},
				},
			},
			out: struct {
				Storage    int64
				AccessMode []corev1.PersistentVolumeAccessMode
			}{
				Storage:    20,
				AccessMode: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce"},
			},
		},
		{
			inp: appsv1.StatefulSetSpec{
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "data",
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{"ReadWriteMany"},
						},
					},
				},
			},
			out: struct {
				Storage    int64
				AccessMode []corev1.PersistentVolumeAccessMode
			}{
				Storage:    16000000000,
				AccessMode: []corev1.PersistentVolumeAccessMode{"ReadWriteOnce", "ReadWriteMany"},
			},
		},
	}

	t.Setenv(util.NonStaticDatabaseEnterpriseImage, "some-registry")
	t.Setenv(util.InitDatabaseImageUrlEnv, "some-registry")

	for _, tt := range tests {
		mdbm := getMultiClusterMongoDB()

		stsOverrideConfiguration := &common.StatefulSetConfiguration{SpecWrapper: common.StatefulSetSpecWrapper{Spec: tt.inp}}
		opts := MultiClusterReplicaSetOptions(
			WithClusterNum(0),
			WithMemberCount(3),
			construct.GetPodEnvOptions(),
			WithStsOverride(&stsOverrideConfiguration.SpecWrapper.Spec),
		)
		sts := MultiClusterStatefulSet(mdbm, opts)
		assert.Equal(t, tt.out.AccessMode, sts.Spec.VolumeClaimTemplates[0].Spec.AccessModes)
		storage, _ := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().AsInt64()
		assert.Equal(t, tt.out.Storage, storage)
	}
}

func TestMultiClusterStatefulSet_StaticContainersEnvVars(t *testing.T) {
	tests := []struct {
		name                 string
		defaultArchitecture  string
		annotations          map[string]string
		expectedEnvVar       corev1.EnvVar
		expectAgentContainer bool
	}{
		{
			name:                 "Default architecture - static, no annotations",
			defaultArchitecture:  string(architectures.Static),
			annotations:          nil,
			expectedEnvVar:       corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"},
			expectAgentContainer: true,
		},
		{
			name:                 "Default architecture - non-static, annotations - static",
			defaultArchitecture:  string(architectures.NonStatic),
			annotations:          map[string]string{architectures.ArchitectureAnnotation: string(architectures.Static)},
			expectedEnvVar:       corev1.EnvVar{Name: "MDB_STATIC_CONTAINERS_ARCHITECTURE", Value: "true"},
			expectAgentContainer: true,
		},
		{
			name:                 "Default architecture - non-static, no annotations",
			defaultArchitecture:  string(architectures.NonStatic),
			annotations:          nil,
			expectedEnvVar:       corev1.EnvVar{},
			expectAgentContainer: false,
		},
		{
			name:                 "Default architecture - static, annotations - non-static",
			defaultArchitecture:  string(architectures.Static),
			annotations:          map[string]string{architectures.ArchitectureAnnotation: string(architectures.NonStatic)},
			expectedEnvVar:       corev1.EnvVar{},
			expectAgentContainer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(architectures.DefaultEnvArchitecture, tt.defaultArchitecture)

			mdbm := getMultiClusterMongoDB()
			mdbm.Annotations = tt.annotations
			opts := MultiClusterReplicaSetOptions(
				WithClusterNum(0),
				WithMemberCount(3),
				construct.GetPodEnvOptions(),
			)

			sts := MultiClusterStatefulSet(mdbm, opts)

			agentContainerIdx := slices.IndexFunc(sts.Spec.Template.Spec.Containers, func(container corev1.Container) bool {
				return container.Name == util.AgentContainerName
			})
			if tt.expectAgentContainer {
				require.NotEqual(t, -1, agentContainerIdx)
				assert.Contains(t, sts.Spec.Template.Spec.Containers[agentContainerIdx].Env, tt.expectedEnvVar)
			} else {
				// In non-static architecture there is no agent container
				// so the index should be -1.
				require.Equal(t, -1, agentContainerIdx)
			}
		})
	}
}
