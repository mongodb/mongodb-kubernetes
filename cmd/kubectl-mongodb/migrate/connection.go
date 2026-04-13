package migrate

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	k8sClient "sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/common"
)

var omConnectionFactory om.ConnectionFactory = om.NewOpsManagerConnection

func prepareConnection(ctx context.Context, namespace, configMapName, secretName string) (om.Connection, kubernetesClient.Client, error) {
	kubeClient, err := newKubeClient()
	if err != nil {
		return nil, nil, fmt.Errorf("error creating Kubernetes client: %w", err)
	}

	log := zap.S()
	config, credentials, err := readConfigAndCredentials(ctx, kubeClient, log, namespace, configMapName, secretName)
	if err != nil {
		return nil, nil, err
	}

	conn, err := resolveProjectReadOnly(config, credentials, log)
	if err != nil {
		return nil, nil, fmt.Errorf("error resolving Ops Manager project: %w", err)
	}
	return conn, kubeClient, nil
}

func readConfigAndCredentials(ctx context.Context, kubeClient kubernetesClient.Client, log *zap.SugaredLogger, namespace, configMapName, secretName string) (mdbv1.ProjectConfig, mdbv1.Credentials, error) {
	config, err := project.ReadProjectConfig(ctx, kubeClient, kube.ObjectKey(namespace, configMapName), "")
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, fmt.Errorf("error reading project config: %w", err)
	}
	if config.ProjectName == "" {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, fmt.Errorf("ConfigMap %s/%s does not contain a projectName", namespace, configMapName)
	}
	secretClient := secrets.SecretClient{KubeClient: kubeClient}
	credentials, err := project.ReadCredentials(ctx, secretClient, kube.ObjectKey(namespace, secretName), log)
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, fmt.Errorf("error reading credentials secret: %w", err)
	}
	return config, credentials, nil
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

// ProjectConfigs holds project-level agent and log rotation config read from the OM API.
type ProjectConfigs struct {
	MonitoringConfig *om.MonitoringAgentConfig
	BackupConfig     *om.BackupAgentConfig
	SystemLogRotate  *automationconfig.AcLogRotate
	AuditLogRotate   *automationconfig.AcLogRotate
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

func newKubeClient() (kubernetesClient.Client, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		kubeConfigPath := common.LoadKubeConfigFilePath()
		loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath}
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	cl, err := k8sClient.New(restConfig, k8sClient.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, err
	}
	return kubernetesClient.NewClient(cl), nil
}
