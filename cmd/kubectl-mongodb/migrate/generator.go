package migrate

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	PrometheusPasswordSecretName = "prometheus-password"
	PrometheusTLSSecretName      = "prometheus-tls"
	LdapBindQuerySecretName      = "ldap-bind-query-password"
	LdapCAConfigMapName          = "ldap-ca"
	LdapCAKey                    = "ca.pem"
)

// GenerateOptions holds parameters for CR generation that come from CLI flags
// and deployment-level agent configuration read from the OM API.
type GenerateOptions struct {
	ResourceName          string
	CredentialsSecretName string
	ConfigMapName         string
	MultiClusterNames     []string
	AgentConfigs          *ProjectAgentConfigs
	ProcessConfigs        *ProjectProcessConfigs
}

// UserCROutput holds the generated YAML and metadata for a single MongoDBUser CR.
type UserCROutput struct {
	YAML           string
	Username       string
	Database       string
	NeedsPassword  bool
	PasswordSecret string
}

// GenerateMongoDBCR generates a MongoDB CR from the given automation config.
// It detects the deployment type (replica set vs sharded cluster) and topology
// (single vs multi-cluster) and dispatches to the appropriate builder.
func GenerateMongoDBCR(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	isSharded := len(ac.Deployment.GetShardedClusters()) > 0
	isMultiCluster := len(opts.MultiClusterNames) > 0

	switch {
	case isSharded && isMultiCluster:
		return generateShardedClusterMultiCluster(ac, opts)
	case isSharded:
		return generateShardedClusterSingleCluster(ac, opts)
	case isMultiCluster:
		return generateReplicaSetMultiCluster(ac, opts)
	default:
		return generateReplicaSetSingleCluster(ac, opts)
	}
}

func generateShardedClusterSingleCluster(_ *om.AutomationConfig, _ GenerateOptions) (string, string, error) {
	return "", "", fmt.Errorf("sharded cluster migration is not yet supported")
}

func generateShardedClusterMultiCluster(_ *om.AutomationConfig, _ GenerateOptions) (string, string, error) {
	return "", "", fmt.Errorf("sharded cluster multi-cluster migration is not yet supported")
}

func generateReplicaSetSingleCluster(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	replicaSets := ac.Deployment.GetReplicaSets()
	if len(replicaSets) == 0 {
		return "", "", fmt.Errorf("no replica sets found in the automation config")
	}
	rs := replicaSets[0]

	rsName := rs.Name()
	members := rs.Members()
	processMap := ac.Deployment.ProcessMap()

	externalMembers, version, fcv := om.ExtractMemberInfo(members, processMap)

	resourceName := opts.ResourceName
	if resourceName == "" {
		resourceName = rsName
	}

	spec, err := buildMongoDBSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac, processMap, members)
	if err != nil {
		return "", "", fmt.Errorf("error building MongoDB spec: %w", err)
	}

	cr := mdbv1.MongoDB{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mongodb.com/v1",
			Kind:       "MongoDB",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: resourceName,
			Annotations: map[string]string{
				"mongodb.com/migrate-tool-version": pluginVersion(),
			},
		},
		Spec: spec,
	}

	out, err := marshalCRToYAML(cr)
	if err != nil {
		return "", "", fmt.Errorf("error marshalling CR to YAML: %w", err)
	}

	return out, resourceName, nil
}

func generateReplicaSetMultiCluster(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	if len(opts.MultiClusterNames) == 0 {
		return "", "", fmt.Errorf("multi-cluster generation requires at least one cluster name")
	}

	replicaSets := ac.Deployment.GetReplicaSets()
	if len(replicaSets) == 0 {
		return "", "", fmt.Errorf("no replica sets found in the automation config")
	}
	rs := replicaSets[0]

	rsName := rs.Name()
	members := rs.Members()
	processMap := ac.Deployment.ProcessMap()

	externalMembers, version, fcv := om.ExtractMemberInfo(members, processMap)

	resourceName := opts.ResourceName
	if resourceName == "" {
		resourceName = rsName
	}

	spec, err := buildMultiClusterSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac, processMap, members)
	if err != nil {
		return "", "", fmt.Errorf("error building multi-cluster spec: %w", err)
	}

	cr := mdbmulti.MongoDBMultiCluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mongodb.com/v1",
			Kind:       "MongoDBMultiCluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: resourceName,
			Annotations: map[string]string{
				"mongodb.com/migrate-tool-version": pluginVersion(),
			},
		},
		Spec: spec,
	}

	out, err := marshalCRToYAML(cr)
	if err != nil {
		return "", "", fmt.Errorf("error marshalling CR to YAML: %w", err)
	}

	return out, resourceName, nil
}

