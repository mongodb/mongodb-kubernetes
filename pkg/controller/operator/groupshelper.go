package operator

import (
	"fmt"
	"strings"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

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
configMap are created in parallel - this function will be invoked sequantaly and the second caller will see the group
created on the first call
*/
func (c *ReconcileCommonController) readOrCreateGroup(projectName string, config *mdbv1.ProjectConfig, credentials *Credentials, log *zap.SugaredLogger) (*om.Project, error) {
	mutex := om.GetMutex(projectName, config.OrgID)
	mutex.Lock()
	defer mutex.Unlock()

	log = log.With("project", projectName)

	// we need to create a temporary connection object without group id
	omContext := om.OMContext{
		GroupID:      "",
		GroupName:    projectName,
		OrgID:        config.OrgID,
		BaseURL:      config.BaseURL,
		PublicAPIKey: credentials.PublicAPIKey,
		User:         credentials.User,

		AllowInvalidSSLCertificate: !config.SSLRequireValidMMSServerCertificates,
		CACertificate:              config.SSLMMSCAConfigMapContents,
	}
	conn := c.omConnectionFactory(&omContext)

	org, err := findOrganization(config.OrgID, projectName, conn, log)
	if err != nil {
		return nil, err
	}

	var project *om.Project
	if org != nil {
		project, err = findProject(projectName, org, conn, log)

		if err != nil {
			return nil, err
		}
	}

	if project == nil {
		project, err = tryCreateProject(org, projectName, config.OrgID, conn, log)
		if err != nil {
			return nil, err
		}
	}

	// ensure the project has necessary tag
	for _, t := range project.Tags {
		if t == util.OmGroupExternallyManagedTag {
			return project, nil
		}
	}

	// So the project doesn't have necessary tag - let's fix it (this is a temporary solution and we must throw the
	// exception by 1.0)
	// return nil, fmt.Errorf("Project \"%s\" doesn't have the tag %s", config.ProjectName, OmGroupExternallyManagedTag)
	log.Infow("Seems group doesn't have necessary tag " + util.OmGroupExternallyManagedTag + " - updating it")

	groupWithTags := &om.Project{
		Name:  project.Name,
		OrgID: project.OrgID,
		ID:    project.ID,
		Tags:  append(project.Tags, util.OmGroupExternallyManagedTag),
	}
	g, err := conn.UpdateProject(groupWithTags)
	if err != nil {
		log.Warnf("Failed to update tags for group: %s", err)
	} else {
		log.Infow("Project tags are fixed")
		project = g
	}

	return project, nil
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
	var project *om.Project
	// 1. Trying to find the project by name
	projects, err := conn.ReadProjectsInOrganizationByName(organization.ID, projectName)

	if err != nil && err.(*om.APIError).ErrorCode == om.ProjectNotFound {
		return nil, nil
	}
	if err == nil && len(projects) == 1 {
		// there is no error so we need to check if the project found has this name
		// (the project found could be just the page of one single project if the OM is old and "name"
		// parameter is not supported)
		if projects[0].Name == projectName {
			return projects[0], nil
		}
	} else if err != nil {
		log.Error(err)
	}
	// 2. At this stage we guess that the "name" filter parameter is not supported or the projects
	// slice was empty - let's failback to reading the pages (old version of OM?)
	log.Debugf("The Ops Manager used is too old (< 4.2.0) so we need to traverse all projects inside organization %s to find '%s'.", organization.ID, projectName)

	_, err = om.TraversePages(
		func(pageNum int) (paginated om.Paginated, e error) {
			return conn.ReadProjectsInOrganization(organization.ID, pageNum)
		},
		func(o interface{}) bool {
			g := o.(*om.Project)
			if g.Name == projectName {
				log.Debugf("Found the project %s in organization %s (\"%s\")", g.ID, organization.ID, organization.Name)
				project = g
				return true
			}
			return false
		})
	return project, err
}

func findOrganizationByName(conn om.Connection, name string, log *zap.SugaredLogger) (string, error) {
	var orgID string
	// 1. We try to find the ogranization using 'name' filter parameter first
	organizations, err := conn.ReadOrganizationsByName(name)

	if err != nil && err.(*om.APIError).ErrorCode == om.OrganizationNotFound {
		// the "name" API is supported and the organization not found - returning nil
		return "", nil
	}
	if err == nil && len(organizations) == 1 {
		// there is no error so we need to check if the organization found has this name
		// (the organization found could be just the page of one single organization if the OM is old and "name"
		// parameter is not supported)
		if organizations[0].Name == name {
			return organizations[0].ID, nil
		}
	} else if err != nil {
		log.Error(err)
	}

	// 2. At this stage we guess that the "name" filter parameter is not supported or the organizations slice
	// was empty - let's failback to reading the pages (old version of OM?)
	log.Debugf("The Ops Manager used is too old (< 4.2.0) so we need to traverse all organizations to find '%s'.", name)

	_, err = om.TraversePages(
		conn.ReadOrganizations,
		func(o interface{}) bool {
			org := o.(*om.Organization)
			if org.Name == name {
				orgID = org.ID
				log.Debugf("Found organization \"%s\" (%s)", org.Name, org.ID)
				return true
			}
			return false
		})
	return orgID, err
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
		Tags:  []string{util.OmGroupExternallyManagedTag},
	}
	ans, err := conn.CreateProject(group)

	if err != nil {
		apiError := err.(*om.APIError)
		if apiError.ErrorCodeIn(om.InvalidAttribute) && strings.Contains(apiError.Detail, "tags") {
			// Fallback logic: seems that OM version is < 4.0.2 (as it allows to edit group
			// tags only for GLOBAL_OWNER users), let's try to create group without tags
			group.Tags = []string{}
			ans, err = conn.CreateProject(group)

			if err != nil {
				return nil, fmt.Errorf("Error creating project \"%s\" in Ops Manager: %s", group, err)
			}
			log.Infow("Created project without tags as current version of Ops Manager forbids tags modification")
		} else {
			return nil, fmt.Errorf("Error creating project \"%s\" in Ops Manager: %s", group, err)
		}
	}
	log.Infow("Project successfully created", "id", ans.ID)

	return ans, nil
}
