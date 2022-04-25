package authentication

import (
	"regexp"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

const ExternalDB = "$external"

func NewConnectionX509(conn om.Connection, ac *om.AutomationConfig, opts Options) ConnectionX509 {
	return ConnectionX509{
		AutomationConfig: ac,
		Conn:             conn,
		Options:          opts,
	}
}

type ConnectionX509 struct {
	AutomationConfig *om.AutomationConfig
	Conn             om.Connection
	Options          Options
}

func (x ConnectionX509) EnableAgentAuthentication(opts Options, log *zap.SugaredLogger) error {
	log.Info("Configuring x509 authentication")
	err := x.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		if err := ac.EnsureKeyFileContents(); err != nil {
			return err
		}
		auth := ac.Auth
		auth.AutoPwd = util.MergoDelete
		auth.Disabled = false
		auth.AuthoritativeSet = opts.AuthoritativeSet
		auth.KeyFile = util.AutomationAgentKeyFilePathInContainer
		auth.KeyFileWindows = util.AutomationAgentWindowsKeyFilePath
		ac.AgentSSL = &om.AgentSSL{
			AutoPEMKeyFilePath:    util.AutomationAgentPemFilePath,
			CAFilePath:            opts.CAFilePath,
			ClientCertificateMode: opts.ClientCertificates,
		}

		auth.AutoUser = x.Options.AutomationSubject
		auth.LdapGroupDN = opts.AutoLdapGroupDN
		auth.AutoAuthMechanisms = []string{string(MongoDBX509)}

		return nil
	}, log)

	if err != nil {
		return err
	}

	log.Info("Configuring backup agent user")
	err = x.Conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.EnableX509Authentication(opts.AutomationSubject)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)

	if err != nil {
		return err
	}

	log.Info("Configuring monitoring agent user")
	return x.Conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.EnableX509Authentication(opts.AutomationSubject)
		config.SetLdapGroupDN(opts.AutoLdapGroupDN)
		return nil
	}, log)
}

func (x ConnectionX509) DisableAgentAuthentication(log *zap.SugaredLogger) error {
	err := x.Conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {

		ac.AgentSSL = &om.AgentSSL{
			AutoPEMKeyFilePath:    util.MergoDelete,
			ClientCertificateMode: util.OptionalClientCertficates,
		}

		if stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(MongoDBX509)) {
			ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(MongoDBX509))
		}
		return nil

	}, log)
	if err != nil {
		return err
	}
	err = x.Conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)

	if err != nil {
		return err
	}

	return x.Conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
		config.DisableX509Authentication()
		return nil
	}, log)
}

func (x ConnectionX509) EnableDeploymentAuthentication(opts Options) error {
	ac := x.AutomationConfig
	if !stringutil.Contains(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) {
		ac.Auth.DeploymentAuthMechanisms = append(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
	}
	// AutomationConfig validation requires the CAFile path to be specified in the case of multiple auth
	// mechanisms enabled. This is not required if only X509 is being configured
	ac.AgentSSL.CAFilePath = opts.CAFilePath
	return nil
}

func (x ConnectionX509) DisableDeploymentAuthentication() error {
	ac := x.AutomationConfig
	ac.Auth.DeploymentAuthMechanisms = stringutil.Remove(ac.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
	return nil
}

func (x ConnectionX509) IsAgentAuthenticationConfigured() bool {
	ac := x.AutomationConfig
	if ac.Auth.Disabled {
		return false
	}

	if !stringutil.Contains(ac.Auth.AutoAuthMechanisms, string(MongoDBX509)) {
		return false
	}

	if !isValidX509Subject(ac.Auth.AutoUser) || ac.Auth.AutoPwd != util.MergoDelete {
		return false
	}

	if ac.Auth.Key == "" || ac.Auth.KeyFile == "" || ac.Auth.KeyFileWindows == "" {
		return false
	}

	return true
}

func (x ConnectionX509) IsDeploymentAuthenticationConfigured() bool {
	return stringutil.Contains(x.AutomationConfig.Auth.DeploymentAuthMechanisms, string(MongoDBX509))
}

// isValidX509Subject checks the subject contains CommonName, Country and Organizational Unit, Location and State.
func isValidX509Subject(subject string) bool {
	expected := []string{"CN", "C", "OU"}
	for _, name := range expected {
		matched, err := regexp.MatchString(name+`=\w+`, subject)
		if err != nil {
			continue
		}
		if !matched {
			return false
		}
	}
	return true
}

//canEnableX509 determines if it's possible to enable/disable x509 configuration options in the current
// version of Ops Manager
func canEnableX509(conn om.Connection) bool {
	err := conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
		return nil
	}, nil)
	if err != nil && strings.Contains(err.Error(), util.MethodNotAllowed) {
		return false
	}
	return true
}

func ConfigureStatefulSetSecret(sts *appsv1.StatefulSet, secretName string) {
	secretVolume := statefulset.CreateVolumeFromSecret(util.AgentSecretName, secretName)
	sts.Spec.Template.Spec.Containers[0].VolumeMounts = append(sts.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
		MountPath: "/mongodb-automation/" + util.AgentSecretName,
		Name:      secretVolume.Name,
		ReadOnly:  true,
	})
	sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes, secretVolume)
}