// buildMultiClusterSpec assembles a MongoDBMultiSpec, distributing members
// across the provided target clusters.
func buildMultiClusterSpec(
	version, fcv string,
	externalMembers []mdbv1.ExternalMember,
	rsName, resourceName string,
	opts GenerateOptions,
	ac *om.AutomationConfig,
	processMap map[string]om.Process,
	members []om.ReplicaSetMember,
) (mdbmulti.MongoDBMultiSpec, error) {
	memberCount := len(externalMembers)

	clusterSpecList := distributeMembers(memberCount, opts.MultiClusterNames)
	clusterMemberConfig := distributeMemberConfig(members, opts.MultiClusterNames)
	for i := range clusterSpecList {
		clusterSpecList[i].MemberConfig = clusterMemberConfig[i]
	}

	spec := mdbmulti.MongoDBMultiSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Version:      version,
			ResourceType: mdbv1.ReplicaSet,
			ConnectionSpec: mdbv1.ConnectionSpec{
				SharedConnectionSpec: mdbv1.SharedConnectionSpec{
					OpsManagerConfig: &mdbv1.PrivateCloudConfig{
						ConfigMapRef: mdbv1.ConfigMapRef{
							Name: opts.ConfigMapName,
						},
					},
				},
				Credentials: opts.CredentialsSecretName,
			},
			ExternalMembers: externalMembers,
		},
		ClusterSpecList: clusterSpecList,
	}

	if resourceName != rsName {
		spec.DbCommonSpec.ReplicaSetNameOverride = rsName
	}

	spec.FeatureCompatibilityVersion = &fcv

	security, err := buildSecurity(ac.Auth, processMap, members, ac.Ldap, ac.OIDCProviderConfigs)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, fmt.Errorf("error building security config: %w", err)
	}
	spec.Security = security

	if roles := extractCustomRoles(ac.Deployment); len(roles) > 0 {
		if spec.Security == nil {
			spec.Security = &mdbv1.Security{}
		}
		spec.Security.Roles = roles
	}

	prom, err := extractPrometheusConfig(ac.Deployment)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, fmt.Errorf("error extracting prometheus config: %w", err)
	}
	spec.Prometheus = prom

	additionalConfig, err := extractAdditionalMongodConfig(processMap, members)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, fmt.Errorf("error extracting additional mongod config: %w", err)
	}
	spec.AdditionalMongodConfig = additionalConfig

	agentConfig, err := extractAgentConfig(processMap, members, opts.AgentConfigs, opts.ProcessConfigs)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, fmt.Errorf("error extracting agent config: %w", err)
	}
	if agentConfig.Mongod.HasLoggingConfigured() || agentConfig.MonitoringAgent.LogRotate != nil {
		spec.Agent = agentConfig
	}

	return spec, nil
}

// distributeMembers spreads memberCount as evenly as possible across the
// given cluster names. Extra members go to the earlier clusters.
func distributeMembers(memberCount int, clusterNames []string) mdbv1.ClusterSpecList {
	n := len(clusterNames)
	if n == 0 {
		return nil
	}
	base := memberCount / n
	remainder := memberCount % n

	list := make(mdbv1.ClusterSpecList, n)
	for i, name := range clusterNames {
		count := base
		if i < remainder {
			count++
		}
		list[i] = mdbv1.ClusterSpecItem{
			ClusterName: name,
			Members:     count,
		}
	}
	return list
}

// distributeMemberConfig builds per-cluster MemberOptions slices that mirror
// the member distribution in distributeMembers. Each member gets votes=0 and
// priority="0" (draining policy). Tags are preserved from the automation config.
func distributeMemberConfig(members []om.ReplicaSetMember, clusterNames []string) [][]automationconfig.MemberOptions {
	n := len(clusterNames)
	if n == 0 {
		return nil
	}
	allConfig := buildMemberConfig(members)
	memberCount := len(allConfig)
	base := memberCount / n
	remainder := memberCount % n

	result := make([][]automationconfig.MemberOptions, n)
	offset := 0
	for i := 0; i < n; i++ {
		count := base
		if i < remainder {
			count++
		}
		result[i] = allConfig[offset : offset+count]
		offset += count
	}
	return result
}

