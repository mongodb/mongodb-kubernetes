package operator

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ProjectReconciler reconciles a Project (ConfigMap)
type ProjectReconciler struct {
	*ReconcileCommonController
}

var _ reconcile.Reconciler = &ProjectReconciler{}

func newProjectReconciler(mgr manager.Manager, omFunc om.ConnectionFactory) *ProjectReconciler {
	return &ProjectReconciler{newReconcileCommonController(mgr, omFunc)}
}

func (r *ProjectReconciler) getProjectConfig(namespacedName types.NamespacedName) (*ProjectConfig, error) {
	config, err := r.kubeHelper.readProjectConfig(namespacedName.Namespace, namespacedName.Name)
	if err != nil {
		return nil, err
	}
	return config, nil
}

// tlsResult contains a group of fields which indicate
// the success of enabling TLS
// TODO: revist after CLOUDP-44175
type tlsResult struct {
	msg                  string
	isError, shouldRetry bool
}

// ensureTLS makes sure that it is possible to enable TLS at the project level
// if TLS cannot be enabled, it means that it will not be possible to enable x509 authentication.
func ensureTLS(conn om.Connection, log *zap.SugaredLogger) tlsResult {

	shouldRetry := false
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if !ac.AgentSSL.SSLEnabled() {
			ac.AgentSSL = &om.AgentSSL{
				ClientCertificateMode: util.OptionalClientCertficates,
				CAFilePath:            util.CAFilePathInContainer,
				AutoPEMKeyFilePath:    util.AutomationAgentPemFilePath,
			}
		}

		// if it's not possible to enable x509, we shouldn't attempt to as we would be
		// providing an invalid automation config.
		if canEnableX509, reason := ac.CanEnableX509ProjectAuthentication(); !canEnableX509 {
			shouldRetry = true
			return fmt.Errorf(reason)
		}
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if err != nil {
		return tlsResult{msg: err.Error(), shouldRetry: shouldRetry, isError: true}
	}

	return tlsResult{}
}

func (r *ProjectReconciler) Reconcile(request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("Project", request.NamespacedName)
	projectConfig, err := r.getProjectConfig(request.NamespacedName)
	if err != nil {
		log.Warnf("Error getting config map %s, was it removed?", err)
		return stop()
	}

	hasCredentials := projectConfig.Credentials != ""
	log.Infow("-> Project.Reconcile",
		"project", projectConfig.ProjectName,
		"authenticationMode", projectConfig.AuthMode,
		"baseUrl", projectConfig.BaseURL,
		"orgId", projectConfig.OrgID,
		"hasCredentials", hasCredentials)

	if !hasCredentials {
		log.Info("No project credentials - stopping now.")
		return stop()
	}

	connectionSpec := v1.ConnectionSpec{
		Project:     request.Name,
		Credentials: projectConfig.Credentials,
	}

	conn, err := r.prepareConnection(request.NamespacedName, connectionSpec, nil, log)

	if err != nil {
		log.Errorf("Error establishing Ops Manager connection. %s", err)
		return retry()
	}

	if !canEnableX509(conn) {
		// only log warning if the configuration is being changed
		if projectConfig.AuthMode == util.X509 {
			log.Warnf("X509 authentication not compatible with this version of Ops Manager! Please update to at least 4.0.11")
		}
		return stop()
	}

	if projectConfig.AuthMode == util.X509 {
		return r.enableX509Authentication(request, projectConfig, conn, log)
	} else {
		return r.disableX509Authentication(request, conn, log)
	}
}

