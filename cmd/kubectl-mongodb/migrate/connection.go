package migrate

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	k8sClient "sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/common"
)

var omConnectionFactory om.ConnectionFactory = om.NewOpsManagerConnection

func prepareConnection(ctx context.Context) (om.Connection, kubernetesClient.Client, error) {
	kubeClient, err := newKubeClient()
	if err != nil {
		return nil, nil, fmt.Errorf("error creating Kubernetes client: %w", err)
	}

	log := zap.S()
	config, credentials, err := readConfigAndCredentials(ctx, kubeClient, log)
	if err != nil {
		return nil, nil, err
	}

	conn, err := resolveProjectReadOnly(config, credentials, log)
	if err != nil {
		return nil, nil, fmt.Errorf("error resolving Ops Manager project: %w", err)
	}
	return conn, kubeClient, nil
}

func readConfigAndCredentials(ctx context.Context, kubeClient kubernetesClient.Client, log *zap.SugaredLogger) (mdbv1.ProjectConfig, mdbv1.Credentials, error) {
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

	org, err := resolveOrganization(conn, config.OrgID, config.ProjectName, log)
	if err != nil {
		return nil, err
	}
	if org == nil {
		return nil, fmt.Errorf("organization not found for project name %q", config.ProjectName)
	}

	proj, err := resolveProjectInOrg(conn, config.ProjectName, org)
	if err != nil {
		return nil, err
	}
	if proj == nil {
		return nil, fmt.Errorf("project %q not found in organization %s (%q)", config.ProjectName, org.ID, org.Name)
	}

	conn.ConfigureProject(proj)
	return conn, nil
}

func resolveOrgIDByName(conn om.Connection, name string) (string, error) {
	organizations, err := conn.ReadOrganizationsByName(name)
	if err != nil {
		if v, ok := err.(*apierror.Error); ok && v.ErrorCode == apierror.OrganizationNotFound {
			return "", nil
		}
		return "", fmt.Errorf("could not find organization %s: %w", name, err)
	}
	for _, org := range organizations {
		if org.Name == name {
			return org.ID, nil
		}
	}
	return "", nil
}

func resolveOrganization(conn om.Connection, orgID string, projectName string, log *zap.SugaredLogger) (*om.Organization, error) {
	if orgID == "" {
		log.Debugf("Organization id is not specified - trying to find the organization with name %q", projectName)
		var err error
		if orgID, err = resolveOrgIDByName(conn, projectName); err != nil {
			return nil, err
		}
		if orgID == "" {
			return nil, nil
		}
	}

	organization, err := conn.ReadOrganization(orgID)
	if err != nil {
		return nil, fmt.Errorf("organization with id %s not found: %w", orgID, err)
	}
	return organization, nil
}

func resolveProjectInOrg(conn om.Connection, projectName string, organization *om.Organization) (*om.Project, error) {
	projects, err := conn.ReadProjectsInOrganizationByName(organization.ID, projectName)
	if err != nil {
		if v, ok := err.(*apierror.Error); ok && v.ErrorCode == apierror.ProjectNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("error looking up project %s in organization %s: %w", projectName, organization.ID, err)
	}
	var found *om.Project
	var names []string
	for _, p := range projects {
		if p.Name == projectName {
			found = p
			names = append(names, fmt.Sprintf("%s (%s)", p.Name, p.ID))
		}
	}
	if len(names) == 1 {
		return found, nil
	}
	if len(names) > 1 {
		return nil, fmt.Errorf("found more than one project with name %s in organization %s (%s): %s", projectName, organization.ID, organization.Name, strings.Join(names, ", "))
	}
	return nil, nil
}

// ProjectAgentConfigs holds monitoring and backup agent config read from the OM API.
type ProjectAgentConfigs struct {
	MonitoringConfig *om.MonitoringAgentConfig
	BackupConfig     *om.BackupAgentConfig
}

// ProjectProcessConfigs holds log rotation config read from the OM API.
type ProjectProcessConfigs struct {
	SystemLogRotate *automationconfig.AcLogRotate
	AuditLogRotate  *automationconfig.AcLogRotate
}

func readProjectConfigs(conn om.Connection) (*ProjectAgentConfigs, *ProjectProcessConfigs, error) {
	monitoringConfig, err := conn.ReadMonitoringAgentConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("error reading monitoring agent config: %w", err)
	}
	backupConfig, err := conn.ReadBackupAgentConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("error reading backup agent config: %w", err)
	}
	systemLogRotate, err := conn.ReadProcessLogRotation()
	if err != nil {
		return nil, nil, fmt.Errorf("error reading system log rotate config: %w", err)
	}
	auditLogRotate, err := conn.ReadAuditLogRotation()
	if err != nil {
		return nil, nil, fmt.Errorf("error reading audit log rotate config: %w", err)
	}
	return &ProjectAgentConfigs{
			MonitoringConfig: monitoringConfig,
			BackupConfig:     backupConfig,
		}, &ProjectProcessConfigs{
			SystemLogRotate: systemLogRotate,
			AuditLogRotate:  auditLogRotate,
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
