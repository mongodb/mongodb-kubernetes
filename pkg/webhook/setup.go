package webhook

import (
	"context"
	"os"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubernetesClient "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/client"
	mekoService "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

// This label must match the label used for Operator deployment
const controllerLabelName = "app.kubernetes.io/name"

// createWebhookService creates a Kubernetes service for the webhook.
func createWebhookService(ctx context.Context, client client.Client, location types.NamespacedName, webhookPort int, multiClusterMode bool) error {
	svcSelector := util.OperatorName
	if multiClusterMode {
		svcSelector = util.MultiClusterOperatorName
	}

	svc := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      location.Name,
			Namespace: location.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       "operator",
					Port:       443,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(webhookPort),
				},
			},
			Selector: map[string]string{
				controllerLabelName: svcSelector,
			},
		},
	}

	// create the service if it doesn't already exist
	existingService := &corev1.Service{}
	err := client.Get(ctx, location, existingService)
	if apiErrors.IsNotFound(err) {
		return client.Create(ctx, &svc)
	} else if err != nil {
		return err
	}

	// Update existing client with resource version and cluster IP
	svc.ResourceVersion = existingService.ResourceVersion
	svc.Spec.ClusterIP = existingService.Spec.ClusterIP
	return client.Update(ctx, &svc)
}

// GetWebhookConfig constructs a Kubernetes configuration resource for the
// validating admission webhook based on the name and namespace of the webhook
// service.
func GetWebhookConfig(serviceLocation types.NamespacedName) admissionv1.ValidatingWebhookConfiguration {
	caBytes, err := os.ReadFile("/tmp/k8s-webhook-server/serving-certs/tls.crt")
	if err != nil {
		panic("could not read CA")
	}

	// need to make variables as one can't take the address of a constant
	scope := admissionv1.NamespacedScope
	sideEffects := admissionv1.SideEffectClassNone
	failurePolicy := admissionv1.Ignore
	var port int32 = 443
	dbPath := "/validate-mongodb-com-v1-mongodb"
	dbmultiPath := "/validate-mongodb-com-v1-mongodbmulticluster"
	omPath := "/validate-mongodb-com-v1-mongodbopsmanager"
	return admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mdbpolicy.mongodb.com",
		},
		Webhooks: []admissionv1.ValidatingWebhook{
			{
				Name: "mdbpolicy.mongodb.com",
				ClientConfig: admissionv1.WebhookClientConfig{
					Service: &admissionv1.ServiceReference{
						Name:      serviceLocation.Name,
						Namespace: serviceLocation.Namespace,
						Path:      &dbPath,
						// NOTE: port isn't supported in k8s 1.11 and lower. It works in
						// 1.15 but I am unsure about the intervening versions.
						Port: &port,
					},
					CABundle: caBytes,
				},
				Rules: []admissionv1.RuleWithOperations{
					{
						Operations: []admissionv1.OperationType{
							admissionv1.Create,
							admissionv1.Update,
						},
						Rule: admissionv1.Rule{
							APIGroups:   []string{"mongodb.com"},
							APIVersions: []string{"*"},
							Resources:   []string{"mongodb"},
							Scope:       &scope,
						},
					},
				},
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &sideEffects,
				FailurePolicy:           &failurePolicy,
			},
			{
				Name: "mdbmultipolicy.mongodb.com",
				ClientConfig: admissionv1.WebhookClientConfig{
					Service: &admissionv1.ServiceReference{
						Name:      serviceLocation.Name,
						Namespace: serviceLocation.Namespace,
						Path:      &dbmultiPath,
						Port:      &port,
					},
					CABundle: caBytes,
				},
				Rules: []admissionv1.RuleWithOperations{
					{
						Operations: []admissionv1.OperationType{
							admissionv1.Create,
							admissionv1.Update,
						},
						Rule: admissionv1.Rule{
							APIGroups:   []string{"mongodb.com"},
							APIVersions: []string{"*"},
							Resources:   []string{"mongodbmulticluster"},
							Scope:       &scope,
						},
					},
				},
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &sideEffects,
				FailurePolicy:           &failurePolicy,
			},
			{
				Name: "ompolicy.mongodb.com",
				ClientConfig: admissionv1.WebhookClientConfig{
					Service: &admissionv1.ServiceReference{
						Name:      serviceLocation.Name,
						Namespace: serviceLocation.Namespace,
						Path:      &omPath,
						// NOTE: port isn't supported in k8s 1.11 and lower. It works in
						// 1.15 but I am unsure about the intervening versions.
						Port: &port,
					},
					CABundle: caBytes,
				},
				Rules: []admissionv1.RuleWithOperations{
					{
						Operations: []admissionv1.OperationType{
							admissionv1.Create,
							admissionv1.Update,
						},
						Rule: admissionv1.Rule{
							APIGroups:   []string{"mongodb.com"},
							APIVersions: []string{"*"},
							Resources:   []string{"opsmanagers"},
							Scope:       &scope,
						},
					},
				},
				AdmissionReviewVersions: []string{"v1"},
				SideEffects:             &sideEffects,
				FailurePolicy:           &failurePolicy,
			},
		},
	}
}

