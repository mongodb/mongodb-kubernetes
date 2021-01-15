package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/pem"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/certs"
	enterprisesvc "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"
	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

type AuthMode string

const (
	NumAgents                    = 3
	externalConnectivityPortName = "external-connectivity-port"
	backupPortName               = "backup-port"
)

// createOrUpdateInKubernetes creates (updates if it exists) the StatefulSet with its Service.
// It returns any errors coming from Kubernetes API.
func createOrUpdateDatabaseInKubernetes(client kubernetesClient.Client, mdb mdbv1.MongoDB, sts appsv1.StatefulSet, config func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) error {
	opts := config(mdb)
	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		mdb.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := kube.ObjectKey(mdb.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &mdb, set.Spec.ServiceName, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(client, internalService, log)
	if err != nil {
		return err
	}

	if mdb.Spec.ExposedExternally {
		namespacedName := objectKey(mdb.Namespace, set.Spec.ServiceName+"-external")
		externalService := buildService(namespacedName, &mdb, set.Spec.ServiceName, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeNodePort})
		return enterprisesvc.CreateOrUpdateService(client, externalService, log)
	}

	return nil
}

// ensureQueryableAbleBackupService will make sure the queryable backup service exists. It will either update the existing external service
// if it exists, or create a new one if it does not.
func ensureQueryableAbleBackupService(serviceGetUpdateCreator service.GetUpdateCreator, opsManager omv1.MongoDBOpsManager, externalService corev1.Service, serviceName string, log *zap.SugaredLogger) error {
	backupSvcPort, err := opsManager.Spec.BackupSvcPort()
	if err != nil {
		return fmt.Errorf("can't parse queryable backup port: %s", err)
	}

	// If external connectivity is already configured, add a port to externalService
	if opsManager.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		externalService.Spec.Ports[0].Name = externalConnectivityPortName
		externalService.Spec.Ports = append(externalService.Spec.Ports, corev1.ServicePort{
			Name: backupPortName,
			Port: backupSvcPort,
		})
		return enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, externalService, log)
	}
	// Otherwise create a new service
	namespacedName := kube.ObjectKey(opsManager.Namespace, serviceName+"-backup")
	backupService := buildService(namespacedName, &opsManager, "ops-manager-backup", backupSvcPort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeLoadBalancer})

	return enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, backupService, log)

}

// createOrUpdateOpsManagerInKubernetes creates all of the required Kubernetes resources for Ops Manager.
// It creates the StatefulSet and all required services.
func createOrUpdateOpsManagerInKubernetes(client kubernetesClient.Client, opsManager omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) error {
	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		opsManager.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	_, port := opsManager.GetSchemePort()

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, set.Spec.ServiceName, int32(port), omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(client, internalService, log)
	if err != nil {
		return err
	}

	externalService := corev1.Service{}
	if opsManager.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName+"-ext")
		externalService = buildService(namespacedName, &opsManager, set.Spec.ServiceName, int32(port), *opsManager.Spec.MongoDBOpsManagerExternalConnectivity)
		err = enterprisesvc.CreateOrUpdateService(client, externalService, log)
		if err != nil {
			return err
		}
	}

	// Need to create queryable backup service
	if opsManager.Spec.Backup.Enabled {
		return ensureQueryableAbleBackupService(client, opsManager, externalService, set.Spec.ServiceName, log)
	}

	return err
}

func createOrUpdateBackupDaemonInKubernetes(client kubernetesClient.Client, opsManager omv1.MongoDBOpsManager, sts appsv1.StatefulSet, log *zap.SugaredLogger) (bool, error) {
	set, err := enterprisests.CreateOrUpdateStatefulset(
		client,
		opsManager.Namespace,
		log,
		&sts,
	)

	if err != nil {
		// Check if it is a k8s error or a custom one
		if _, ok := err.(enterprisests.StatefulSetCantBeUpdatedError); !ok {
			return false, err
		}
		// In this case, we delete the old Statefulset
		log.Debug("Deleting the old backup stateful set and creating a new one")
		stsNamespacedName := kube.ObjectKey(opsManager.Namespace, opsManager.BackupStatefulSetName())
		err = client.DeleteStatefulSet(stsNamespacedName)
		if err != nil {
			return false, fmt.Errorf("failed while trying to delete previous backup daemon statefulset: %s", err)
		}
		return true, nil
	}
	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, set.Spec.ServiceName, construct.BackupDaemonServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(client, internalService, log)
	return false, err
}

