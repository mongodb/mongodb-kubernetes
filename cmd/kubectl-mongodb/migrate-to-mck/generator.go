package migratetomck

import (
	"encoding/json"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	authn "github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/pkg/passwordhash"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// operatorVersion returns the operator version stamped at build time, or "latest" for development builds.
func operatorVersion() string {
	if util.OperatorVersion == "" {
		return "latest"
	}
	return util.OperatorVersion
}

const (
	PrometheusPasswordSecretName = "prometheus-password"
	PrometheusTLSSecretName      = "prometheus-tls"
	LdapBindQuerySecretName      = "ldap-bind-query-password" //nolint:gosec // secret name, not a credential
	LdapAgentPasswordSecretName  = "ldap-agent-password"      //nolint:gosec // secret name, not a credential
	LdapCAConfigMapName          = "ldap-ca"
	LdapCAKey                    = "ca.pem"

	externalDatabase = "$external" // MongoDB virtual database for X.509 and LDAP users.

	// passwordSecretDataKey is the Secret data key used for all generated and referenced password Secrets.
	passwordSecretDataKey = "password" //nolint:gosec // data key name, not a credential
)

// GenerateOptions holds CLI flags, OM-fetched configs, and validation outputs for CR generation.
// It does not duplicate fields that are directly derivable from the automation config.
type GenerateOptions struct {
	// CLI flags
	ResourceNameOverride  string
	CredentialsSecretName string
	ConfigMapName         string
	Namespace             string
	CertsSecretPrefix     string // spec.security.certsSecretPrefix, required when TLS is enabled

	// Fetched from OM
	ProjectConfigs *ProjectConfigs

	// Output of ValidateMigration — the process used as the template for spec fields (e.g. version, args).
	SourceProcess *om.Process

	// User credentials — maps "username:database" to a pre-created Secret name; no Secret YAML is written
	ExistingUserSecrets map[string]string

	// Prometheus credentials
	PrometheusSecretName string // name of a pre-created Secret; no Secret YAML is written when set

	// PrometheusPassword holds the plaintext password read from the Secret for validation against the
	// automation config's passwordHash/passwordSalt. Empty when collected from the CLI path.
	PrometheusPassword string
}

// resolveK8sResourceName resolves the K8s resource name from the AC name or an explicit override.
// Returns "" when the name cannot be normalized and no override was provided.
func resolveK8sResourceName(acName string, opts GenerateOptions) string {
	if opts.ResourceNameOverride != "" {
		return opts.ResourceNameOverride
	}
	return util.NormalizeName(acName)
}

// buildDbCommonSpec constructs the DbCommonSpec shared by replica set and sharded cluster specs,
// including version, FCV, security, Prometheus, connection, and agent config.
func buildDbCommonSpec(ac *om.AutomationConfig, opts GenerateOptions, version, fcv string, resourceType mdbv1.ResourceType, resourceName string) (mdbv1.DbCommonSpec, error) {
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
		if acProm := ac.Deployment.GetPrometheus(); acProm != nil && acProm.PasswordSalt != "" {
			if opts.PrometheusPassword == "" {
				return mdbv1.DbCommonSpec{}, fmt.Errorf("prometheus is enabled with a password hash in the automation config but no password was provided; create a Kubernetes Secret with the password and pass --prometheus-secret-name")
			}
			match, pErr := passwordhash.PasswordMatchesHash(opts.PrometheusPassword, acProm.PasswordHash, acProm.PasswordSalt)
			if pErr != nil {
				return mdbv1.DbCommonSpec{}, fmt.Errorf("failed to verify prometheus password against automation config: %w", pErr)
			}
			if !match {
				return mdbv1.DbCommonSpec{}, fmt.Errorf("prometheus password in Secret %q does not match the password in the automation config", opts.PrometheusSecretName)
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
		ResourceType:                resourceType,
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

// GenerateMongoDBCR generates a MongoDB CR for the given topology.
func GenerateMongoDBCR(ac *om.AutomationConfig, opts GenerateOptions) (client.Object, string, error) {
	if len(ac.Deployment.GetShardedClusters()) > 0 {
		return generateShardedCluster(ac, opts)
	}
	return generateReplicaSet(ac, opts)
}

// generateExtraResources returns the supporting Secrets/ConfigMaps for LDAP and Prometheus.
func generateExtraResources(ac *om.AutomationConfig, opts GenerateOptions) []client.Object {
	var resources []client.Object
	if ldap := ac.Ldap; ldap != nil {
		if ldap.BindQueryPassword != "" {
			resources = append(resources, GeneratePasswordSecret(LdapBindQuerySecretName, opts.Namespace, ldap.BindQueryPassword))
		}
		if ldap.CaFileContents != "" {
			resources = append(resources, buildLdapCAConfigMap(opts.Namespace, ldap.CaFileContents))
		}
	}
	// An LDAP agent authenticates with an external password the operator cannot derive, so carry it
	// over as a Secret referenced by spec.security.authentication.agents.automationPasswordSecretRef.
	if ac.Auth != nil && ac.Auth.AutoPwd != "" {
		if mode, ok := authn.MapMechanismToAuthMode(ac.Auth.AutoAuthMechanism); ok && mode == util.LDAP {
			resources = append(resources, GeneratePasswordSecret(LdapAgentPasswordSecretName, opts.Namespace, ac.Auth.AutoPwd))
		}
	}
	return resources
}

// renderObjects serializes objects to the same multi-document YAML written by the CLI output path.
func renderObjects(objects []client.Object) (string, error) {
	var sb strings.Builder
	for i, obj := range objects {
		if i > 0 {
			sb.WriteString("---\n")
		}
		y, err := marshalCRToYAML(obj)
		if err != nil {
			return "", fmt.Errorf("failed to marshal %T: %w", obj, err)
		}
		sb.WriteString(y)
	}
	return sb.String(), nil
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

func buildCRObjectMeta(name, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Annotations: map[string]string{
			util.MigrateToolVersionAnnotation: operatorVersion(),
			util.MigrationDryRunAnnotation:    "true",
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