// buildMongoDBSpec assembles the MongoDbSpec by extracting security, roles,
// prometheus, additional mongod config, agent config, and member config from
// the automation config.
func buildMongoDBSpec(
	version, fcv string,
	externalMembers []mdbv1.ExternalMember,
	rsName, resourceName string,
	opts GenerateOptions,
	ac *om.AutomationConfig,
	processMap map[string]om.Process,
	members []om.ReplicaSetMember,
) (mdbv1.MongoDbSpec, error) {
	memberCount := len(externalMembers)

	spec := mdbv1.MongoDbSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Version:      version,
			ResourceType: mdbv1.ReplicaSet,
			ConnectionSpec: mdbv1.ConnectionSpec{
				SharedConnectionSpec: mdbv1.SharedConnectionSpec{
					OpsManagerConfig: &mdbv1.PrivateCloudConfig{
						ConfigMapRef: mdbv1.ConfigMapRef{
							Name: opts.ConfigMapName,
						},
					},
				},
				Credentials: opts.CredentialsSecretName,
			},
			ExternalMembers: externalMembers,
		},
		Members: memberCount,
	}

	if resourceName != rsName {
		spec.DbCommonSpec.ReplicaSetNameOverride = rsName
	}

	spec.FeatureCompatibilityVersion = &fcv

	security, err := buildSecurity(ac.Auth, processMap, members, ac.Ldap, ac.OIDCProviderConfigs)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("error building security config: %w", err)
	}
	spec.Security = security

	if roles := extractCustomRoles(ac.Deployment); len(roles) > 0 {
		if spec.Security == nil {
			spec.Security = &mdbv1.Security{}
		}
		spec.Security.Roles = roles
	}

	prom, err := extractPrometheusConfig(ac.Deployment)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("error extracting prometheus config: %w", err)
	}
	spec.Prometheus = prom

	additionalConfig, err := extractAdditionalMongodConfig(processMap, members)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("error extracting additional mongod config: %w", err)
	}
	spec.AdditionalMongodConfig = additionalConfig

	agentConfig, err := extractAgentConfig(processMap, members, opts.AgentConfigs, opts.ProcessConfigs)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("error extracting agent config: %w", err)
	}
	if agentConfig.Mongod.HasLoggingConfigured() || agentConfig.MonitoringAgent.LogRotate != nil {
		spec.Agent = agentConfig
	}

	spec.MemberConfig = buildMemberConfig(members)

	return spec, nil
}

// GenerateUserCRs creates MongoDBUser CRs for each user in auth.usersWanted,
// skipping the automation agent user.
func GenerateUserCRs(ac *om.AutomationConfig, mongodbResourceName string) ([]UserCROutput, error) {
	if ac.Auth == nil || len(ac.Auth.Users) == 0 {
		return nil, nil
	}

	seenCRNames := map[string]string{}
	var results []UserCROutput
	for i, user := range ac.Auth.Users {
		if user == nil {
			return nil, fmt.Errorf("user at index %d is nil", i)
		}
		if user.Username == "" {
			return nil, fmt.Errorf("user at index %d has an empty username", i)
		}

		if user.Username == ac.Auth.AutoUser && user.Database == util.DefaultUserDatabase {
			continue
		}

		needsPassword := user.Database != "$external"
		crName, err := normalizeK8sName(user.Username)
		if err != nil {
			return nil, fmt.Errorf("error normalizing username %q: %w", user.Username, err)
		}
		if prev, exists := seenCRNames[crName]; exists {
			return nil, fmt.Errorf("users %q and %q normalize to the same Kubernetes name %q; rename one before migration", prev, user.Username, crName)
		}
		seenCRNames[crName] = user.Username
		passwordSecretName := crName + "-password"

		roles, err := convertRoles(user.Roles)
		if err != nil {
			return nil, fmt.Errorf("error converting roles for user %q: %w", user.Username, err)
		}

		spec := userv1.MongoDBUserSpec{
			Username: user.Username,
			Database: user.Database,
			MongoDBResourceRef: userv1.MongoDBResourceRef{
				Name: mongodbResourceName,
			},
			Roles: roles,
		}
		if needsPassword {
			spec.PasswordSecretKeyRef = userv1.SecretKeyRef{
				Name: passwordSecretName,
				Key:  "password",
			}
		}

		userYAML, err := marshalCRToYAML(userv1.MongoDBUser{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "mongodb.com/v1",
				Kind:       "MongoDBUser",
			},
			ObjectMeta: metav1.ObjectMeta{Name: crName},
			Spec:       spec,
		})
		if err != nil {
			return nil, fmt.Errorf("error marshalling MongoDBUser CR for %q: %w", user.Username, err)
		}

		results = append(results, UserCROutput{
			YAML:           userYAML,
			Username:       user.Username,
			Database:       user.Database,
			NeedsPassword:  needsPassword,
			PasswordSecret: passwordSecretName,
		})
	}

	return results, nil
}