func createOrUpdateAppDBInKubernetes(client kubernetesClient.Client, opsManager omv1.MongoDBOpsManager, sts appsv1.StatefulSet, config func(om omv1.MongoDBOpsManager) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) error {
	opts := config(opsManager)
	set, err := enterprisests.CreateOrUpdateStatefulset(client,
		opsManager.Namespace,
		log,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := kube.ObjectKey(opsManager.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, &opsManager, set.Spec.ServiceName, opts.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	return enterprisesvc.CreateOrUpdateService(client, internalService, log)
}

// needToPublishStateFirst will check if the Published State of the StatfulSet backed MongoDB Deployments
// needs to be updated first. In the case of unmounting certs, for instance, the certs should be not
// required anymore before we unmount them, or the automation-agent and readiness probe will never
// reach goal state.
func needToPublishStateFirst(stsGetter statefulset.Getter, mdb mdbv1.MongoDB, configFunc func(mdb mdbv1.MongoDB) construct.DatabaseStatefulSetOptions, log *zap.SugaredLogger) bool {
	opts := configFunc(mdb)
	namespacedName := objectKey(mdb.Namespace, opts.Name)
	currentSts, err := stsGetter.GetStatefulSet(namespacedName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// No need to publish state as this is a new StatefulSet
			log.Debugf("New StatefulSet %s", namespacedName)
			return false
		}

		log.Debugw(fmt.Sprintf("Error getting StatefulSet %s", namespacedName), "error", err)
		return false
	}

	volumeMounts := currentSts.Spec.Template.Spec.Containers[0].VolumeMounts
	if mdb.Spec.Security != nil {
		if !mdb.Spec.Security.TLSConfig.Enabled && volumeMountWithNameExists(volumeMounts, util.SecretVolumeName) {
			log.Debug("About to set `security.tls.enabled` to false. automationConfig needs to be updated first")
			return true
		}

		if mdb.Spec.Security.TLSConfig.CA == "" && volumeMountWithNameExists(volumeMounts, ConfigMapVolumeCAName) {
			log.Debug("About to set `security.tls.CA` to empty. automationConfig needs to be updated first")
			return true
		}
	}

	if opts.PodVars.SSLMMSCAConfigMap == "" && volumeMountWithNameExists(volumeMounts, CaCertName) {
		log.Debug("About to set `SSLMMSCAConfigMap` to empty. automationConfig needs to be updated first")
		return true
	}

	if mdb.Spec.Security.GetAgentMechanism(opts.CurrentAgentAuthMode) != util.X509 && volumeMountWithNameExists(volumeMounts, util.AgentSecretName) {
		log.Debug("About to set `project.AuthMode` to empty. automationConfig needs to be updated first")
		return true
	}

	if int32(opts.Replicas) < *currentSts.Spec.Replicas {
		log.Debug("Scaling down operation. automationConfig needs to be updated first")
		return true
	}

	return false
}

func volumeMountWithNameExists(mounts []corev1.VolumeMount, volumeName string) bool {
	for _, mount := range mounts {
		if mount.Name == volumeName {
			return true
		}
	}

	return false
}

// ensureAutomationConfigSecret fetches the existing Secret and applies the callback to it and pushes changes back.
// The callback is expected to update the data in Secret or return false if no update/create is needed
// Returns the final Secret (could be the initial one or the one after the update)
func ensureAutomationConfigSecret(secretGetUpdateCreator secret.GetUpdateCreator, nsName client.ObjectKey, callback func(*corev1.Secret) bool, owner v1.CustomResourceReadWriter) (corev1.Secret, error) {
	existingSecret, err := secretGetUpdateCreator.GetSecret(nsName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			newSecret := secret.Builder().
				SetName(nsName.Name).
				SetNamespace(nsName.Namespace).
				SetOwnerReferences(baseOwnerReference(owner)).
				Build()

			if !callback(&newSecret) {
				return corev1.Secret{}, nil
			}

			if err := secretGetUpdateCreator.CreateSecret(newSecret); err != nil {
				return corev1.Secret{}, err
			}
			return newSecret, nil
		}
		return corev1.Secret{}, err
	}
	// We are updating the existing Secret
	if !callback(&existingSecret) {
		return existingSecret, nil
	}
	if err := secretGetUpdateCreator.UpdateSecret(existingSecret); err != nil {
		return existingSecret, err
	}
	return existingSecret, nil
}

// validateSelfManagedSSLCertsForStatefulSet ensures that a stateful set using
// user-provided certificates has all of the relevant certificates in place.
func validateSelfManagedSSLCertsForStatefulSet(client kubernetesClient.Client, secretName string, opts certs.Options) workflow.Status {
	// A "Certs" attribute has been provided
	// This means that the customer has provided with a secret name they have
	// already populated with the certs and keys for this deployment.
	// Because of the async nature of Kubernetes, this object might not be ready yet,
	// in which case, we'll keep reconciling until the object is created and is correct.
	if notReadyCerts := certs.VerifyCertificatesForStatefulSet(client, secretName, opts); notReadyCerts > 0 {
		return workflow.Failed("The secret object '%s' does not contain all the certificates needed."+
			"Required: %d, contains: %d", secretName,
			opts.Replicas,
			opts.Replicas-notReadyCerts,
		)
	}

	if err := certs.ValidateCertificates(client, secretName, opts.Namespace); err != nil {
		return workflow.Failed(err.Error())
	}

	return workflow.OK()
}

