package migratetomck

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func generateReplicaSet(ac *om.AutomationConfig, opts GenerateOptions) (client.Object, string, error) {
	replicaSets := ac.Deployment.GetReplicaSets()
	if len(replicaSets) == 0 {
		return nil, "", fmt.Errorf("no replica sets found in the automation config")
	}
	rs := replicaSets[0]

	rsName := rs.Name()
	externalMembers, version, fcv := om.ExtractMemberInfo(rs.Members(), ac.Deployment.ProcessMap())

	resourceName := resolveK8sResourceName(rsName, opts)
	if resourceName == "" {
		return nil, "", fmt.Errorf("replica set name %q cannot be normalized to a valid Kubernetes resource name. Use --resource-name-override to provide one", rsName)
	}
	if errs := k8svalidation.IsDNS1123Subdomain(resourceName); len(errs) > 0 {
		return nil, "", fmt.Errorf("resource name %q is not a valid Kubernetes resource name: %s", resourceName, errs[0])
	}

	return generateReplicaSetSingleCluster(ac, opts, rsName, resourceName, version, fcv, externalMembers)
}

func generateReplicaSetSingleCluster(ac *om.AutomationConfig, opts GenerateOptions, rsName, resourceName, version, fcv string, externalMembers []mdbv1.ExternalMember) (client.Object, string, error) {
	spec, err := buildReplicaSetSpec(ac, opts, version, fcv, externalMembers, rsName, resourceName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build MongoDB spec: %w", err)
	}
	return &mdbv1.MongoDB{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDB"},
		ObjectMeta: buildCRObjectMeta(resourceName, opts.Namespace),
		Spec:       spec,
	}, resourceName, nil
}

func buildReplicaSetSpec(ac *om.AutomationConfig, opts GenerateOptions, version, fcv string, externalMembers []mdbv1.ExternalMember, rsName, resourceName string) (mdbv1.MongoDbSpec, error) {
	common, err := buildDbCommonSpec(ac, opts, version, fcv, mdbv1.ReplicaSet, resourceName)
	if err != nil {
		return mdbv1.MongoDbSpec{}, err
	}

	spec := mdbv1.MongoDbSpec{
		DbCommonSpec:    common,
		Members:         0,
		ExternalMembers: externalMembers,
	}
	if resourceName != rsName {
		spec.ReplicaSetNameOverride = rsName
	}
	return spec, nil
}
