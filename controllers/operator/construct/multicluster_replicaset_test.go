package construct

import (
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getMultiClusterMongoDB() mdbmulti.MongoDBMulti {
	spec := mdbmulti.MongoDBMultiSpec{
		Version:    "5.0.0",
		Persistent: util.BooleanRef(false),
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

	for _, tt := range tests {
		mdbm := getMultiClusterMongoDB()
		mdbm.Spec.ClusterSpecList.ClusterSpecs[0].StatefulSetConfiguration.SpecWrapper.Spec = tt.inp
		sts := MultiClusterStatefulSet(mdbm, 0, 3, om.NewEmptyMockedOmConnection(&om.OMContext{}))
		assert.Equal(t, *sts.Spec.Replicas, tt.outReplicas)
		assert.Equal(t, sts.Spec.Selector.MatchLabels, tt.outLabelSelector)
	}
}