// ensureOperatorManagedSSLCertsForStatefulSet ensures that a stateful set
// using operator-managed certificates has all of the relevant certificates in
// place.
func ensureOperatorManagedSSLCertsForStatefulSet(client kubernetesClient.Client, secretName string, opts certs.Options, log *zap.SugaredLogger) workflow.Status {
	certsNeedApproval := false

	if err := certs.ValidateCertificates(client, secretName, opts.Namespace); err != nil {
		return workflow.Failed(err.Error())
	}

	if notReadyCerts := certs.VerifyCertificatesForStatefulSet(client, secretName, opts); notReadyCerts > 0 {
		// If the Kube CA and the operator are responsible for the certificates to be
		// ready and correctly stored in the secret object, and this secret is not "complete"
		// we'll go through the process of creating the CSR, wait for certs approval and then
		// creating a correct secret with the certificates and keys.

		// For replica set we need to create rs.Spec.Replicas certificates, one per each Pod
		fqdns, podnames := certs.GetDNSNames(opts)

		// pemFiles will store every key (during the CSR creation phase) and certificate
		// both can happen on different reconciliation stages (CSR and keys are created, then
		// reconciliation, then certs are obtained from the CA). If this happens we need to
		// store the keys in the final secret, that will be updated with the certs, once they
		// are issued by the CA.
		pemFiles := pem.NewCollection()

		for idx, host := range fqdns {
			csr, err := certs.ReadCSR(client, podnames[idx], opts.Namespace)
			additionalCertDomains := certs.GetAdditionalCertDomainsForMember(opts, idx)
			if err != nil {
				certsNeedApproval = true
				hostnames := []string{host, podnames[idx]}
				hostnames = append(hostnames, additionalCertDomains...)
				key, err := certs.CreateTlsCSR(client, podnames[idx], opts.Namespace, clusterDomainOrDefault(opts.ClusterDomain), hostnames, host)
				if err != nil {
					return workflow.Failed("Failed to create CSR, %s", err)
				}

				// This note was added on Release 1.5.1 of the Operator.
				log.Warn("The Operator is generating TLS certificates for server authentication. " + TLSGenerationDeprecationWarning)

				pemFiles.AddPrivateKey(podnames[idx], string(key))
			} else if !certs.CSRHasRequiredDomains(csr, additionalCertDomains) {
				log.Infow(
					"Certificate request does not have all required domains",
					"requiredDomains", additionalCertDomains,
					"host", host,
				)
				return workflow.Pending("Certificate request for " + host + " doesn't have all required domains. Please manually remove the CSR in order to proceed.")
			} else if certs.CSRWasApproved(csr) {
				log.Infof("Certificate for Pod %s -> Approved", host)
				pemFiles.AddCertificate(podnames[idx], string(csr.Status.Certificate))
			} else {
				log.Infof("Certificate for Pod %s -> Waiting for Approval", host)
				certsNeedApproval = true
			}
		}

		// once we are here we know we have built everything we needed
		// This "secret" object corresponds to the certificates for this statefulset
		labels := make(map[string]string)
		labels["mongodb/secure"] = "certs"
		labels["mongodb/operator"] = "certs." + secretName

		// note that createOrUpdateSecret modifies pemFiles in place by merging
		// in the existing values in the secret

		// TODO: client should not come from KubeHelper
		err := pem.CreateOrUpdateSecret(client, secretName, opts.Namespace, pemFiles)
		if err != nil {
			// If we have an error creating or updating the secret, we might lose
			// the keys, in which case we return an error, to make it clear what
			// the error was to customers -- this should end up in the status
			// message.
			return workflow.Failed("Failed to create or update the secret: %s", err)
		}
	}

	if certsNeedApproval {
		return workflow.Pending("Not all certificates have been approved by Kubernetes CA for %s", opts.Name)
	}
	return workflow.OK()
}

// ensureSSLCertsForStatefulSet contains logic to ensure that all of the
// required SSL certs for a StatefulSet object exist.
func ensureSSLCertsForStatefulSet(client kubernetesClient.Client, mdb mdbv1.MongoDB, opts certs.Options, log *zap.SugaredLogger) workflow.Status {
	if !mdb.Spec.IsTLSEnabled() {
		// if there's no SSL certs to generate, return
		return workflow.OK()
	}

	secretName := opts.Name + "-cert"
	if mdb.Spec.Security.TLSConfig.IsSelfManaged() {
		if mdb.Spec.Security.TLSConfig.SecretRef.Name != "" {
			secretName = mdb.Spec.Security.TLSConfig.SecretRef.Name
		}
		return validateSelfManagedSSLCertsForStatefulSet(client, secretName, opts)
	}
	return ensureOperatorManagedSSLCertsForStatefulSet(client, secretName, opts, log)
}

// envVarFromSecret returns a corev1.EnvVar that is a reference to a secret with the field
// "secretKey" being used
func envVarFromSecret(envVarName, secretName, secretKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envVarName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
				Key: secretKey,
			},
		},
	}
}
