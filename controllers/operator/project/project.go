package project

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/vault"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"go.uber.org/zap"
)

// Reader returns the name of a ConfigMap which contains Ops Manager project details.
// and the name of a secret containing project credentials.
type Reader interface {
	metav1.Object
	GetProjectConfigMapName() string
	GetProjectConfigMapNamespace() string
	GetCredentialsSecretName() string
	GetCredentialsSecretNamespace() string
}

// ReadConfigAndCredentials returns the ProjectConfig and Credentials for a given resource which are
// used to communicate with Ops Manager.
func ReadConfigAndCredentials(client kubernetesClient.Client, reader Reader, vaultClient *vault.VaultClient, log *zap.SugaredLogger) (mdbv1.ProjectConfig, mdbv1.Credentials, error) {
	projectConfig, err := ReadProjectConfig(client, kube.ObjectKey(reader.GetProjectConfigMapNamespace(), reader.GetProjectConfigMapName()), reader.GetName())
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, fmt.Errorf("error reading project %s", err)
	}
	credsConfig, err := ReadCredentials(client, kube.ObjectKey(reader.GetCredentialsSecretNamespace(), reader.GetCredentialsSecretName()), vaultClient, log)
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, fmt.Errorf("error reading Credentials secret: %s", err)
	}
	return projectConfig, credsConfig, nil
}

/*
Communication with groups is tricky.
In connection ConfigMap user must provide the project name and optionally the id of organization.
If org id is omitted this means that controller will create the project if it doesn't exist and organization will be
created with the same name as project. The only way to find out if the project exists already is to check if its
organization name is the same as the projects one and that's what Operator is doing. So if ConfigMap specifies the
project with name "A" and no org id and there is already project in Ops manager named "A" in organization with name"B"
then Operator won't find it as the names don't match.

Note, that the method is performed holding the "groupName+orgId" mutex which allows to avoid race conditions and avoid
duplicated groups/organizations creation. So if for example the standalone and the replica set which reference the same
configMap are created in parallel - this function will be invoked sequentially and the second caller will see the group
created on the first call
*/
func ReadOrCreateProject(config mdbv1.ProjectConfig, credentials mdbv1.Credentials, connectionFactory om.ConnectionFactory, log *zap.SugaredLogger) (*om.Project, om.Connection, error) {
	projectName := config.ProjectName
	mutex := om.GetMutex(projectName, config.OrgID)
	mutex.Lock()
	defer mutex.Unlock()

	log = log.With("project", projectName)

	// we need to create a temporary connection object without group id
	omContext := om.OMContext{
		GroupID:    "",
		GroupName:  projectName,
		OrgID:      config.OrgID,
		BaseURL:    config.BaseURL,
		PublicKey:  credentials.PublicAPIKey,
		PrivateKey: credentials.PrivateAPIKey,

		// The OM Client expects the inverse of "Require valid cert" because in Go
		// The "zero" value of bool is "False", hence this default.
		AllowInvalidSSLCertificate: !config.SSLRequireValidMMSServerCertificates,

		// The CA certificate passed to the OM client needs to be a actual certificate,
		// and not a location in disk, because each "project" will have its own CA cert.
		CACertificate: config.SSLMMSCAConfigMapContents,
	}

	conn := connectionFactory(&omContext)

	org, err := findOrganization(config.OrgID, projectName, conn, log)
	if err != nil {
		return nil, nil, err
	}

	var project *om.Project
	if org != nil {
		project, err = findProject(projectName, org, conn, log)

		if err != nil {
			return nil, nil, err
		}
	}

	if project == nil {
		project, err = tryCreateProject(org, projectName, config.OrgID, conn, log)
		if err != nil {
			return nil, nil, err
		}
	}

	conn.ConfigureProject(project)

	return project, conn, nil
}

