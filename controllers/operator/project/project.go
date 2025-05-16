package project

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
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
func ReadConfigAndCredentials(ctx context.Context, cmGetter configmap.Getter, secretGetter secrets.SecretClient, reader Reader, log *zap.SugaredLogger) (mdbv1.ProjectConfig, mdbv1.Credentials, error) {
	projectConfig, err := ReadProjectConfig(ctx, cmGetter, kube.ObjectKey(reader.GetProjectConfigMapNamespace(), reader.GetProjectConfigMapName()), reader.GetName())
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, xerrors.Errorf("error reading project %w", err)
	}
	credsConfig, err := ReadCredentials(ctx, secretGetter, kube.ObjectKey(reader.GetCredentialsSecretNamespace(), reader.GetCredentialsSecretName()), log)
	if err != nil {
		return mdbv1.ProjectConfig{}, mdbv1.Credentials{}, xerrors.Errorf("error reading Credentials secret: %w", err)
	}
	return projectConfig, credsConfig, nil
}

/*
ReadOrCreateProject
Communication with groups is tricky.
In connection ConfigMap user must provide the project name and the id of organization.
The only way to find out if the project exists already is to check if its
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

		// The CA certificate passed to the OM client needs to be an actual certificate,
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
		// Note: this org_id = "" has to be explicitly set by the customer.
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
		return nil, xerrors.Errorf("organization with id %s not found: %w", orgID, err)
	}
	return organization, nil
}

// findProject tries to find if the group already exists.
func findProject(projectName string, organization *om.Organization, conn om.Connection, log *zap.SugaredLogger) (*om.Project, error) {
	project, err := findProjectInsideOrganization(conn, projectName, organization, log)
	if err != nil {
		return nil, xerrors.Errorf("error finding project %s in organization with id %s: %w", projectName, organization, err)
	}
	if project != nil {
		return project, nil
	}
	log.Debugf("Project \"%s\" not found in organization %s (\"%s\")", projectName, organization.ID, organization.Name)
	return nil, nil
}

// findProjectInsideOrganization looks up a project by name inside an organization and returns the project if it was the only one found by that name.
// If no project was found, the function returns a nil project to indicate that no such project exists.
// In all other cases, a non nil error is returned.
func findProjectInsideOrganization(conn om.Connection, projectName string, organization *om.Organization, log *zap.SugaredLogger) (*om.Project, error) {
	projects, err := conn.ReadProjectsInOrganizationByName(organization.ID, projectName)
	if err != nil {
		if v, ok := err.(*apierror.Error); ok {
			// If the project was not found, return an empty project and no error.
			if v.ErrorCode == apierror.ProjectNotFound {
				// ProjectNotFound is an expected condition.

				return nil, nil
			}
		}
		// Return an empty project and the OM api error in case there is a different API error.
		return nil, xerrors.Errorf("error looking up project %s in organization %s: %w", projectName, organization.ID, err)
	}

	// There is no API error. We check if the project found has the exact name.
	// The API endpoint returns a list of projects and in case of no exact match, it would return the first item that matches the search term as a prefix.
	var projectsFound []*om.Project
	for _, project := range projects {
		if project.Name == projectName {
			projectsFound = append(projectsFound, project)
		}
	}

	if len(projectsFound) == 1 {
		// If there is just one project returned, and it matches the name, return it.
		return projectsFound[0], nil
	} else if len(projectsFound) > 0 {
		projectsList := util.Transform(projectsFound, func(project *om.Project) string {
			return fmt.Sprintf("%s (%s)", project.Name, project.ID)
		})
		// This should not happen, but older versions of OM supported the same name for a project in an org. We cannot proceed here so we return an error.
		return nil, xerrors.Errorf("found more than one project with name %s in organization %s (%s): %v", projectName, organization.ID, organization.Name, strings.Join(projectsList, ", "))
	}

	// If there is no error from the API and no match in the response, return an empty project and no error.
	return nil, nil
}

func findOrganizationByName(conn om.Connection, name string, log *zap.SugaredLogger) (string, error) {
	// 1. We try to find the organization using 'name' filter parameter first
	organizations, err := conn.ReadOrganizationsByName(name)
	if err != nil {
		// This code is Ops Manager < 7 variant. Once 6 is EOL, it can be removed.
		if v, ok := err.(*apierror.Error); ok {
			if v.ErrorCode == apierror.OrganizationNotFound {
				// the "name" API is supported and the organization not found - returning nil
				return "", nil
			}
		}
		return "", xerrors.Errorf("could not find organization %s: %w", name, err)
	}
	// There's no error, so now we're checking organizations and matching names.
	// This code is needed as the Ops Manager selects organizations that "start by" the query. So potentially,
	// it can return manu results.
	// For example:
	//   When looking for name "development", it may return ["development", "development-oplog"].
	for _, organization := range organizations {
		if organization.Name == name {
			return organization.ID, nil
		}
	}
	// This is the fallback case - there is no org and in subsequent steps we'll create it.
	return "", nil
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
		return nil, xerrors.Errorf("Error creating project \"%s\" in Ops Manager: %w", group, err)
	}

	log.Infow("Project successfully created", "id", ans.ID)

	return ans, nil
}
