package migratetomck

import (
	"encoding/json"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
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
	ResourceNameOverride  string
	CredentialsSecretName string
	ConfigMapName         string
	Namespace             string
	MultiClusterNames     []string
	CertsSecretPrefix     string // spec.security.certsSecretPrefix, required when TLS is enabled

	// Fetched from OM
	ProjectConfigs *ProjectConfigs

	// Output of ValidateMigration — the process used as the template for spec fields (e.g. version, args).
	SourceProcess *om.Process

	// Interactive flow (prompted at runtime)
	UserPasswords      map[string]string // maps "username:database" to plaintext passwords, a Secret is generated for each
	PrometheusPassword string            // plaintext password, a prometheus Secret is generated from it

	// Non-interactive flow (supplied via flags)
	ExistingUserSecrets  map[string]string // maps "username:database" to a precreated Secret name, no Secret YAML emitted
	PrometheusSecretName string            // name of a precreated prometheus Secret, no Secret YAML emitted
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
	acProm := ac.Deployment.GetPrometheus()
	if acProm != nil && acProm.Enabled && acProm.Username != "" {
		if opts.PrometheusSecretName == "" && opts.PrometheusPassword != "" {
			resources = append(resources, GeneratePasswordSecret(PrometheusPasswordSecretName, opts.Namespace, opts.PrometheusPassword))
		}
	}
	return resources
}

// marshalMultiDoc serializes each object to YAML, joined by YAML document separator markers.
func marshalMultiDoc(objects []client.Object) (string, error) {
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
			migrateToolVersionAnnotation: versionutil.StaticContainersOperatorVersion(),
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
