package migratetomck

import (
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

const (
	PrometheusPasswordSecretName = "prometheus-password"
	PrometheusTLSSecretName      = "prometheus-tls"
	LdapBindQuerySecretName      = "ldap-bind-query-password" //nolint:gosec // secret name, not a credential
	LdapCAConfigMapName          = "ldap-ca"
	LdapCAKey                    = "ca.pem"

	migrateToolVersionAnnotation = "mongodb.com/migrate-tool-version"

	externalDatabase = "$external" // MongoDB virtual database for X.509 and LDAP users.
)

// GenerateOptions holds CLI flags, OM-fetched configs, and validation outputs for CR generation.
// It does not duplicate fields that are directly derivable from the automation config.
type GenerateOptions struct {
	// CLI flags
	ReplicaSetNameOverride string
	CredentialsSecretName  string
	ConfigMapName          string
	Namespace              string
	MultiClusterNames      []string
	CertsSecretPrefix      string // spec.security.certsSecretPrefix; required when TLS is enabled

	// Fetched from OM
	ProjectConfigs *ProjectConfigs

	// Output of ValidateMigration — the process used as the template for spec fields (e.g. version, args).
	SourceProcess *om.Process

	// Collected interactively
	UserPasswords      map[string]string // maps "username:database" to plaintext passwords for SCRAM users
	PrometheusPassword string
}

// UserCROutput holds the generated YAML for a single MongoDBUser CR and its password Secret (if any).
type UserCROutput struct {
	MongoDBUserYAML    string
	PasswordSecretYAML string // empty for external (X.509/LDAP) users that have no password
}

// GenerateMongoDBCR generates a MongoDB CR for the given topology.
func GenerateMongoDBCR(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	isSharded := len(ac.Deployment.GetShardedClusters()) > 0

	if isSharded {
		if len(opts.MultiClusterNames) > 0 {
			return "", "", fmt.Errorf("sharded cluster multi-cluster migration is not yet supported")
		}
		return "", "", fmt.Errorf("sharded cluster migration is not yet supported")
	}
	return generateReplicaSet(ac, opts)
}

// isValidKubernetesName reports whether name is a valid DNS label (RFC 1123).
func isValidKubernetesName(name string) bool {
	return len(k8svalidation.IsDNS1123Label(name)) == 0
}

func generateReplicaSet(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	replicaSets := ac.Deployment.GetReplicaSets()
	if len(replicaSets) == 0 {
		return "", "", fmt.Errorf("no replica sets found in the automation config")
	}
	rs := replicaSets[0]

	rsName := rs.Name()
	externalMembers, version, fcv := om.ExtractMemberInfo(rs.Members(), ac.Deployment.ProcessMap())

	resourceName := opts.ReplicaSetNameOverride
	if resourceName == "" {
		if !isValidKubernetesName(rsName) {
			return "", "", fmt.Errorf("replica set name %q is not a valid Kubernetes resource name. Use --replicaset-name-override to provide a valid name (spec.replicaSetNameOverride will be set automatically)", rsName)
		}
		resourceName = rsName
	}

	if len(opts.MultiClusterNames) > 0 {
		return generateReplicaSetMultiCluster(ac, opts, rsName, resourceName, version, fcv, externalMembers)
	}
	return generateReplicaSetSingleCluster(ac, opts, rsName, resourceName, version, fcv, externalMembers)
}

func generateReplicaSetSingleCluster(ac *om.AutomationConfig, opts GenerateOptions, rsName, resourceName, version, fcv string, externalMembers []mdbv1.ExternalMember) (string, string, error) {
	spec, err := buildReplicaSetSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac)
	if err != nil {
		return "", "", fmt.Errorf("failed to build MongoDB spec: %w", err)
	}
	cr := mdbv1.MongoDB{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mongodb.com/v1",
			Kind:       "MongoDB",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: opts.Namespace,
			Annotations: map[string]string{
				migrateToolVersionAnnotation: versionutil.StaticContainersOperatorVersion(),
			},
		},
		Spec: spec,
	}
	out, err := marshalCRToYAML(cr)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal Custom Resource to YAML: %w", err)
	}
	return out, resourceName, nil
}

