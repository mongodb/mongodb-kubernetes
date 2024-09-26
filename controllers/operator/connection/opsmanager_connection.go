package connection

import (
	"context"
	"fmt"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"

	"golang.org/x/xerrors"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/secrets"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"go.uber.org/zap"
)

func PrepareOpsManagerConnection(ctx context.Context, client secrets.SecretClient, projectConfig mdbv1.ProjectConfig, credentials mdbv1.Credentials, connectionFunc om.ConnectionFactory, namespace string, log *zap.SugaredLogger) (om.Connection, string, error) {
	omProject, conn, err := project.ReadOrCreateProject(projectConfig, credentials, connectionFunc, log)
	if err != nil {
		return nil, "", xerrors.Errorf("error reading or creating project in Ops Manager: %w", err)
	}

	omVersion := conn.OpsManagerVersion()
	if omVersion.VersionString != "" { // older versions of Ops Manager will not include the version in the header
		log.Infof("Using Ops Manager version %s", omVersion)
	}

	// adds the namespace as a tag to the Ops Manager project
	if err = EnsureTagAdded(conn, omProject, namespace, log); err != nil {
		return nil, "", err
	}

	// adds the externally_managed tag if feature controls is not available.
	if !controlledfeature.ShouldUseFeatureControls(conn.OpsManagerVersion()) {
		if err = EnsureTagAdded(conn, omProject, util.OmGroupExternallyManagedTag, log); err != nil {
			return nil, "", err
		}
	}

	var databaseSecretPath string
	if client.VaultClient != nil {
		databaseSecretPath = client.VaultClient.DatabaseSecretPath()
	}
	if agentAPIKey, err := agents.EnsureAgentKeySecretExists(ctx, client, conn, namespace, omProject.AgentAPIKey, conn.GroupID(), databaseSecretPath, log); err != nil {
		return nil, "", err
	} else {
		return conn, agentAPIKey, err
	}
}

// EnsureTagAdded makes sure that the given project has the provided tag
func EnsureTagAdded(conn om.Connection, project *om.Project, tag string, log *zap.SugaredLogger) error {
	// must truncate the tag to at most 32 characters and capitalise as
	// these are Ops Manager requirements

	sanitisedTag := strings.ToUpper(fmt.Sprintf("%.32s", tag))
	alreadyHasTag := stringutil.Contains(project.Tags, sanitisedTag)
	if alreadyHasTag {
		return nil
	}

	project.Tags = append(project.Tags, sanitisedTag)

	log.Infow("Updating group tags", "newTags", project.Tags)
	_, err := conn.UpdateProject(project)
	if err != nil {
		log.Warnf("Failed to update tags for project: %s", err)
	} else {
		log.Info("Project tags are fixed")
	}
	return err
}