func findOrganization(orgID string, projectName string, conn om.Connection, log *zap.SugaredLogger) (*om.Organization, error) {
	if orgID == "" {
		// If org id is not specified - then the contract is that the organization for the project must have the same
		// name as project has (as it was created automatically for the project), so we need to find relevant organization
		log.Debugf("Organization id is not specified - trying to find the organization with name \"%s\"", projectName)
		var err error
		if orgID, err = findOrganizationByName(conn, projectName, log); err != nil {
			return nil, err
		}
		if orgID == "" {
			log.Debugf("Organization \"%s\" not found", projectName)
			return nil, nil
		}
	}

	organization, err := conn.ReadOrganization(orgID)
	if err != nil {
		return nil, fmt.Errorf("organization with id %s not found: %s", orgID, err)
	}
	return organization, nil
}

// findProject tries to find if the group already exists.
func findProject(projectName string, organization *om.Organization, conn om.Connection, log *zap.SugaredLogger) (*om.Project, error) {
	project, err := findProjectInsideOrganization(conn, projectName, organization, log)
	if err != nil {
		return nil, fmt.Errorf("error finding project %s in organization with id %s: %s", projectName, organization, err)
	}
	if project != nil {
		return project, nil
	}
	log.Debugf("Project \"%s\" not found in organization %s (\"%s\")", projectName, organization.ID, organization.Name)
	return nil, nil
}

func findProjectInsideOrganization(conn om.Connection, projectName string, organization *om.Organization, log *zap.SugaredLogger) (*om.Project, error) {
	// 1. Trying to find the project by name
	projects, err := conn.ReadProjectsInOrganizationByName(organization.ID, projectName)

	if err != nil {
		if v, ok := err.(*apierror.Error); ok {
			if v.ErrorCode == apierror.ProjectNotFound {
				// ProjectNotFound is an expected condition.
				return nil, nil
			}
		}
		log.Error(err)
	}

	if err == nil && len(projects) == 1 {
		// there is no error so we need to check if the project found has this name
		// (the project found could be just the page of one single project if the OM is old and "name"
		// parameter is not supported)
		if projects[0].Name == projectName {
			return projects[0], nil
		}
	}

	return nil, fmt.Errorf("could not find project %s in organization %s", projectName, organization.ID)
}

func findOrganizationByName(conn om.Connection, name string, log *zap.SugaredLogger) (string, error) {
	// 1. We try to find the ogranization using 'name' filter parameter first
	organizations, err := conn.ReadOrganizationsByName(name)

	if err != nil {
		if v, ok := err.(*apierror.Error); ok {
			if v.ErrorCode == apierror.OrganizationNotFound {
				// the "name" API is supported and the organization not found - returning nil
				return "", nil
			}
		}

		log.Error(err)
	}
	if err == nil && len(organizations) == 1 {
		// there is no error so we need to check if the organization found has this name
		// (the organization found could be just the page of one single organization if the OM is old and "name"
		// parameter is not supported)
		if organizations[0].Name == name {
			return organizations[0].ID, nil
		}
	}

	return "", fmt.Errorf("could not find organization %s: %s", name, err)
}

func tryCreateProject(organization *om.Organization, projectName, orgId string, conn om.Connection, log *zap.SugaredLogger) (*om.Project, error) {
	// We can face the following scenario: for the project "foo" with 'orgId=""' the organization "foo" already exists
	// - so we need to reuse its orgId instead of creating the new Organization with the same name (OM API is quite
	// poor here - it may create duplicates)
	if organization != nil {
		orgId = organization.ID
	}
	// Creating the group as it doesn't exist
	log.Infow("Creating the project as it doesn't exist", "orgId", orgId)
	if orgId == "" {
		log.Infof("Note that as the orgId is not specified the organization with name \"%s\" will be created "+
			"automatically by Ops Manager", projectName)
	}
	group := &om.Project{
		Name:  projectName,
		OrgID: orgId,
		Tags:  []string{}, // Project creation no longer applies the EXTERNALLY_MANAGED tag, this is added afterwards
	}
	ans, err := conn.CreateProject(group)

	if err != nil {
		return nil, fmt.Errorf("Error creating project \"%s\" in Ops Manager: %s", group, err)
	}

	log.Infow("Project successfully created", "id", ans.ID)

	return ans, nil
}
