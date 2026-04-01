package migrate

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

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
	LdapBindQuerySecretName      = "ldap-bind-query-password"
	LdapCAConfigMapName          = "ldap-ca"
	LdapCAKey                    = "ca.pem"

	migrateToolVersionAnnotation = "mongodb.com/migrate-tool-version"
)

// GenerateOptions holds parameters for CR generation that come from CLI flags
// and deployment-level agent configuration read from the OM API.
type GenerateOptions struct {
	ReplicaSetNameOverride string
	CredentialsSecretName  string
	ConfigMapName          string
	Namespace              string
	MultiClusterNames      []string
	AgentConfigs           *ProjectAgentConfigs
	ProcessConfigs         *ProjectProcessConfigs
	// CertsSecretPrefix is spec.security.certsSecretPrefix; required when TLS is enabled.
	CertsSecretPrefix string
	// ProcessMap, Members, and SourceProcess are pre-extracted from the automation
	// config in runGenerate so that validation, TLS detection, and spec building
	// all share the same values without re-parsing the deployment.
	// SourceProcess is the process of the first active data-bearing member, used
	// as the source for spec.additionalMongodConfig and spec.agent.mongod.systemLog.
	ProcessMap    map[string]om.Process
	Members       []om.ReplicaSetMember
	SourceProcess *om.Process
}

// UserCROutput holds the generated YAML and metadata for a single MongoDBUser CR.
type UserCROutput struct {
	YAML           string
	Username       string
	Database       string
	NeedsPassword  bool
	PasswordSecret string
	// MigratedFromVM mirrors spec.migratedFromVm: true when OM had an
	// explicit mechanisms list, so the operator preserves only those mechanisms.
	MigratedFromVM bool
}

// GenerateMongoDBCR generates a MongoDB CR from the given automation config.
// It detects the deployment type (replica set vs sharded cluster) and topology
// (single vs multi-cluster) and dispatches to the appropriate builder.
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

// isValidKubernetesName returns true when name is a valid DNS label (RFC 1123),
// which is the requirement for most Kubernetes resource names.
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
	externalMembers, version, fcv := om.ExtractMemberInfo(opts.Members, opts.ProcessMap)

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
			Name: resourceName,
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
			Name: resourceName,
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
		crName := userv1.NormalizeName(user.Username)
		if crName == "" {
			return nil, fmt.Errorf("username %q cannot be normalized to a valid Kubernetes name: no alphanumeric characters", user.Username)
		}
		if prev, exists := seenCRNames[crName]; exists {
			return nil, fmt.Errorf("users %q and %q normalize to the same Kubernetes name %q; rename one before migration", prev, user.Username, crName)
		}
		seenCRNames[crName] = user.Username
		passwordSecretName := crName + "-password"

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
		if needsPassword {
			spec.PasswordSecretKeyRef = userv1.SecretKeyRef{
				Name: passwordSecretName,
				Key:  "password",
			}
		}
		if len(user.Mechanisms) > 0 {
			t := true
			spec.MigratedFromVM = &t
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
			return nil, fmt.Errorf("failed to marshal MongoDBUser Custom Resource for %q: %w", user.Username, err)
		}

		results = append(results, UserCROutput{
			YAML:           userYAML,
			Username:       user.Username,
			Database:       user.Database,
			NeedsPassword:  needsPassword,
			PasswordSecret: passwordSecretName,
			MigratedFromVM: len(user.Mechanisms) > 0,
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


// GenerateLdapResources creates the Secret and/or ConfigMap that the CR's
// LDAP config references. Returns empty strings when the AC has no LDAP configuration.
func GenerateLdapResources(ac *om.AutomationConfig, namespace string) (bindQueryPasswordSecret, caConfigMap string, err error) {
	if ac.Ldap == nil {
		return "", "", nil
	}

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
		bindQueryPasswordSecret, err = marshalCRToYAML(sec)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal LDAP bind query secret: %w", err)
		}
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
		caConfigMap, err = marshalCRToYAML(cm)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal LDAP CA ConfigMap: %w", err)
		}
	}

	return bindQueryPasswordSecret, caConfigMap, nil
}

// marshalCRToYAML marshals a Kubernetes resource to YAML, stripping the
// "status" block, "creationTimestamp: null", and structurally-empty fields
// (empty strings, nil, empty maps/slices). Numbers and booleans are always
// preserved — 0 and false can be semantically meaningful (e.g. votes: 0,
// logAppend: false).
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

// stripZeroValues recursively removes structurally-empty entries from a map:
// empty strings, nil, empty maps, and empty slices. Numbers and booleans are
// never removed.
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

