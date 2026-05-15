package connection

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
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

// PrepareOpsManagerConnectionReadOnly is the follower-side equivalent of
// PrepareOpsManagerConnection. F12c uses it from non-leader distributed-mode
// reconciles: it reads (never creates) the OM project, skips the
// tag-mutation / agent-key write paths, and returns the connection ready for
// AC reads. If the project does not exist yet, it returns project.ErrProjectNotFound
// so the caller can surface workflow.Pending and wait for the leader to
// create it.
func PrepareOpsManagerConnectionReadOnly(ctx context.Context, client secrets.SecretClient, projectConfig mdbv1.ProjectConfig, credentials mdbv1.Credentials, connectionFunc om.ConnectionFactory, namespace string, log *zap.SugaredLogger) (om.Connection, string, error) {
	_ = ctx
	_ = client
	_ = namespace
	omProject, conn, err := project.ReadProject(projectConfig, credentials, connectionFunc, log)
	if err != nil {
		if xerrors.Is(err, project.ErrProjectNotFound) {
			return nil, "", err
		}
		return nil, "", xerrors.Errorf("error reading project in Ops Manager: %w", err)
	}

	omVersion := conn.OpsManagerVersion()
	if omVersion.VersionString != "" {
		log.Infof("Using Ops Manager version %s", omVersion)
	}

	// Followers must NOT call EnsureTagAdded (it writes to OM) and must NOT
	// call EnsureAgentKeySecretExists (it requests a new agent API key when
	// none exists, which is also a write). We return the existing project's
	// agent API key from the read.
	agentAPIKey := omProject.AgentAPIKey
	return conn, agentAPIKey, nil
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
