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
*/
func (c *ReconcileCommonController) readOrCreateGroup(config *ProjectConfig, credentials *Credentials, log *zap.SugaredLogger) (*om.Group, error) {
	log = log.With("project", config.ProjectName)

	// we need to create a temporary connection object without group id
	conn := c.omConnectionFunc(config.BaseURL, "", credentials.User, credentials.PublicAPIKey)
	groups, err := conn.ReadGroups()

	if err != nil {
		return nil, fmt.Errorf("Error reading all groups for user \"%s\" in Ops Manager: %s", credentials.User, err)
	}

	group, err := findExistingGroup(groups, config, conn)

	if err != nil {
		return nil, err
	}

	if group == nil {
		group, err = tryCreateGroup(config, conn, log)
		if err != nil {
			return nil, err
		}
	} else {
		log.Debug("Group already exists")
	}

	// ensure the group has necessary tag
	for _, t := range group.Tags {
		if t == util.OmGroupExternallyManagedTag {
			return group, nil
		}
	}

	// So the group doesn't have necessary tag - let's fix it (this is a temporary solution and we must throw the
	// exception by 1.0)
	// return nil, fmt.Errorf("Group \"%s\" doesn't have the tag %s", config.ProjectName, OmGroupExternallyManagedTag)
	log.Infow("Seems group doesn't have necessary tag " + util.OmGroupExternallyManagedTag + " - updating it")

	groupWithTags := &om.Group{
		Name:  group.Name,
		OrgID: group.OrgID,
		ID:    group.ID,
		Tags:  append(group.Tags, util.OmGroupExternallyManagedTag),
	}
	g, err := conn.UpdateGroup(groupWithTags)
	if err != nil {
		log.Warnf("Failed to update tags for group: %s", err)
	} else {
		log.Infow("Group tags are fixed")
		group = g
	}

	return group, nil
}

func findExistingGroup(groups []*om.Group, config *ProjectConfig, conn om.Connection) (*om.Group, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	if config.OrgID != "" {
		for _, g := range groups {
			if g.OrgID == config.OrgID && g.Name == config.ProjectName {
				return g, nil
			}
		}
		return nil, fmt.Errorf("Failed to find group \"%s\" inside organization %s", config.ProjectName, config.OrgID)
	}
	// If org id is not specified - then the contract is that the organization for the project must have the same
	// name as project has (as it was created automatically for the project)
	organizations, err := conn.ReadOrganizations()
	if err != nil {
		return nil, err
	}

	// There can be situations that the group with the name "config.ProjectName" exists but belongs to other
	// organization - we skip it
	for _, org := range organizations {
		for _, group := range groups {
			if group.Name == config.ProjectName && group.OrgID == org.ID && org.Name == config.ProjectName {
				return group, nil
			}
		}
	}

	return nil, nil
}

func tryCreateGroup(config *ProjectConfig, conn om.Connection, log *zap.SugaredLogger) (*om.Group, error) {
	// Creating the group as it doesn't exist
	log.Infow("Creating the project as it doesn't exist", "orgId", config.OrgID)
	if config.OrgID == "" {
		log.Infof("Note that as the orgId is not specified the organization with name \"%s\" will be created "+
			"automatically by Ops Manager", config.ProjectName)
	}
	group := &om.Group{
		Name:  config.ProjectName,
		OrgID: config.OrgID,
		Tags:  []string{util.OmGroupExternallyManagedTag},
	}
	ans, err := conn.CreateGroup(group)

	if err != nil {
		apiError := err.(*om.APIError)
		if apiError.ErrorCodeIn("INVALID_ATTRIBUTE") && strings.Contains(apiError.Detail, "tags") {
			// Fallback logic: seems that OM version is < 4.0.2 (as it allows to edit group
			// tags only for GLOBAL_OWNER users), let's try to create group without tags
			group.Tags = []string{}
			ans, err = conn.CreateGroup(group)

			if err != nil {
				return nil, fmt.Errorf("Error creating group \"%s\" in Ops Manager: %s", group, err)
			}
			log.Infow("Created group without tags as current version of Ops Manager forbids tags modification")
		} else {
			return nil, fmt.Errorf("Error creating group \"%s\" in Ops Manager: %s", group, err)
		}
	}
	log.Infow("Project successfully created", "id", ans.ID)

	return ans, nil
}
