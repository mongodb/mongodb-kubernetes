package multicluster

import (
	"fmt"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"os"
	"testing"

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
		ClusterSpecList: mdbmulti.ClusterSpecList{
			ClusterSpecs: []mdbmulti.ClusterSpecItem{
				{
					ClusterName: "foo",
					Members:     3,
				},
			},
		},
	}
	return mdbmulti.MongoDBMulti{Spec: spec, ObjectMeta: metav1.ObjectMeta{Name: "pod-aff", Namespace: mock.TestNamespace}}
}

func TestMultiClusterStatefulSet(t *testing.T) {

	tests := []struct {
		inp              appsv1.StatefulSetSpec
		outReplicas      int32
		outLabelSelector map[string]string
	}{
		{
			inp: appsv1.StatefulSetSpec{
				Replicas: int32Ptr(int32(4)),
			},
			outReplicas: 4,
			outLabelSelector: map[string]string{
				"controller":        "mongodb-enterprise-operator",
				"pod-anti-affinity": "pod-aff",
			},
		},
		{
			inp: appsv1.StatefulSetSpec{
				Replicas: int32Ptr(int32(5)),
			},
			outReplicas: 5,
			outLabelSelector: map[string]string{
				"controller":        "mongodb-enterprise-operator",
				"pod-anti-affinity": "pod-aff",
			},
		},
		{
			inp: appsv1.StatefulSetSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"foo": "bar"},
				},
			},
			outReplicas: 3,
			outLabelSelector: map[string]string{
				"controller":        "mongodb-enterprise-operator",
				"pod-anti-affinity": "pod-aff",
				"foo":               "bar",
			},
		},
	}
	os.Setenv(util.AutomationAgentImage, "some-registry")
	os.Setenv(util.InitDatabaseImageUrlEnv, "some-registry")

	for _, tt := range tests {
		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.ClusterSpecList.ClusterSpecs[0].StatefulSetConfiguration.SpecWrapper.Spec = tt.inp
		sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}), mdbv1.ProjectConfig{}, tt.inp, "")
		assert.NoError(t, err)
		assert.Equal(t, *sts.Spec.Replicas, tt.outReplicas)
		assert.Equal(t, sts.Spec.Selector.MatchLabels, tt.outLabelSelector)
	}
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
		mdbm.Spec.ClusterSpecList.ClusterSpecs[0].StatefulSetConfiguration.SpecWrapper.Spec = tt.inp
		sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}), mdbv1.ProjectConfig{}, tt.inp, "")
		assert.NoError(t, err)
		assert.Equal(t, tt.out.AccessMode, sts.Spec.VolumeClaimTemplates[0].Spec.AccessModes)
		storage, _ := sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().AsInt64()
		assert.Equal(t, tt.out.Storage, storage)
	}
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
	sts, err := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}), mdbv1.ProjectConfig{}, appsv1.StatefulSetSpec{}, "")
	assert.NoError(t, err)

	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
}
