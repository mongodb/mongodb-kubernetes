package migratetomck

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/passwordhash"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func generateReplicaSet(ac *om.AutomationConfig, opts GenerateOptions) (client.Object, string, error) {
	replicaSets := ac.Deployment.GetReplicaSets()
	if len(replicaSets) == 0 {
		return nil, "", fmt.Errorf("no replica sets found in the automation config")
	}
	rs := replicaSets[0]

	rsName := rs.Name()
	externalMembers, version, fcv := om.ExtractMemberInfo(rs.Members(), ac.Deployment.ProcessMap())

	resourceName := opts.ResourceNameOverride
	if resourceName == "" {
		resourceName = util.NormalizeName(rsName)
		if resourceName == "" {
			return nil, "", fmt.Errorf("replica set name %q cannot be normalized to a valid Kubernetes resource name. Use --resource-name-override to provide one", rsName)
		}
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

// buildReplicaSetDbCommonSpec constructs the DbCommonSpec for a replica set deployment,
// including security, Prometheus, TLS, and connection settings.
func buildReplicaSetDbCommonSpec(ac *om.AutomationConfig, opts GenerateOptions, version, fcv, resourceName string) (mdbv1.DbCommonSpec, error) {
	security, err := buildSecurity(ac, opts.CertsSecretPrefix, resourceName)
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
	if prom != nil {
		acProm := ac.Deployment.GetPrometheus()
		if acProm != nil && acProm.PasswordSalt != "" {
			if opts.PrometheusPassword == "" {
				return mdbv1.DbCommonSpec{}, fmt.Errorf("Prometheus is enabled with a password hash in the Ops Manager automation config but no password was provided. Create a Kubernetes Secret with the password and pass --prometheus-secret-name")
			}
			match, pErr := passwordhash.PasswordMatchesHash(opts.PrometheusPassword, acProm.PasswordHash, acProm.PasswordSalt)
			if pErr != nil {
				return mdbv1.DbCommonSpec{}, fmt.Errorf("failed to verify Prometheus password against automation config: %w", pErr)
			}
			if !match {
				return mdbv1.DbCommonSpec{}, fmt.Errorf("Prometheus password in Secret %q does not match the password in the Ops Manager automation config", opts.PrometheusSecretName)
			}
		}
	}

	var additionalConfig *mdbv1.AdditionalMongodConfig
	if opts.SourceProcess != nil {
		additionalConfig = opts.SourceProcess.AdditionalMongodConfig()
	}
	additionalConfig = applyClientCertificateMode(ac.AgentSSL, additionalConfig)

	var featureCompatibilityVersion *string
	if fcv != "" {
		featureCompatibilityVersion = &fcv
	}
	return mdbv1.DbCommonSpec{
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
		Security:               security,
		Prometheus:             prom,
		AdditionalMongodConfig: additionalConfig,
		Agent:                  extractAgentConfig(opts.SourceProcess, opts.ProjectConfigs),
	}, nil
}

func buildReplicaSetSpec(ac *om.AutomationConfig, opts GenerateOptions, version, fcv string, externalMembers []mdbv1.ExternalMember, rsName, resourceName string) (mdbv1.MongoDbSpec, error) {
	common, err := buildReplicaSetDbCommonSpec(ac, opts, version, fcv, resourceName)
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