// GeneratePasswordSecret builds a Kubernetes Secret for a SCRAM user's password.
func GeneratePasswordSecret(secretName, namespace, password string) corev1.Secret {
	return corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		StringData: map[string]string{
			"password": password,
		},
	}
}

// GeneratePasswordSecretYAML returns the YAML representation of a password Secret.
func GeneratePasswordSecretYAML(secretName, namespace, password string) (string, error) {
	sec := GeneratePasswordSecret(secretName, namespace, password)
	return marshalCRToYAML(sec)
}

// LdapResources holds the YAML for Kubernetes resources required by the LDAP configuration.
type LdapResources struct {
	BindQueryPasswordSecret string
	CAConfigMap             string
}

// GenerateLdapResources creates the Secret and/or ConfigMap that the CR's
// LDAP config references. Returns nil when the AC has no LDAP configuration.
func GenerateLdapResources(ac *om.AutomationConfig, namespace string) (*LdapResources, error) {
	if ac.Ldap == nil {
		return nil, nil
	}

	res := &LdapResources{}
	hasResources := false

	if ac.Ldap.BindQueryPassword != "" {
		sec := corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      LdapBindQuerySecretName,
				Namespace: namespace,
			},
			StringData: map[string]string{
				"password": ac.Ldap.BindQueryPassword,
			},
		}
		out, err := marshalCRToYAML(sec)
		if err != nil {
			return nil, fmt.Errorf("error marshalling LDAP bind query secret: %w", err)
		}
		res.BindQueryPasswordSecret = out
		hasResources = true
	}

	if ac.Ldap.CaFileContents != "" {
		cm := corev1.ConfigMap{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ConfigMap",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      LdapCAConfigMapName,
				Namespace: namespace,
			},
			Data: map[string]string{
				LdapCAKey: ac.Ldap.CaFileContents,
			},
		}
		out, err := marshalCRToYAML(cm)
		if err != nil {
			return nil, fmt.Errorf("error marshalling LDAP CA configmap: %w", err)
		}
		res.CAConfigMap = out
		hasResources = true
	}

	if !hasResources {
		return nil, nil
	}
	return res, nil
}

// buildMemberConfig creates MemberOptions for each member with votes=0 and
// priority="0" (draining policy for external members being transitioned).
// Tags are preserved from the automation config.
func buildMemberConfig(members []om.ReplicaSetMember) []automationconfig.MemberOptions {
	config := make([]automationconfig.MemberOptions, len(members))

	for i, m := range members {
		v, p := 0, "0"
		config[i] = automationconfig.MemberOptions{
			Votes:    &v,
			Priority: &p,
		}

		if tags := m.Tags(); len(tags) > 0 {
			config[i].Tags = tags
		}
	}
	return config
}

// marshalCRToYAML marshals a Kubernetes resource to YAML, stripping the
// "status" block, "creationTimestamp: null", and all zero-value fields that
// are artifacts of Go struct serialization (empty strings, 0, false, nil,
// empty maps/slices). This produces clean YAML matching hand-written CRs.
func marshalCRToYAML(obj interface{}) (string, error) {
	jsonBytes, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return "", err
	}
	delete(m, "status")
	if meta, ok := m["metadata"].(map[string]interface{}); ok {
		delete(meta, "creationTimestamp")
	}
	stripZeroValues(m)
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// stripZeroValues recursively removes zero-value entries from a map:
// empty strings, 0, false, nil, empty maps, and empty slices.
func stripZeroValues(m map[string]interface{}) {
	for k, v := range m {
		if isZeroValue(v) {
			delete(m, k)
			continue
		}
		switch val := v.(type) {
		case map[string]interface{}:
			stripZeroValues(val)
			if len(val) == 0 {
				delete(m, k)
			}
		case []interface{}:
			for i, item := range val {
				if sub, ok := item.(map[string]interface{}); ok {
					stripZeroValues(sub)
					val[i] = sub
				}
			}
		}
	}
}

// isZeroValue returns true for values that are Go struct serialization
// artifacts: empty strings, nil, empty maps, and empty slices.
// Numbers and booleans are never stripped since 0 and false can be
// semantically meaningful (e.g. votes: 0 for draining members).
func isZeroValue(v interface{}) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case string:
		return val == ""
	case map[string]interface{}:
		return len(val) == 0
	case []interface{}:
		return len(val) == 0
	}
	return false
}

func convertRoles(roles []*om.Role) ([]userv1.Role, error) {
	var out []userv1.Role
	for i, r := range roles {
		if r == nil {
			return nil, fmt.Errorf("role at index %d is nil", i)
		}
		out = append(out, userv1.Role{
			RoleName: r.Role,
			Database: r.Database,
		})
	}
	return out, nil
}

