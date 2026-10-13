package connection

import (
	"context"
	"fmt"
	"strings"

	"github.com/r3labs/diff/v3"
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesClient "sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/agents"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// headlessOMContext builds the OM context for a MongoDB resource that is not managed by Ops
// Manager. The automation config is stored in the <name>-config Secret and the agents run in
// headless mode.
func headlessOMContext(kubeClient kubernetesClient.Client, mdb *mdbv1.MongoDB, agentVersion string) *om.OMContext {
	return &om.OMContext{
		GroupID:   mdb.Name,
		GroupName: mdb.Name,
		OrgID:     mdb.Namespace,
		Headless: &om.HeadlessContext{
			Client:          kubeClient,
			Namespace:       mdb.Namespace,
			Name:            mdb.Name,
			SecretName:      mdb.Name + om.HeadlessAutomationConfigSecretSuffix,
			AgentVersion:    agentVersion,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(mdb, v1.SchemeGroupVersion.WithKind("MongoDB"))},
		},
	}
}

// PrepareHeadlessConnection prepares a connection for a MongoDB resource that is not managed by
// an Ops Manager instance. The automation config is stored in a Secret and the agents run in
// headless mode; no project or credentials need to be configured on the resource.
func PrepareHeadlessConnection(ctx context.Context, kubeClient kubernetesClient.Client, secretClient secrets.SecretClient, connectionFunc om.ConnectionFactory, mdb *mdbv1.MongoDB, agentVersion string, log *zap.SugaredLogger) (om.Connection, string, error) {
	conn := connectionFunc(headlessOMContext(kubeClient, mdb, agentVersion))

	var databaseSecretPath string
	if secretClient.VaultClient != nil {
		databaseSecretPath = secretClient.VaultClient.DatabaseSecretPath()
	}

	if agentAPIKey, err := agents.EnsureAgentKeySecretExists(ctx, secretClient, conn, mdb.Namespace, "", conn.GroupID(), databaseSecretPath, log); err != nil {
		return nil, "", err
	} else {
		log.Infof("Prepared headless connection for %s/%s (automation config secret %s)", mdb.Namespace, mdb.Name, mdb.Name+om.HeadlessAutomationConfigSecretSuffix)
		return conn, agentAPIKey, nil
	}
}

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

// sanitizeHeadlessDeploymentForOpsManager removes the fields that exist in the headless automation
// config only to satisfy the automation agent's config schema, before that config is pushed to Ops
// Manager. mongoDbVersions is a synthetic version manifest built from the process versions whose
// builds carry empty urls; Ops Manager computes its own manifest and rejects the headless one with
// "The required attribute mongoDbVersions.builds.url was not specified". The headless AC in the
// <name>-config Secret is left untouched: it is still consumed by the agents that run headless
// while the rolling restart is in flight.
func sanitizeHeadlessDeploymentForOpsManager(deployment om.Deployment) {
	delete(deployment, "mongoDbVersions")
}

// SeedOpsManagerAutomationConfigFromHeadless copies the automation config stored in the headless
// <name>-config Secret into the target Ops Manager project before any StatefulSet is mutated to
// point at the new project. The target version is preserved and the seed step is idempotent: if
// the target project already has processes, nothing is written.
// It returns the version the Secret had at seed time: the baseline the still-headless agents are
// evaluated against while the rolling restart is in flight.
func SeedOpsManagerAutomationConfigFromHeadless(targetConn om.Connection, kubeClient kubernetesClient.Client, mdb *mdbv1.MongoDB, log *zap.SugaredLogger) (int64, error) {
	headlessConn := om.NewHeadlessConnection(headlessOMContext(kubeClient, mdb, ""))

	sourceAC, err := headlessConn.ReadAutomationConfig()
	if err != nil {
		return 0, xerrors.Errorf("failed to read headless automation config: %w", err)
	}
	baselineVersion := sourceAC.Deployment.Version()

	targetAC, err := targetConn.ReadAutomationConfig()
	if err != nil {
		return 0, xerrors.Errorf("failed to read target project automation config: %w", err)
	}
	if targetAC.Deployment.NumberOfProcesses() > 0 {
		log.Infof("Target project %s already has an automation config; skipping the headless seed step", targetConn.GroupID())
		return baselineVersion, nil
	}

	if sourceAC.Deployment.NumberOfProcesses() == 0 {
		return 0, xerrors.Errorf("headless automation config secret for %s/%s has no processes; refusing to hand the resource over to Ops Manager", mdb.Namespace, mdb.Name)
	}

	sanitizeHeadlessDeploymentForOpsManager(sourceAC.Deployment)
	expected := sourceAC.Deployment.ToCanonicalForm()
	if err := targetConn.UpdateAutomationConfig(sourceAC, log); err != nil {
		return 0, xerrors.Errorf("failed to seed target project automation config from the headless automation config: %w", err)
	}

	log.Infof("Seeded target project %s with the headless automation config (baseline version %d)", targetConn.GroupID(), baselineVersion)
	logAutomationConfigParity(targetConn, expected, log)
	return baselineVersion, nil
}

// PrepareHeadlessMigrationConnection returns the connection to use for the remainder of a
// reconcile while a headless resource is being handed over to Ops Manager: automation config
// reads and writes go to the target Ops Manager project, while pods that still run headless are
// evaluated against the <name>-config Secret (baselineVersion was captured by
// SeedOpsManagerAutomationConfigFromHeadless).
func PrepareHeadlessMigrationConnection(targetConn om.Connection, kubeClient kubernetesClient.Client, mdb *mdbv1.MongoDB, baselineVersion int64) om.Connection {
	headlessConn := om.NewHeadlessConnection(headlessOMContext(kubeClient, mdb, ""))
	return om.NewHeadlessMigrationConnection(targetConn, headlessConn, baselineVersion)
}

// logAutomationConfigParity logs (never fails) the differences between the automation config that
// was pushed from the headless Secret and the one Ops Manager reports back. Expected divergences
// are an OM-side version renumbering and fields OM adds on its own (options, monitoringVersions).
func logAutomationConfigParity(conn om.Connection, expected om.Deployment, log *zap.SugaredLogger) {
	actual, err := conn.ReadAutomationConfig()
	if err != nil {
		log.Warnf("Automation config parity check skipped: failed to read the automation config back from Ops Manager: %s", err)
		return
	}

	actualDeployment := actual.Deployment.ToCanonicalForm()
	changes, err := diff.Diff(expected.ToCanonicalForm(), actualDeployment, diff.AllowTypeMismatch(true))
	if err != nil {
		log.Warnf("Automation config parity check skipped: could not compute the diff: %s", err)
		return
	}

	if len(changes) == 0 {
		log.Info("Automation config parity check: Ops Manager reports the seeded automation config unchanged")
		return
	}

	const maxLoggedChanges = 20
	loggedChanges := changes
	if len(loggedChanges) > maxLoggedChanges {
		loggedChanges = loggedChanges[:maxLoggedChanges]
	}
	log.Warnf("Automation config parity check: Ops Manager reports %d difference(s) from the seeded automation config (Ops Manager may renumber the version or add fields such as options/monitoringVersions): %+v", len(changes), loggedChanges)
}
