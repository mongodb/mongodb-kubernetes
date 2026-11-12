package webhook

import (
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	admissionv1 "k8s.io/api/admissionregistration/v1"
)

func webhooksByPath(config admissionv1.ValidatingWebhookConfiguration) map[string]admissionv1.ValidatingWebhook {
	webhooks := map[string]admissionv1.ValidatingWebhook{}
	for _, webhook := range config.Webhooks {
		webhooks[*webhook.ClientConfig.Service.Path] = webhook
	}
	return webhooks
}

// OLM and kustomize deployments use config/webhooks/webhooks.yaml instead of the configuration
// the operator registers at startup, so both must route the same requests to the same handlers.
func TestWebhookConfigMatchesKustomizeConfig(t *testing.T) {
	data, err := os.ReadFile("../../config/webhooks/webhooks.yaml")
	require.NoError(t, err)
	var kustomizeConfig admissionv1.ValidatingWebhookConfiguration
	require.NoError(t, yaml.UnmarshalStrict(data, &kustomizeConfig))

	operatorWebhooks := webhooksByPath(GetWebhookConfig(types.NamespacedName{}, nil))
	kustomizeWebhooks := webhooksByPath(kustomizeConfig)
	require.ElementsMatch(t, slices.Collect(maps.Keys(kustomizeWebhooks)), slices.Collect(maps.Keys(operatorWebhooks)))

	for path, operatorWebhook := range operatorWebhooks {
		t.Run(path, func(t *testing.T) {
			kustomizeWebhook := kustomizeWebhooks[path]
			assert.Equal(t, kustomizeWebhook.AdmissionReviewVersions, operatorWebhook.AdmissionReviewVersions)
			assert.Equal(t, kustomizeWebhook.SideEffects, operatorWebhook.SideEffects)
			assert.Equal(t, kustomizeWebhook.FailurePolicy, operatorWebhook.FailurePolicy)

			require.Len(t, kustomizeWebhook.Rules, 1)
			require.Len(t, operatorWebhook.Rules, 1)
			assert.Equal(t, kustomizeWebhook.Rules[0].Operations, operatorWebhook.Rules[0].Operations)
			assert.Equal(t, kustomizeWebhook.Rules[0].APIGroups, operatorWebhook.Rules[0].APIGroups)
			assert.Equal(t, kustomizeWebhook.Rules[0].Resources, operatorWebhook.Rules[0].Resources)
		})
	}
}
