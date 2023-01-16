package multicluster

import (
	"fmt"
	"os"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	mdbc "github.com/mongodb/mongodb-kubernetes-operator/api/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getMultiClusterMongoDB() mdbmulti.MongoDBMulti {
	spec := mdbmulti.MongoDBMultiSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Version: "5.0.0",
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
		},
		ClusterSpecList: []mdbmulti.ClusterSpecItem{
			{
				ClusterName: "foo",
				Members:     3,
			},
		},
	}

	return mdbmulti.MongoDBMulti{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "pod-aff", Namespace: mock.TestNamespace}}
}

func TestMultiClusterStatefulSet(t *testing.T) {

	t.Run("No override provided", func(t *testing.T) {
		mdbm := getMultiClusterMongoDB()

		sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}),
			mdbv1.ProjectConfig{}, nil, "")
		assert.NoError(t, err)

		expectedReplicas := mdbm.Spec.ClusterSpecList[0].Members
		assert.Equal(t, expectedReplicas, int(*sts.Spec.Replicas))

	})

	t.Run("Override provided at clusterSpecList level only", func(t *testing.T) {
		singleClusterOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{
			Spec: appsv1.StatefulSetSpec{
				Replicas: int32Ptr(int32(4)),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "bar"},
				},
			},
		}}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.ClusterSpecList[0].StatefulSetConfiguration = singleClusterOverride

		sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}),
			mdbv1.ProjectConfig{}, singleClusterOverride, "")
		assert.NoError(t, err)

		expectedMatchLabels := singleClusterOverride.SpecWrapper.Spec.Selector.MatchLabels
		expectedMatchLabels["pod-anti-affinity"] = mdbm.Name
		expectedMatchLabels["controller"] = "mongodb-enterprise-operator"

		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.Replicas, sts.Spec.Replicas)
		assert.Equal(t, expectedMatchLabels, sts.Spec.Selector.MatchLabels)

	})

	t.Run("Override provided only at Spec level", func(t *testing.T) {
		stsOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo": "bar"},
			},
			ServiceName: "overrideservice",
		},
		},
		}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.StatefulSetConfiguration = stsOverride

		sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}),
			mdbv1.ProjectConfig{}, nil, "")
		assert.NoError(t, err)

		expectedReplicas := mdbm.Spec.ClusterSpecList[0].Members
		assert.Equal(t, expectedReplicas, int(*sts.Spec.Replicas))

		assert.Equal(t, stsOverride.SpecWrapper.Spec.ServiceName, sts.Spec.ServiceName)

	})

	t.Run("Override provided at both Spec and clusterSpecList level", func(t *testing.T) {

		stsOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo": "bar"},
			},
			ServiceName: "overrideservice",
		},
		},
		}

		singleClusterOverride := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{
			Spec: appsv1.StatefulSetSpec{
				ServiceName: "clusteroverrideservice",
				Replicas:    int32Ptr(int32(4)),
			},
		},
		}

		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.StatefulSetConfiguration = stsOverride

		sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}),
			mdbv1.ProjectConfig{}, singleClusterOverride, "")
		assert.NoError(t, err)

		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.ServiceName, sts.Spec.ServiceName)
		assert.Equal(t, singleClusterOverride.SpecWrapper.Spec.Replicas, sts.Spec.Replicas)
	})
}

func Test_MultiClusterStatefulSetWithRelatedImages(t *testing.T) {
	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.AutomationAgentImage)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0", util.InitDatabaseImageUrlEnv)

	defer env.RevertEnvVariables(databaseRelatedImageEnv, initDatabaseRelatedImageEnv, util.AutomationAgentImage, construct.DatabaseVersionEnv, util.InitDatabaseImageUrlEnv, construct.InitDatabaseVersionEnv)()

	_ = os.Setenv(util.AutomationAgentImage, "quay.io/mongodb/mongodb-enterprise-database")
	_ = os.Setenv(construct.DatabaseVersionEnv, "1.0.0")
	_ = os.Setenv(util.InitDatabaseImageUrlEnv, "quay.io/mongodb/mongodb-enterprise-init-database")
	_ = os.Setenv(construct.InitDatabaseVersionEnv, "2.0.0")
	_ = os.Setenv(databaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE")
	_ = os.Setenv(initDatabaseRelatedImageEnv, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE")

	mdbm := getMultiClusterMongoDB()
	sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}), mdbv1.ProjectConfig{}, nil, "")
	assert.NoError(t, err)

	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
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
							Resources: corev1.ResourceRequirements{
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

	os.Setenv(util.AutomationAgentImage, "some-registry")
	os.Setenv(util.InitDatabaseImageUrlEnv, "some-registry")

	for _, tt := range tests {
		mdbm := getMultiClusterMongoDB()

		stsOverrideConfiguration := &mdbc.StatefulSetConfiguration{SpecWrapper: mdbc.StatefulSetSpecWrapper{Spec: tt.inp}}
		sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}), mdbv1.ProjectConfig{}, stsOverrideConfiguration, "")

		assert.NoError(t, err)
		assert.Equal(t, tt.out.AccessMode, sts.Spec.VolumeClaimTemplates[0].Spec.AccessModes)
		storage, _ := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().AsInt64()
		assert.Equal(t, tt.out.Storage, storage)
	}
}
