package migrate

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cast"
	"golang.org/x/xerrors"
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

// GenerateOptions holds parameters for CR generation that come from CLI flags.
type GenerateOptions struct {
	ResourceName          string
	CredentialsSecretName string
	ConfigMapName         string
	MultiClusterNames     []string
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
// It returns the YAML string, the resolved resource name, and any error.
func GenerateMongoDBCR(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	rs, err := getFirstReplicaSet(ac.Deployment)
	if err != nil {
		return "", "", xerrors.Errorf("error reading replica set: %w", err)
	}

	rsName := rs.Name()
	if rsName == "" {
		return "", "", xerrors.Errorf("replica set has no _id field")
	}

	members := rs.Members()
	processes := getSlice(ac.Deployment, "processes")
	processMap, err := buildProcessMap(processes)
	if err != nil {
		return "", "", xerrors.Errorf("error building process map: %w", err)
	}

	externalMembers, version, fcv, err := extractMemberInfo(members, processMap)
	if err != nil {
		return "", "", xerrors.Errorf("error extracting member info: %w", err)
	}

	resourceName := opts.ResourceName
	if resourceName == "" {
		resourceName = rsName
	}

	spec, err := buildMongoDBSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac, processMap, members)
	if err != nil {
		return "", "", xerrors.Errorf("error building MongoDB spec: %w", err)
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
		return "", "", xerrors.Errorf("error marshalling CR to YAML: %w", err)
	}

	return out, resourceName, nil
}

// GenerateMultiClusterCR generates a MongoDBMultiCluster CR from the given
// automation config and the list of target cluster names.
// Members are distributed as evenly as possible across the clusters.
func GenerateMultiClusterCR(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	rs, err := getFirstReplicaSet(ac.Deployment)
	if err != nil {
		return "", "", xerrors.Errorf("error reading replica set: %w", err)
	}

	rsName := rs.Name()
	if rsName == "" {
		return "", "", xerrors.Errorf("replica set has no _id field")
	}

	members := rs.Members()
	processes := getSlice(ac.Deployment, "processes")
	processMap, err := buildProcessMap(processes)
	if err != nil {
		return "", "", xerrors.Errorf("error building process map: %w", err)
	}

	externalMembers, version, fcv, err := extractMemberInfo(members, processMap)
	if err != nil {
		return "", "", xerrors.Errorf("error extracting member info: %w", err)
	}

	resourceName := opts.ResourceName
	if resourceName == "" {
		resourceName = rsName
	}

	spec, err := buildMultiClusterSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac, processMap, members)
	if err != nil {
		return "", "", xerrors.Errorf("error building MongoDBMultiCluster spec: %w", err)
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
		return "", "", xerrors.Errorf("error marshalling CR to YAML: %w", err)
	}

	return out, resourceName, nil
}

// buildMultiClusterSpec assembles the MongoDBMultiSpec, distributing members
// across the provided target clusters.
func buildMultiClusterSpec(
	version, fcv string,
	externalMembers []ExternalMember,
	rsName, resourceName string,
	opts GenerateOptions,
	ac *om.AutomationConfig,
	processMap map[string]map[string]interface{},
	members []om.ReplicaSetMember,
) (mdbmulti.MongoDBMultiSpec, error) {
	memberCount := len(externalMembers)

	processIDs := make([]string, memberCount)
	for i, em := range externalMembers {
		processIDs[i] = em.ProcessID
	}

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
			ExternalMembers: processIDs,
		},
		ClusterSpecList: clusterSpecList,
	}

	if resourceName != rsName {
		spec.DbCommonSpec.ReplicaSetNameOverride = rsName
	}

	if fcv != "" {
		spec.FeatureCompatibilityVersion = &fcv
	}

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

	agentConfig, err := extractAgentConfig(processMap, members)
	if err != nil {
		return mdbmulti.MongoDBMultiSpec{}, fmt.Errorf("error extracting agent config: %w", err)
	}
	if agentConfig.Mongod.HasLoggingConfigured() {
		spec.Agent = agentConfig
	}

	return spec, nil
}

// distributeMembers spreads memberCount as evenly as possible across the
// given cluster names. Extra members go to the earlier clusters.
func distributeMembers(memberCount int, clusterNames []string) mdbv1.ClusterSpecList {
	n := len(clusterNames)
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
	allConfig := buildMemberConfig(members)
	n := len(clusterNames)
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
	externalMembers []ExternalMember,
	rsName, resourceName string,
	opts GenerateOptions,
	ac *om.AutomationConfig,
	processMap map[string]map[string]interface{},
	members []om.ReplicaSetMember,
) (mdbv1.MongoDbSpec, error) {
	memberCount := len(externalMembers)

	processIDs := make([]string, memberCount)
	for i, em := range externalMembers {
		processIDs[i] = em.ProcessID
	}

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
			ExternalMembers: processIDs,
		},
		Members: memberCount,
	}

	if resourceName != rsName {
		spec.DbCommonSpec.ReplicaSetNameOverride = rsName
	}

	if fcv != "" {
		spec.FeatureCompatibilityVersion = &fcv
	}

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

	agentConfig, err := extractAgentConfig(processMap, members)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("error extracting agent config: %w", err)
	}
	if agentConfig.Mongod.HasLoggingConfigured() {
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

	var results []UserCROutput
	for i, user := range ac.Auth.Users {
		if user == nil {
			return nil, xerrors.Errorf("user at index %d is nil", i)
		}
		if user.Username == "" {
			return nil, xerrors.Errorf("user at index %d has an empty username", i)
		}

		if user.Username == ac.Auth.AutoUser && user.Database == util.DefaultUserDatabase {
			continue
		}

		needsPassword := user.Database != "$external"
		crName, err := normalizeK8sName(user.Username)
		if err != nil {
			return nil, xerrors.Errorf("error normalizing username %q: %w", user.Username, err)
		}
		passwordSecretName := crName + "-password"

		roles, err := convertRoles(user.Roles)
		if err != nil {
			return nil, xerrors.Errorf("error converting roles for user %q: %w", user.Username, err)
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
			return nil, xerrors.Errorf("error marshalling MongoDBUser CR for %q: %w", user.Username, err)
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
				Name:      "ldap-bind-query-password",
				Namespace: namespace,
			},
			StringData: map[string]string{
				"password": ac.Ldap.BindQueryPassword,
			},
		}
		out, err := marshalCRToYAML(sec)
		if err != nil {
			return nil, xerrors.Errorf("error marshalling LDAP bind query secret: %w", err)
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
				Name:      "ldap-ca",
				Namespace: namespace,
			},
			Data: map[string]string{
				"ca.pem": ac.Ldap.CaFileContents,
			},
		}
		out, err := marshalCRToYAML(cm)
		if err != nil {
			return nil, xerrors.Errorf("error marshalling LDAP CA configmap: %w", err)
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

		if tagsRaw, ok := m["tags"].(map[string]interface{}); ok && len(tagsRaw) > 0 {
			tags := make(map[string]string, len(tagsRaw))
			for k, val := range tagsRaw {
				tags[k] = cast.ToString(val)
			}
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
