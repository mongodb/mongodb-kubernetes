package operator

import (
	"fmt"
	"strings"

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
func (c *ReconcileCommonController) readOrCreateGroup(config *ProjectConfig, credentials *Credentials, log *zap.SugaredLogger) (*om.Project, error) {
	mutex := getMutex(config.ProjectName, config.OrgID)
	mutex.Lock()
	defer mutex.Unlock()

	log = log.With("project", config.ProjectName)

	// we need to create a temporary connection object without group id
	omContext := om.OMContext{
		GroupID:      "",
		GroupName:    config.ProjectName,
		OrgID:        config.OrgID,
		BaseURL:      config.BaseURL,
		PublicAPIKey: credentials.PublicAPIKey,
		User:         credentials.User,

		AllowInvalidSSLCertificate: !config.SSLRequireValidMMSServerCertificates,
		CACertificate:              config.SSLMMSCAConfigMapContents,
	}
	conn := c.omConnectionFactory(&omContext)

	group, org, err := findExistingGroup(config, conn, log)

	if err != nil {
		return nil, err
	}

	if group == nil {
		group, err = tryCreateProject(org, config, conn, log)
		if err != nil {
			return nil, err
		}
	}

	// ensure the group has necessary tag
	for _, t := range group.Tags {
		if t == util.OmGroupExternallyManagedTag {
			return group, nil
		}
	}

	// So the group doesn't have necessary tag - let's fix it (this is a temporary solution and we must throw the
	// exception by 1.0)
	// return nil, fmt.Errorf("Project \"%s\" doesn't have the tag %s", config.ProjectName, OmGroupExternallyManagedTag)
	log.Infow("Seems group doesn't have necessary tag " + util.OmGroupExternallyManagedTag + " - updating it")

	groupWithTags := &om.Project{
		Name:  group.Name,
		OrgID: group.OrgID,
		ID:    group.ID,
		Tags:  append(group.Tags, util.OmGroupExternallyManagedTag),
	}
	g, err := conn.UpdateProject(groupWithTags)
	if err != nil {
		log.Warnf("Failed to update tags for group: %s", err)
	} else {
		log.Infow("Project tags are fixed")
		group = g
	}

	return group, nil
}

// findExistingGroup tries to find if the group already exists. The logic is to read all projects in the organization
// and find the one with the required name. If the 'orgId' is not specified - then we need to find the organization by
// name first
func findExistingGroup(config *ProjectConfig, conn om.Connection, log *zap.SugaredLogger) (*om.Project, *om.Organization, error) {
	orgId := config.OrgID
	if config.OrgID == "" {
		// If org id is not specified - then the contract is that the organization for the project must have the same
		// name as project has (as it was created automatically for the project), so we need to find relevant organization
		log.Debugf("Organization id is not specified - trying to find the organization with name \"%s\"", config.ProjectName)
		_, err := om.TraversePages(conn.ReadOrganizations, func(o interface{}) bool {
			org := o.(*om.Organization)
			if org.Name == config.ProjectName {
				orgId = org.ID
				log.Debugf("Found organization \"%s\" (%s)", org.Name, org.ID)
				return true
			}
			return false
		})
		if err != nil {
			return nil, nil, err
		}
		if orgId == "" {
			log.Debugf("Organization \"%s\" not found", config.ProjectName)
			return nil, nil, nil
		}
	}

	organization, err := conn.ReadOrganization(orgId)
	if err != nil {
		return nil, nil, fmt.Errorf("Organization with id %s not found: %s", config.OrgID, err)
	}
	var group *om.Project
	_, err = om.TraversePages(
		func(pageNum int) (paginated om.Paginated, e error) {
			return conn.ReadProjectsInOrganization(orgId, pageNum)
		},
		func(o interface{}) bool {
			g := o.(*om.Project)
			if g.Name == config.ProjectName {
				log.Debugf("Found the project %s in organization %s (\"%s\")", g.ID, organization.ID, organization.Name)
				group = g
				return true
			}
			return false
		})
	if err != nil {
		return nil, nil, fmt.Errorf("Error reading projects in organization with id %s: %s", config.OrgID, err)
	}
	if group != nil {
		return group, organization, nil
	}
	log.Debugf("Project \"%s\" not found in organization %s (\"%s\")", config.ProjectName, organization.ID, organization.Name)
	return nil, organization, nil
}

func tryCreateProject(organization *om.Organization, config *ProjectConfig, conn om.Connection, log *zap.SugaredLogger) (*om.Project, error) {
	orgId := config.OrgID
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
			"automatically by Ops Manager", config.ProjectName)
	}
	group := &om.Project{
		Name:  config.ProjectName,
		OrgID: orgId,
		Tags:  []string{util.OmGroupExternallyManagedTag},
	}
	ans, err := conn.CreateProject(group)

	if err != nil {
		apiError := err.(*om.APIError)
		if apiError.ErrorCodeIn("INVALID_ATTRIBUTE") && strings.Contains(apiError.Detail, "tags") {
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
