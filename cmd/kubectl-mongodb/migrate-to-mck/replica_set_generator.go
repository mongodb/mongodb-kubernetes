package migratetomck

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
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

	resourceName := opts.ReplicaSetNameOverride
	if resourceName == "" {
		if userv1.NormalizeName(rsName) != rsName {
			return nil, "", fmt.Errorf("replica set name %q is not a valid Kubernetes resource name. Use --replicaset-name-override to provide a valid name (spec.replicaSetNameOverride will be set automatically)", rsName)
		}
		resourceName = rsName
	} else if userv1.NormalizeName(resourceName) != resourceName {
		return nil, "", fmt.Errorf("--replicaset-name-override value %q is not a valid Kubernetes resource name", resourceName)
	}

	if len(opts.MultiClusterNames) > 0 {
		return generateReplicaSetMultiCluster(ac, opts, rsName, resourceName, version, fcv, externalMembers)
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

func generateReplicaSetMultiCluster(ac *om.AutomationConfig, opts GenerateOptions, rsName, resourceName, version, fcv string, externalMembers []mdbv1.ExternalMember) (client.Object, string, error) {
	spec, err := buildReplicaSetMultiClusterSpec(ac, opts, version, fcv, externalMembers, rsName, resourceName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build multi-cluster spec: %w", err)
	}
	return &mdbmulti.MongoDBMultiCluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDBMultiCluster"},
		ObjectMeta: buildCRObjectMeta(resourceName, opts.Namespace),
		Spec:       spec,
	}, resourceName, nil
}

// buildReplicaSetDbCommonSpec constructs the DbCommonSpec for a replica set deployment,
// including security, Prometheus, TLS, and connection settings.
func buildReplicaSetDbCommonSpec(ac *om.AutomationConfig, opts GenerateOptions, version, fcv, rsName, resourceName string, externalMembers []mdbv1.ExternalMember) (mdbv1.DbCommonSpec, error) {
	rs := ac.Deployment.GetReplicaSets()[0]
	security, err := buildSecurity(ac.Auth, ac.Deployment.ProcessMap(), rs.Members(), ac.Ldap, ac.OIDCProviderConfigs, opts.CertsSecretPrefix)
	if err != nil {
		return mdbv1.DbCommonSpec{}, fmt.Errorf("failed to build security config: %w", err)
	}
	if roles := ac.Deployment.GetRoles(); len(roles) > 0 {
		if security == nil {
			security = &mdbv1.Security{}
		}
		security.Roles = roles
	}

	prom, err := extractPrometheusConfig(ac.Deployment)
	if err != nil {
		return mdbv1.DbCommonSpec{}, fmt.Errorf("failed to extract Prometheus config: %w", err)
	}
	if prom != nil && opts.PrometheusSecretName != "" {
		prom.PasswordSecretRef.Name = opts.PrometheusSecretName
	}

	var additionalConfig *mdbv1.AdditionalMongodConfig
	if opts.SourceProcess != nil {
		additionalConfig = opts.SourceProcess.AdditionalMongodConfig()
	}

	var featureCompatibilityVersion *string
	if fcv != "" {
		featureCompatibilityVersion = &fcv
	}
	common := mdbv1.DbCommonSpec{
		Version:                     version,
		ResourceType:                mdbv1.ReplicaSet,
		FeatureCompatibilityVersion: featureCompatibilityVersion,
		ConnectionSpec: mdbv1.ConnectionSpec{
			SharedConnectionSpec: mdbv1.SharedConnectionSpec{
				OpsManagerConfig: &mdbv1.PrivateCloudConfig{
					ConfigMapRef: mdbv1.ConfigMapRef{Name: opts.ConfigMapName},
				},
			},
			Credentials: opts.CredentialsSecretName,
		},
		ExternalMembers:        externalMembers,
		Security:               security,
		Prometheus:             prom,
		AdditionalMongodConfig: additionalConfig,
		Agent:                  extractAgentConfig(opts.SourceProcess, opts.ProjectConfigs),
	}
	if resourceName != rsName {
		common.ReplicaSetNameOverride = rsName
	}
	return common, nil
}

func buildReplicaSetSpec(ac *om.AutomationConfig, opts GenerateOptions, version, fcv string, externalMembers []mdbv1.ExternalMember, rsName, resourceName string) (mdbv1.MongoDbSpec, error) {
	common, err := buildReplicaSetDbCommonSpec(ac, opts, version, fcv, rsName, resourceName, externalMembers)
	if err != nil {
		return mdbv1.MongoDbSpec{}, err
	}
	return mdbv1.MongoDbSpec{
		DbCommonSpec: common,
		Members:      len(externalMembers),
		MemberConfig: buildMemberConfig(ac.Deployment.GetReplicaSets()[0].Members()),
	}, nil
}

// buildReplicaSetMultiClusterSpec assembles a MongoDBMultiSpec, distributing members across target clusters.
func buildReplicaSetMultiClusterSpec(ac *om.AutomationConfig, opts GenerateOptions, version, fcv string, externalMembers []mdbv1.ExternalMember, rsName, resourceName string) (mdbmulti.MongoDBMultiSpec, error) {
	common, err := buildReplicaSetDbCommonSpec(ac, opts, version, fcv, rsName, resourceName, externalMembers)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, err
	}
	clusterSpecList, err := distributeMembers(externalMembers, ac.Deployment.GetReplicaSets()[0].Members(), opts.MultiClusterNames)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, err
	}
	return mdbmulti.MongoDBMultiSpec{
		DbCommonSpec:    common,
		ClusterSpecList: clusterSpecList,
	}, nil
}
