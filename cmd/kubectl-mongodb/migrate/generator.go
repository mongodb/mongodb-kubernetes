package migrate

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cast"
	"golang.org/x/xerrors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
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

	rsName := cast.ToString(rs["_id"])
	if rsName == "" {
		return "", "", xerrors.Errorf("replica set has no _id field")
	}

	members := getSlice(rs, "members")
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
	members []interface{},
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

	memberConfig, err := buildMemberConfig(members)
	if err != nil {
		return mdbv1.MongoDbSpec{}, fmt.Errorf("error building member config: %w", err)
	}
	spec.MemberConfig = memberConfig

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
		crName := normalizeK8sName(user.Username)
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

// buildMemberConfig creates MemberOptions for each member with votes=0 and
// priority="0" (draining policy for external members being transitioned).
// Tags are preserved from the automation config.
func buildMemberConfig(members []interface{}) ([]automationconfig.MemberOptions, error) {
	config := make([]automationconfig.MemberOptions, len(members))

	for i, m := range members {
		v, p := 0, "0"
		config[i] = automationconfig.MemberOptions{
			Votes:    &v,
			Priority: &p,
		}

		member, ok := m.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("member at index %d is not a valid map", i)
		}
		if tagsRaw, ok := member["tags"].(map[string]interface{}); ok && len(tagsRaw) > 0 {
			tags := make(map[string]string, len(tagsRaw))
			for k, val := range tagsRaw {
				tags[k] = cast.ToString(val)
			}
			config[i].Tags = tags
		}
	}
	return config, nil
}

// marshalCRToYAML marshals a Kubernetes resource to YAML, stripping fields
// that are artifacts of using the full CRD struct: the "status" block,
// "creationTimestamp: null", and any zero-value / empty nested fields that
// the struct emits because their JSON tags lack omitempty.
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
	pruneEmptyValues(m)
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// pruneEmptyValues recursively removes null, empty string, zero numeric,
// and empty container values from a map. Array elements are left intact to
// preserve intentionally set zero values (e.g. votes: 0 in memberConfig).
func pruneEmptyValues(m map[string]interface{}) {
	for k, v := range m {
		if isZeroValue(v) {
			delete(m, k)
			continue
		}
		if sub, ok := v.(map[string]interface{}); ok {
			pruneEmptyValues(sub)
			if len(sub) == 0 {
				delete(m, k)
			}
		}
	}
}

func isZeroValue(v interface{}) bool {
	switch val := v.(type) {
	case nil:
		return true
	case string:
		return val == ""
	case float64:
		return val == 0
	case bool:
		return false
	case []interface{}:
		return len(val) == 0
	case map[string]interface{}:
		return len(val) == 0
	default:
		return false
	}
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
