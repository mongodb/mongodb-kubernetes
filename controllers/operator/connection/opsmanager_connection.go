package connection

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// PrepareOpsManagerConnection sets up a connection to Ops Manager for the given project and credentials.
// tagNamespace controls whether the calling controller's namespace is added as a tag on the OM project.
// Pass false for controllers where multiple namespaces share one project (e.g. MongoDBUser), because
// Ops Manager enforces a hard limit of 10 tags per project and each namespace would otherwise consume one slot.
func PrepareOpsManagerConnection(ctx context.Context, client secrets.SecretClient, projectConfig mdbv1.ProjectConfig, credentials mdbv1.Credentials, connectionFunc om.ConnectionFactory, namespace string, tagNamespace bool, log *zap.SugaredLogger) (om.Connection, string, error) {
	omProject, conn, err := project.ReadOrCreateProject(projectConfig, credentials, connectionFunc, log)
	if err != nil {
		return nil, "", xerrors.Errorf("error reading or creating project in Ops Manager: %w", err)
	}

	omVersion := conn.OpsManagerVersion()
	if omVersion.VersionString != "" { // older versions of Ops Manager will not include the version in the header
		log.Infof("Using Ops Manager version %s", omVersion)
	}

	// tagNamespace is false for controllers where many namespaces share one project (e.g. MongoDBUser)
	// to avoid hitting the OM hard limit of 10 tags per project.
	if tagNamespace {
		if err = EnsureTagAdded(conn, omProject, namespace, log); err != nil {
			return nil, "", err
		}
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
	if stringutil.Contains(project.Tags, sanitisedTag) {
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

// EnsureTargetAutomationConfigSeeded copies the full Automation Config from the
// source project (sourceProjectID) into the target project (targetConn) before
// any StatefulSet is mutated to point at the new project. The target AC version
// is preserved. If sourceProjectID is empty or already equals the target group,
// or the target already has processes, it is a no-op.
// With this we Pre-seed the target project's Automation Config from the prior project
// before any pod mutation, so agents switching to the new GROUP_ID find the
// same topology and auth.key already in place.
func EnsureTargetAutomationConfigSeeded(targetConn om.Connection, sourceProjectID string, projectConfig mdbv1.ProjectConfig, credentials mdbv1.Credentials, connectionFunc om.ConnectionFactory, log *zap.SugaredLogger) error {
	if sourceProjectID == "" || sourceProjectID == targetConn.GroupID() {
		return nil
	}

	targetAC, err := targetConn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("failed to read target project automation config: %w", err)
	}
	if targetAC.Deployment.NumberOfProcesses() > 0 {
		return nil
	}

	sourceConn := connectionFunc(&om.OMContext{
		GroupID:                    sourceProjectID,
		GroupName:                  projectConfig.ProjectName,
		OrgID:                      projectConfig.OrgID,
		BaseURL:                    projectConfig.BaseURL,
		PublicKey:                  credentials.PublicAPIKey,
		PrivateKey:                 credentials.PrivateAPIKey,
		AllowInvalidSSLCertificate: !projectConfig.SSLRequireValidMMSServerCertificates,
		CACertificate:              projectConfig.SSLMMSCAConfigMapContents,
	})

	sourceAC, err := sourceConn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("failed to read source project automation config: %w", err)
	}

	sourceAC.Deployment["version"] = targetAC.Deployment.Version()
	if err := targetConn.UpdateAutomationConfig(sourceAC, log); err != nil {
		return xerrors.Errorf("failed to seed target project automation config: %w", err)
	}

	log.Infof("Seeded target project %s automation config from source project %s", targetConn.GroupID(), sourceProjectID)
	return nil
}
