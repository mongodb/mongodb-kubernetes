package webhook

import (
	"context"
	"io/ioutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	admissionv1beta "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This label must match the label used for Operator deployment
const controllerLabelName = "app.kubernetes.io/name"

// createWebhookService creates a Kubernetes service for the webhook.
func createWebhookService(client client.Client, location types.NamespacedName, webhookPort int) error {
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
				controllerLabelName: util.OperatorName,
			},
		},
	}

	// create the service if it doesn't already exist
	existingService := &corev1.Service{}
	err := client.Get(context.TODO(), location, existingService)
	if apiErrors.IsNotFound(err) {
		return client.Create(context.Background(), &svc)
	} else if err != nil {
		return err
	}

	// Update existing client with resource version and cluster IP
	svc.ResourceVersion = existingService.ResourceVersion
	svc.Spec.ClusterIP = existingService.Spec.ClusterIP
	return client.Update(context.Background(), &svc)
}

// GetWebhookConfig constructs a Kubernetes configuration resource for the
// validating admission webhook based on the name and namespace of the webhook
// service.
func GetWebhookConfig(serviceLocation types.NamespacedName) admissionv1beta.ValidatingWebhookConfiguration {
	caBytes, err := ioutil.ReadFile("/tmp/k8s-webhook-server/serving-certs/tls.crt")
	if err != nil {
		panic("could not read CA")
	}

	// need to make variables as one can't take the address of a constant
	var scope admissionv1beta.ScopeType = admissionv1beta.NamespacedScope
	var sideEffects admissionv1beta.SideEffectClass = admissionv1beta.SideEffectClassNone
	var failurePolicy admissionv1beta.FailurePolicyType = admissionv1beta.Ignore
	var port int32 = 443
	dbPath := "/validate-mongodb-com-v1-mongodb"
	omPath := "/validate-mongodb-com-v1-mongodbopsmanager"
	return admissionv1beta.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mdbpolicy.mongodb.com",
		},
		Webhooks: []admissionv1beta.ValidatingWebhook{
			{
				Name: "mdbpolicy.mongodb.com",
				ClientConfig: admissionv1beta.WebhookClientConfig{
					Service: &admissionv1beta.ServiceReference{
						Name:      serviceLocation.Name,
						Namespace: serviceLocation.Namespace,
						Path:      &dbPath,
						// NOTE: port isn't supported in k8s 1.11 and lower. It works in
						// 1.15 but I am unsure about the intervening versions.
						Port: &port,
					},
					CABundle: caBytes,
				},
				Rules: []admissionv1beta.RuleWithOperations{
					{
						Operations: []admissionv1beta.OperationType{
							admissionv1beta.Create,
							admissionv1beta.Update,
						},
						Rule: admissionv1beta.Rule{
							APIGroups:   []string{"mongodb.com"},
							APIVersions: []string{"*"},
							Resources:   []string{"mongodb"},
							Scope:       &scope,
						},
					},
				},
				SideEffects:   &sideEffects,
				FailurePolicy: &failurePolicy,
			},
			{
				Name: "ompolicy.mongodb.com",
				ClientConfig: admissionv1beta.WebhookClientConfig{
					Service: &admissionv1beta.ServiceReference{
						Name:      serviceLocation.Name,
						Namespace: serviceLocation.Namespace,
						Path:      &omPath,
						// NOTE: port isn't supported in k8s 1.11 and lower. It works in
						// 1.15 but I am unsure about the intervening versions.
						Port: &port,
					},
					CABundle: caBytes,
				},
				Rules: []admissionv1beta.RuleWithOperations{
					{
						Operations: []admissionv1beta.OperationType{
							admissionv1beta.Create,
							admissionv1beta.Update,
						},
						Rule: admissionv1beta.Rule{
							APIGroups:   []string{"mongodb.com"},
							APIVersions: []string{"*"},
							Resources:   []string{"opsmanagers"},
							Scope:       &scope,
						},
					},
				},
				SideEffects:   &sideEffects,
				FailurePolicy: &failurePolicy,
			},
		},
	}
}

func Setup(client client.Client, serviceLocation types.NamespacedName, certDirectory string, webhookPort int) error {
	if err := createWebhookService(client, serviceLocation, webhookPort); err != nil {
		return err
	}

	certHosts := []string{serviceLocation.Name + "." + serviceLocation.Namespace + ".svc"}
	if err := CreateCertFiles(certHosts, certDirectory); err != nil {
		return err
	}

	webhookConfig := GetWebhookConfig(serviceLocation)
	err := client.Create(context.Background(), &webhookConfig)
	if apiErrors.IsAlreadyExists(err) {
		// client.Update results in internal K8s error "Invalid value: 0x0: must be specified for an update"
		// (see https://github.com/kubernetes/kubernetes/issues/80515)
		// this fixed in K8s 1.16.0+
		if err := client.Delete(context.Background(), &webhookConfig); err != nil {
			return err
		}
		return client.Create(context.Background(), &webhookConfig)
	}
	return err
}