func shouldRegisterWebhookConfiguration() bool {
	return env.ReadBoolOrDefault(util.MdbWebhookRegisterConfigurationEnv, true) // nolint:forbidigo
}

func Setup(ctx context.Context, client client.Client, serviceLocation types.NamespacedName, certDirectory string, webhookPort int, multiClusterMode bool, log *zap.SugaredLogger) error {
	if !shouldRegisterWebhookConfiguration() {
		log.Debugf("Skipping configuration of ValidatingWebhookConfiguration")
		// After upgrading OLM version after migrating to proper OLM webhooks we don't need that `operator-service` anymore.
		// By default, the service is created by the operator in createWebhookService below
		// It will also be useful here if someone decides to disable automatic webhook configuration by the operator.
		if err := mekoService.DeleteServiceIfItExists(ctx, kubernetesClient.NewClient(client), serviceLocation); err != nil {
			log.Warnf("Failed to delete webhook service %v: %w", serviceLocation, err)
			// we don't want to fail the operator startup if we cannot do the cleanup
		}

		webhookConfig := admissionv1.ValidatingWebhookConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name: "mdbpolicy.mongodb.com",
			},
		}
		if err := client.Delete(ctx, &webhookConfig); err != nil {
			if !apiErrors.IsNotFound(err) {
				log.Warnf("Failed to perform cleanup of ValidatingWebhookConfiguration %s. The operator might not have necessary permissions. Remove the configuration manually. Error: %s", webhookConfig.Name, err)
				// we don't want to fail the operator startup if we cannot do the cleanup
			}
		}

		return nil
	}

	webhookServerHost := []string{serviceLocation.Name + "." + serviceLocation.Namespace + ".svc"}
	if err := CreateCertFiles(webhookServerHost, certDirectory); err != nil {
		return err
	}

	if err := createWebhookService(ctx, client, serviceLocation, webhookPort, multiClusterMode); err != nil {
		return err
	}

	webhookConfig := GetWebhookConfig(serviceLocation)
	err := client.Create(ctx, &webhookConfig)
	if apiErrors.IsAlreadyExists(err) {
		// client.Update results in internal K8s error "Invalid value: 0x0: must be specified for an update"
		// (see https://github.com/kubernetes/kubernetes/issues/80515)
		// this fixed in K8s 1.16.0+
		if err = client.Delete(context.Background(), &webhookConfig); err == nil {
			err = client.Create(ctx, &webhookConfig)
		}
	}
	if err != nil {
		log.Warnf("Failed to configure admission webhooks. The operator might not have necessary permissions anymore. " +
			"Admission webhooks might not work correctly. Ignore this error if the cluster role for the operator was removed deliberately.")
		return nil
	}
	log.Debugf("Configured ValidatingWebhookConfiguration %s", webhookConfig.Name)

	return nil
}