func generateReplicaSetMultiCluster(ac *om.AutomationConfig, opts GenerateOptions, rsName, resourceName, version, fcv string, externalMembers []mdbv1.ExternalMember) (string, string, error) {
	spec, err := buildReplicaSetMultiClusterSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac)
	if err != nil {
		return "", "", fmt.Errorf("failed to build multi-cluster spec: %w", err)
	}
	cr := mdbmulti.MongoDBMultiCluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "mongodb.com/v1",
			Kind:       "MongoDBMultiCluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: opts.Namespace,
			Annotations: map[string]string{
				migrateToolVersionAnnotation: versionutil.StaticContainersOperatorVersion(),
			},
		},
		Spec: spec,
	}
	out, err := marshalCRToYAML(cr)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal Custom Resource to YAML: %w", err)
	}
	return out, resourceName, nil
}

// GenerateUserCRs creates MongoDBUser CRs for each user in auth.usersWanted, skipping the agent user.
// passwords maps "username:database" to plaintext password; entries are used to generate the
// accompanying Secret for each SCRAM user. External (X.509/LDAP) users produce no Secret.
func GenerateUserCRs(ac *om.AutomationConfig, mongodbResourceName, namespace string, passwords map[string]string) ([]UserCROutput, error) {
	if ac.Auth == nil || len(ac.Auth.Users) == 0 {
		return nil, nil
	}

	crNameToUsername := map[string]string{}
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

		crName := userv1.NormalizeName(user.Username)
		if crName == "" {
			return nil, fmt.Errorf("username %q cannot be normalized to a valid Kubernetes name: no alphanumeric characters", user.Username)
		}
		if prev, exists := crNameToUsername[crName]; exists {
			return nil, fmt.Errorf("users %q and %q normalize to the same Kubernetes name %q; rename one before migration", prev, user.Username, crName)
		}
		crNameToUsername[crName] = user.Username

		roles, err := convertRoles(user.Roles)
		if err != nil {
			return nil, fmt.Errorf("failed to convert roles for user %q: %w", user.Username, err)
		}

		spec := userv1.MongoDBUserSpec{
			Username: user.Username,
			Database: user.Database,
			MongoDBResourceRef: userv1.MongoDBResourceRef{
				Name: mongodbResourceName,
			},
			Roles: roles,
		}

		var secretYAML string
		passwordSecretName := crName + "-password"
		if user.Database != externalDatabase {
			spec.PasswordSecretKeyRef = userv1.SecretKeyRef{
				Name: passwordSecretName,
				Key:  "password",
			}
			if password, ok := passwords[userKey(user.Username, user.Database)]; ok {
				secretYAML, err = marshalCRToYAML(GeneratePasswordSecret(passwordSecretName, namespace, password))
				if err != nil {
					return nil, fmt.Errorf("failed to marshal password Secret for user %q: %w", user.Username, err)
				}
			}
		}

		userYAML, err := marshalCRToYAML(userv1.MongoDBUser{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "mongodb.com/v1",
				Kind:       "MongoDBUser",
			},
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: namespace},
			Spec:       spec,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal MongoDBUser Custom Resource for %q: %w", user.Username, err)
		}

		results = append(results, UserCROutput{
			MongoDBUserYAML:    userYAML,
			PasswordSecretYAML: secretYAML,
		})
	}

	return results, nil
}

// GeneratePasswordSecret returns a Kubernetes Secret for a SCRAM user's password.
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

func buildLdapCAConfigMap(namespace, caFileContents string) corev1.ConfigMap {
	return corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      LdapCAConfigMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			LdapCAKey: caFileContents,
		},
	}
}

// marshalCRToYAML marshals a resource to YAML, stripping status, creationTimestamp, and empty fields.
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

// stripZeroValues recursively removes nil, empty strings, maps, and slices.
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

// isZeroValue reports whether v is nil, an empty string, map, or slice.
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
