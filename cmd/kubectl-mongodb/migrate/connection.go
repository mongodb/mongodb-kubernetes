package migrate

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sClient "sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/common"
)

// ProjectConfigs holds project-level agent and log rotation config read from the OM API.
type ProjectConfigs struct {
	MonitoringConfig *om.MonitoringAgentConfig
	BackupConfig     *om.BackupAgentConfig
	SystemLogRotate  *automationconfig.AcLogRotate
	AuditLogRotate   *automationconfig.AcLogRotate
}

var omConnectionFactory om.ConnectionFactory = om.NewOpsManagerConnection

type configReader struct {
	metav1.ObjectMeta
	configMapName, secretName, namespace string
}

func (r *configReader) GetProjectConfigMapName() string       { return r.configMapName }
func (r *configReader) GetProjectConfigMapNamespace() string  { return r.namespace }
func (r *configReader) GetCredentialsSecretName() string      { return r.secretName }
func (r *configReader) GetCredentialsSecretNamespace() string { return r.namespace }

func prepareConnection(ctx context.Context, namespace, configMapName, secretName string) (om.Connection, kubernetesClient.Client, error) {
	kubeClient, err := newKubeClient()
	if err != nil {
		return nil, nil, fmt.Errorf("error creating Kubernetes client: %w", err)
	}

	log := zap.S()
	reader := &configReader{configMapName: configMapName, secretName: secretName, namespace: namespace}
	secretClient := secrets.SecretClient{KubeClient: kubeClient}
	config, credentials, err := project.ReadConfigAndCredentials(ctx, kubeClient, secretClient, reader, log)
	if err != nil {
		return nil, nil, err
	}

	conn, err := resolveProjectReadOnly(config, credentials, log)
	if err != nil {
		return nil, nil, fmt.Errorf("error resolving Ops Manager project: %w", err)
	}
	return conn, kubeClient, nil
}

func resolveProjectReadOnly(config mdbv1.ProjectConfig, credentials mdbv1.Credentials, log *zap.SugaredLogger) (om.Connection, error) {
	omContext := om.OMContext{
		GroupName:                  config.ProjectName,
		OrgID:                      config.OrgID,
		BaseURL:                    config.BaseURL,
		PublicKey:                  credentials.PublicAPIKey,
		PrivateKey:                 credentials.PrivateAPIKey,
		AllowInvalidSSLCertificate: !config.SSLRequireValidMMSServerCertificates,
		CACertificate:              config.SSLMMSCAConfigMapContents,
	}
	conn := omConnectionFactory(&omContext)

	org, err := project.FindOrganization(config.OrgID, config.ProjectName, conn, log)
	if err != nil {
		return nil, err
	}
	if org == nil {
		return nil, fmt.Errorf("organization not found for project name %q", config.ProjectName)
	}

	proj, err := project.FindProjectInsideOrganization(conn, config.ProjectName, org, log)
	if err != nil {
		return nil, err
	}
	if proj == nil {
		return nil, fmt.Errorf("project %q not found in organization %s (%q)", config.ProjectName, org.ID, org.Name)
	}

	conn.ConfigureProject(proj)
	return conn, nil
}

func newKubeClient() (kubernetesClient.Client, error) {
	kubeConfigPath := common.LoadKubeConfigFilePath()
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	cl, err := k8sClient.New(restConfig, k8sClient.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, err
	}
	return kubernetesClient.NewClient(cl), nil
}

func readProjectConfigs(conn om.Connection) (*ProjectConfigs, error) {
	monitoringConfig, err := conn.ReadMonitoringAgentConfig()
	if err != nil {
		return nil, fmt.Errorf("error reading monitoring agent config: %w", err)
	}
	backupConfig, err := conn.ReadBackupAgentConfig()
	if err != nil {
		return nil, fmt.Errorf("error reading backup agent config: %w", err)
	}
	systemLogRotate, err := conn.ReadProcessLogRotation()
	if err != nil {
		return nil, fmt.Errorf("error reading system log rotate config: %w", err)
	}
	auditLogRotate, err := conn.ReadAuditLogRotation()
	if err != nil {
		return nil, fmt.Errorf("error reading audit log rotate config: %w", err)
	}
	return &ProjectConfigs{
		MonitoringConfig: monitoringConfig,
		BackupConfig:     backupConfig,
		SystemLogRotate:  systemLogRotate,
		AuditLogRotate:   auditLogRotate,
	}, nil
}
