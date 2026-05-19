package secret

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cast"
	"k8s.io/client-go/kubernetes"
)

const (
	automationConfigKey = "cluster-config.json"
)

func ReadAutomationConfigVersionFromSecret(ctx context.Context, namespace string, clientSet kubernetes.Interface, automationConfigSecret string) (int64, error) {
	secretReader := newKubernetesSecretReader(clientSet)
	theSecret, secretReadErr := secretReader.ReadSecret(ctx, namespace, automationConfigSecret)
	if secretReadErr != nil {
		return -1, fmt.Errorf("failed to read automation config secret %s/%s: %w", namespace, automationConfigSecret, secretReadErr)
	}

	var existingDeployment map[string]interface{}
	if err := json.Unmarshal(theSecret.Data[automationConfigKey], &existingDeployment); err != nil {
		return -1, fmt.Errorf("failed to unmarshal automation config %s key from %s/%s secret: %w", automationConfigKey, namespace, automationConfigSecret, err)
	}

	version, ok := existingDeployment["version"]
	if !ok {
		return -1, fmt.Errorf("version field is missing in the automation config %s key from %s/%s secret", automationConfigKey, namespace, automationConfigSecret)
	}

	return cast.ToInt64(version), nil
}
