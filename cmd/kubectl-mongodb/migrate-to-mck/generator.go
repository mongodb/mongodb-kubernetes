package migratetomck

import (
	"encoding/json"
	"fmt"
	"os"

	"sigs.k8s.io/controller-runtime/pkg/client"
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

	// passwordSecretDataKey is the Secret data key used for all generated and referenced password Secrets.
	passwordSecretDataKey = "password" //nolint:gosec // data key name, not a credential
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

	// Interactive flow (prompted at runtime)
	UserPasswords      map[string]string // maps "username:database" to plaintext passwords; a Secret is generated for each
	PrometheusPassword string            // plaintext password; a prometheus Secret is generated from it

	// Non-interactive flow (supplied via flags)
	ExistingUserSecrets  map[string]string // maps "username:database" to pre-created Secret name; no Secret YAML emitted
	PrometheusSecretName string            // name of a pre-created prometheus Secret; no Secret YAML emitted
}

// GenerateMongoDBCR generates a MongoDB CR for the given topology.
func GenerateMongoDBCR(ac *om.AutomationConfig, opts GenerateOptions) (client.Object, string, error) {
	isSharded := len(ac.Deployment.GetShardedClusters()) > 0

	if isSharded {
		if len(opts.MultiClusterNames) > 0 {
			return nil, "", fmt.Errorf("sharded cluster multi-cluster migration is not yet supported")
		}
		return nil, "", fmt.Errorf("sharded cluster migration is not yet supported")
	}
	return generateReplicaSet(ac, opts)
}

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
	spec, err := buildReplicaSetSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac)
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
	spec, err := buildReplicaSetMultiClusterSpec(version, fcv, externalMembers, rsName, resourceName, opts, ac)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build multi-cluster spec: %w", err)
	}
	return &mdbmulti.MongoDBMultiCluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDBMultiCluster"},
		ObjectMeta: buildCRObjectMeta(resourceName, opts.Namespace),
		Spec:       spec,
	}, resourceName, nil
}

func buildCRObjectMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Annotations: map[string]string{
			migrateToolVersionAnnotation: versionutil.StaticContainersOperatorVersion(),
		},
	}
}

// GenerateUserCRs creates MongoDBUser CRs for each user in auth.usersWanted, skipping the agent user.
// When opts.ExistingUserSecrets is set, each SCRAM user CR references a pre-created Secret (no Secret
// emitted, absent users skipped). Otherwise opts.UserPasswords is used to generate new Secrets.
// External (X.509/LDAP) users never produce a Secret.
// Returns objects in document order: MongoDBUser followed immediately by its Secret (if generated).
func GenerateUserCRs(ac *om.AutomationConfig, mongodbResourceName, namespace string, opts GenerateOptions) ([]client.Object, error) {
	if ac.Auth == nil || len(ac.Auth.Users) == 0 {
		return nil, nil
	}

	crNameToUsername := map[string]string{}
	var results []client.Object
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

		var passwordSecret *corev1.Secret
		if user.Database != externalDatabase {
			if opts.ExistingUserSecrets != nil {
				sName, ok := opts.ExistingUserSecrets[userKey(user.Username, user.Database)]
				if !ok {
					fmt.Fprintf(os.Stderr, "[WARNING] skipping user %q (db: %s): not found in --users-secrets-file\n", user.Username, user.Database)
					continue
				}
				spec.PasswordSecretKeyRef = userv1.SecretKeyRef{Name: sName, Key: passwordSecretDataKey}
			} else {
				passwordSecretName := crName + "-password"
				if errs := k8svalidation.IsDNS1123Subdomain(passwordSecretName); len(errs) > 0 {
					return nil, fmt.Errorf("generated password Secret name %q is not a valid Kubernetes name; rename user %q before migration: %s", passwordSecretName, user.Username, errs[0])
				}
				password, ok := opts.UserPasswords[userKey(user.Username, user.Database)]
				if !ok {
					fmt.Fprintf(os.Stderr, "[WARNING] skipping user %q (db: %s): no password provided\n", user.Username, user.Database)
					continue
				}
				spec.PasswordSecretKeyRef = userv1.SecretKeyRef{Name: passwordSecretName, Key: passwordSecretDataKey}
				passwordSecret = GeneratePasswordSecret(passwordSecretName, namespace, password)
			}
		}

		results = append(results, &userv1.MongoDBUser{
			TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDBUser"},
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: namespace},
			Spec:       spec,
		})
		if passwordSecret != nil {
			results = append(results, passwordSecret)
		}
	}

	return results, nil
}

// GeneratePasswordSecret returns a Kubernetes Secret for a SCRAM user's password.
func GeneratePasswordSecret(secretName, namespace, password string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		StringData: map[string]string{
			passwordSecretDataKey: password,
		},
	}
}

func buildLdapCAConfigMap(namespace, caFileContents string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
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
func marshalCRToYAML(obj client.Object) (string, error) {
	jsonBytes, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return "", err
	}
	delete(m, "status")
	if meta, ok := m["metadata"].(map[string]any); ok {
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
func stripZeroValues(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case nil:
			delete(m, k)
		case string:
			if val == "" {
				delete(m, k)
			}
		case map[string]any:
			stripZeroValues(val)
			if len(val) == 0 {
				delete(m, k)
			}
		case []any:
			if len(val) == 0 {
				delete(m, k)
			} else {
				for i, item := range val {
					if sub, ok := item.(map[string]any); ok {
						stripZeroValues(sub)
						val[i] = sub
					}
				}
			}
		}
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
