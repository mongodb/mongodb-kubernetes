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
	MigrationDryRunAnnotation    = "mongodb.com/migration-dry-run"

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
	PrometheusPassword   string // plaintext password; a Secret is generated when set
	PrometheusSecretName string // name of a pre-created Secret; no Secret YAML is written when set
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
// When the resource is wrapped in a yamlCommentCarrier, the spec comment is spliced into the produced YAML.
func marshalCRToYAML(obj client.Object) (string, error) {
	var specComment string
	if w, ok := obj.(*yamlCommentCarrier); ok {
		obj, specComment = w.Object, w.specComment
	}
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
	produced := string(out)
	if specComment != "" {
		// TODO: drop the comment splice once the CRD supports configServerNameOverride
		// and shardNameOverrides as real fields, then populate them on the spec directly.
		const marker = "\n  type: ShardedCluster\n"
		if strings.Count(produced, marker) != 1 {
			return "", fmt.Errorf("cannot inject spec comment: expected exactly one %q anchor in produced YAML, got %d", marker, strings.Count(produced, marker))
		}
		produced = strings.Replace(produced, marker, "\n"+specComment+"  type: ShardedCluster\n", 1)
	}
	return produced, nil
}

// yamlCommentCarrier wraps a client.Object so a spec-level YAML comment block can travel through
// the GenerateMongoDBCR call chain and be spliced in by marshalCRToYAML. Code paths that bypass
// marshalCRToYAML drop the comment, so consumers that need it must go through that helper.
type yamlCommentCarrier struct {
	client.Object
	specComment string
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
			MigrationDryRunAnnotation:    "true",
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