func (r *ProjectReconciler) disableX509Authentication(request reconcile.Request, conn om.Connection, log *zap.SugaredLogger) (reconcile.Result, error) {
	// AutomationConfig update needs to come first otherwise MonitoringAgent and BackupAgent
	// updates are considered invalid

	shouldStop := false
	err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {

		if ac.Deployment.AnyProcessHasInternalClusterAuthentication() {
			shouldStop = true
			return fmt.Errorf("unable to disable x509 authentication as there as at least once process with internal cluster authentication enabled")
		}
		ac.DisableX509Authentication()
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if shouldStop {
		log.Info("Unable to disable x509 authentication as there as at least once process with internal cluster authentication enabled")
		return stop()
	}

	if err != nil {
		log.Errorf("Error disabling authentication in the automationConfig %s", err)
		return retry()
	}

	err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if err != nil {
		log.Errorf("Error disabling authentication in the monitoringAgentConfig %s", err)
		return retry()
	}

	err = conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if err != nil {
		log.Errorf("Error disabling authentication in the backupAgentConfig %s", err)
		return retry()
	}
	log.Info("successfully reconciled Project")
	return success()
}

func (r *ProjectReconciler) enableX509Authentication(request reconcile.Request, projectConfig *ProjectConfig, conn om.Connection, log *zap.SugaredLogger) (reconcile.Result, error) {

	successful, err := r.ensureX509AgentCertsForProject(projectConfig, request.Namespace)
	if err != nil {
		log.Errorf("Error ensuring x509 certificates for agents %s", err)
		return retry()
	} else if !successful {
		log.Info("Agent certs have not yet been approved")
		return retry()
	}

	result := ensureTLS(conn, log)
	if result.isError {
		log.Errorf("Error ensuring ssl is enabled: %s", result.msg)
		return retry()
	} else if result.shouldRetry {
		log.Infof("unable to enable x509: %s", result.msg)
		return retry()
	}

	err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.EnableX509Authentication()
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if err != nil {
		log.Errorf("Error updating monitoringAgentTemplate %s", err)
		return retry()
	}

	err = conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.EnableX509Authentication()
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if err != nil {
		log.Errorf("Error updating backupAgentTemplate %s", err)
		return retry()
	}

	err = conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		return ac.EnableX509Authentication()
	}, getMutex(conn.GroupName(), conn.OrgID()), log)

	if err != nil {
		log.Errorf("Error updating automationConfig %s", err)
		return retry()
	}
	log.Info("Successfully reconciled Project")
	return success()
}

//ensureX509AgentCertsForProject will generate all the CSRs for the agents
func (r *ProjectReconciler) ensureX509AgentCertsForProject(project *ProjectConfig, namespace string) (bool, error) {

	log := zap.S().With("Project", namespace)
	k := r.kubeHelper

	if project.AuthMode != util.X509 {
		return true, nil
	}

	certsNeedApproval := false

	if missing := k.verifyClientCertificatesForAgents(util.AgentSecretName, namespace); missing > 0 {
		if project.UseCustomCA {
			return false, fmt.Errorf("The %s Secret file does not contain the necessary Agent certificates. Missing %d certificates", util.AgentSecretName, missing)
		}

		pemFiles := newPemCollection()
		agents := []string{"automation", "monitoring", "backup"}

		for _, agent := range agents {
			agentName := fmt.Sprintf("mms-%s-agent", agent)
			csr, err := k.readCSR(agentName, namespace)
			if err != nil {
				certsNeedApproval = true

				log.Infof("Creating CSR: %s", agentName)
				// the agentName name will be the same on each host, but we want to ensure there's
				// a unique name for the CSR created.
				key, err := k.createAgentCSR(agentName, namespace)
				if err != nil {
					return false, fmt.Errorf("failed to create CSR, %s", err)
				}

				pemFiles.addPrivateKey(agentName, string(key))
			} else {
				if checkCSRWasApproved(csr.Status.Conditions) {
					log.Infof("Certificate for Agent %s -> Approved", agentName)
					pemFiles.addCertificate(agentName, string(csr.Status.Certificate))
				} else {
					log.Infof("Certificate for Agent %s -> Waiting for Approval", agentName)
					certsNeedApproval = true
				}
			}
		}

		// once we are here we know we have built everything we needed
		// This "secret" object corresponds to the certificates for this statefulset
		labels := make(map[string]string)
		labels["mongodb/secure"] = "certs"
		labels["mongodb/operator"] = "certs." + util.AgentSecretName

		err := k.createOrUpdateSecret(util.AgentSecretName, namespace, pemFiles, labels)
		if err != nil {
			// If we have an error creating or updating the secret, we might lose
			// the keys, in which case we return an error, to make it clear what
			// the error was to customers -- this should end up in the status
			// message.
			return false, fmt.Errorf("failed to create or update the secret: %s", err)
		}

	}

	successful := !certsNeedApproval
	return successful, nil
}

// AddProjectController creates a new ProjectController Controller and adds it to the Manager.
func AddProjectController(mgr manager.Manager) error {
	reconciler := newProjectReconciler(mgr, om.NewOpsManagerConnection)
	c, err := controller.New(util.MongoDbProjectController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForObject{}, predicatesForProject())
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbProjectController)

	return nil
}

// canEnableX509 determines if it's possible to enable/disable x509 configuration options in the current
// version of Ops Manager
func canEnableX509(conn om.Connection) bool {
	err := conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		return nil
	}, getMutex(conn.GroupName(), conn.OrgID()), nil)
	if err != nil && strings.Contains(err.Error(), "405 (Method Not Allowed)") {
		return false
	}
	return true
}
