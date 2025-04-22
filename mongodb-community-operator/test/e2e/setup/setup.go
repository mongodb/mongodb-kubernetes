package setup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/helm"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/envvar"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/util/generate"
	e2eutil "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/test/e2e"
	waite2e "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/test/e2e/util/wait"
)

type HelmArg struct {
	Name  string
	Value string
}

const (
	performCleanupEnv                   = "PERFORM_CLEANUP"
	CommunityHelmChartAndDeploymentName = "mongodb-community-operator"
	MCKHelmChartAndDeploymentName       = "mongodb-mck-operator"
)

func Setup(ctx context.Context, t *testing.T) *e2eutil.TestContext {
	testCtx, err := e2eutil.NewContext(ctx, t, envvar.ReadBool(performCleanupEnv)) // nolint:forbidigo
	if err != nil {
		t.Fatal(err)
	}

	config := LoadTestConfigFromEnv()
	if err := DeployMCKOperator(ctx, t, config, "mdb", false, false); err != nil {
		t.Fatal(err)
	}

	return testCtx
}

func SetupWithTLS(ctx context.Context, t *testing.T, resourceName string, additionalHelmArgs ...HelmArg) (*e2eutil.TestContext, TestConfig) {
	textCtx, err := e2eutil.NewContext(ctx, t, envvar.ReadBool(performCleanupEnv)) // nolint:forbidigo
	if err != nil {
		t.Fatal(err)
	}

	config := LoadTestConfigFromEnv()
	if err := deployCertManager(t, config); err != nil {
		t.Fatal(err)
	}

	if err := DeployMCKOperator(ctx, t, config, resourceName, true, false, additionalHelmArgs...); err != nil {
		t.Fatal(err)
	}

	return textCtx, config
}

func SetupWithTestConfig(ctx context.Context, t *testing.T, testConfig TestConfig, withTLS, defaultOperator bool, resourceName string) *e2eutil.TestContext {
	testCtx, err := e2eutil.NewContext(ctx, t, envvar.ReadBool(performCleanupEnv)) // nolint:forbidigo
	if err != nil {
		t.Fatal(err)
	}

	if withTLS {
		if err := deployCertManager(t, testConfig); err != nil {
			t.Fatal(err)
		}
	}

	if err := DeployMCKOperator(ctx, t, testConfig, resourceName, withTLS, defaultOperator); err != nil {
		t.Fatal(err)
	}

	return testCtx
}

func SetupWithTestConfigNoOperator(ctx context.Context, t *testing.T, testConfig TestConfig, withTLS bool) *e2eutil.TestContext {
	testCtx, err := e2eutil.NewContext(ctx, t, envvar.ReadBool(performCleanupEnv)) // nolint:forbidigo
	if err != nil {
		t.Fatal(err)
	}

	if withTLS {
		if err := deployCertManager(t, testConfig); err != nil {
			t.Fatal(err)
		}
	}

	return testCtx
}

// GeneratePasswordForUser will create a secret with a password for the given user
func GeneratePasswordForUser(testCtx *e2eutil.TestContext, mdbu mdbv1.MongoDBUser, namespace string) (string, error) {
	passwordKey := mdbu.PasswordSecretRef.Key
	if passwordKey == "" {
		passwordKey = "password"
	}

	password, err := generate.RandomFixedLengthStringOfSize(20)
	if err != nil {
		return "", err
	}

	nsp := namespace
	if nsp == "" {
		nsp = e2eutil.OperatorNamespace
	}

	passwordSecret := secret.Builder().
		SetName(mdbu.PasswordSecretRef.Name).
		SetNamespace(nsp).
		SetField(passwordKey, password).
		SetLabels(e2eutil.TestLabels()).
		Build()

	return password, e2eutil.TestClient.Create(testCtx.Ctx, &passwordSecret, &e2eutil.CleanupOptions{TestContext: testCtx})
}

// extractRegistryNameAndVersion splits a full image string and returns the individual components.
// this function expects the input to be in the form of some/registry/imagename:tag.
func extractRegistryNameAndVersion(fullImage string) (string, string, string) {
	splitString := strings.Split(fullImage, "/")
	registry := strings.Join(splitString[:len(splitString)-1], "/")

	splitString = strings.Split(splitString[len(splitString)-1], ":")
	version := "latest"
	if len(splitString) > 1 {
		version = splitString[len(splitString)-1]
	}
	name := splitString[0]
	return registry, name, version
}

// getHelmArgs returns a map of helm arguments that are required to install the operator.
func getHelmArgs(testConfig TestConfig, watchNamespace string, resourceName string, withTLS bool, defaultOperator bool, additionalHelmArgs ...HelmArg) map[string]string {
	agentRegistry, agentName, agentVersion := extractRegistryNameAndVersion(testConfig.AgentImage)
	versionUpgradeHookRegistry, versionUpgradeHookName, versionUpgradeHookVersion := extractRegistryNameAndVersion(testConfig.VersionUpgradeHookImage)
	readinessProbeRegistry, readinessProbeName, readinessProbeVersion := extractRegistryNameAndVersion(testConfig.ReadinessProbeImage)
	helmArgs := make(map[string]string)

	helmArgs["namespace"] = testConfig.Namespace

	helmArgs["operator.watchNamespace"] = watchNamespace

	if !defaultOperator {
		helmArgs["operator.operator_image_name"] = testConfig.OperatorImage
		helmArgs["operator.version"] = testConfig.OperatorVersion
		helmArgs["registry.operator"] = testConfig.OperatorImageRepoUrl

		helmArgs["community.agent.version"] = agentVersion
		helmArgs["community.agent.name"] = agentName

		helmArgs["community.mongodb.name"] = testConfig.MongoDBImage
		helmArgs["community.mongodb.repo"] = testConfig.MongoDBRepoUrl
		helmArgs["community.registry.agent"] = agentRegistry

		helmArgs["registry.versionUpgradeHook"] = versionUpgradeHookRegistry
		helmArgs["registry.readinessProbe"] = readinessProbeRegistry
		helmArgs["registry.imagePullSecrets"] = "image-registries-secret"
		helmArgs["versionUpgradeHook.name"] = versionUpgradeHookName
		helmArgs["versionUpgradeHook.version"] = versionUpgradeHookVersion

		helmArgs["readinessProbe.name"] = readinessProbeName
		helmArgs["readinessProbe.version"] = readinessProbeVersion
	}

	// only used for one mco tls test
	helmArgs["community.createResource"] = strconv.FormatBool(false)
	helmArgs["community.resource.name"] = resourceName
	helmArgs["community.resource.tls.enabled"] = strconv.FormatBool(withTLS)
	helmArgs["community.resource.tls.useCertManager"] = strconv.FormatBool(withTLS)

	for _, arg := range additionalHelmArgs {
		helmArgs[arg.Name] = arg.Value
	}

	return helmArgs
}

// getMCOHelmArgs returns a map of helm arguments that were used to install mco with the mco chart
func getMCOHelmArgs(testConfig TestConfig, watchNamespace string, resourceName string, withTLS bool, additionalHelmArgs ...HelmArg) map[string]string {
	agentRegistry, agentName, agentVersion := extractRegistryNameAndVersion(testConfig.AgentImage)
	versionUpgradeHookRegistry, versionUpgradeHookName, versionUpgradeHookVersion := extractRegistryNameAndVersion(testConfig.VersionUpgradeHookImage)
	readinessProbeRegistry, readinessProbeName, readinessProbeVersion := extractRegistryNameAndVersion(testConfig.ReadinessProbeImage)
	helmArgs := make(map[string]string)

	helmArgs["namespace"] = testConfig.Namespace

	helmArgs["operator.watchNamespace"] = watchNamespace

	helmArgs["operator.operatorImageName"] = "mongodb-kubernetes-operator"
	helmArgs["operator.version"] = "0.12.0"
	helmArgs["versionUpgradeHook.name"] = versionUpgradeHookName
	helmArgs["versionUpgradeHook.version"] = versionUpgradeHookVersion

	helmArgs["readinessProbe.name"] = readinessProbeName
	helmArgs["readinessProbe.version"] = readinessProbeVersion

	helmArgs["agent.version"] = agentVersion
	helmArgs["agent.name"] = agentName

	helmArgs["mongodb.name"] = testConfig.MongoDBImage
	helmArgs["mongodb.repo"] = testConfig.MongoDBRepoUrl

	helmArgs["registry.versionUpgradeHook"] = versionUpgradeHookRegistry
	helmArgs["registry.operator"] = "quay.io/mongodb"
	helmArgs["registry.agent"] = agentRegistry
	helmArgs["registry.readinessProbe"] = readinessProbeRegistry
	helmArgs["registry.imagePullSecrets"] = "image-registries-secret"

	helmArgs["createResource"] = strconv.FormatBool(false)
	helmArgs["resource.name"] = resourceName
	helmArgs["resource.tls.enabled"] = strconv.FormatBool(withTLS)
	helmArgs["resource.tls.useCertManager"] = strconv.FormatBool(withTLS)

	for _, arg := range additionalHelmArgs {
		helmArgs[arg.Name] = arg.Value
	}

	return helmArgs
}

// DeployMCKOperator installs all resources required by the operator using helm.
func DeployMCKOperator(ctx context.Context, t *testing.T, config TestConfig, resourceName string, withTLS bool, defaultOperator bool, additionalHelmArgs ...HelmArg) error {
	e2eutil.OperatorNamespace = config.Namespace
	fmt.Printf("Setting operator namespace to %s\n", e2eutil.OperatorNamespace)
	watchNamespace := config.Namespace
	if config.ClusterWide {
		watchNamespace = "*"
	}
	fmt.Printf("Setting namespace to watch to %s\n", watchNamespace)

	if err := helm.Uninstall(t, MCKHelmChartAndDeploymentName, config.Namespace); err != nil {
		return err
	}

	helmArgs := getHelmArgs(config, watchNamespace, resourceName, withTLS, defaultOperator, additionalHelmArgs...)
	helmFlags := map[string]string{
		"namespace": config.Namespace,
	}

	if config.LocalOperator {
		helmArgs["operator.replicas"] = "0"
	}

	helmArgs["operator.name"] = MCKHelmChartAndDeploymentName

	if err := helm.Install(t, config.HelmChartPath, MCKHelmChartAndDeploymentName, helmFlags, helmArgs); err != nil {
		return err
	}

	dep, err := waite2e.ForDeploymentToExist(ctx, MCKHelmChartAndDeploymentName, time.Second*10, time.Minute*1, e2eutil.OperatorNamespace)
	if err != nil {
		return err
	}

	quantityCPU, err := resource.ParseQuantity("50m")
	if err != nil {
		return err
	}

	for _, cont := range dep.Spec.Template.Spec.Containers {
		cont.Resources.Requests["cpu"] = quantityCPU
	}

	err = e2eutil.TestClient.Update(ctx, &dep)
	if err != nil {
		return err
	}

	if err := wait.PollUntilContextTimeout(ctx, time.Second*2, 120*time.Second, true, hasDeploymentRequiredReplicas(&dep)); err != nil {
		return errors.New("error building operator deployment: the deployment does not have the required replicas")
	}
	fmt.Println("Successfully installed the operator deployment")
	return nil
}

func deployCertManager(t *testing.T, config TestConfig) error {
	const helmChartName = "cert-manager"
	if err := helm.Uninstall(t, helmChartName, config.CertManagerNamespace); err != nil {
		return fmt.Errorf("failed to uninstall cert-manager Helm chart: %s", err)
	}

	chartUrl := fmt.Sprintf("https://charts.jetstack.io/charts/cert-manager-%s.tgz", config.CertManagerVersion)
	flags := map[string]string{
		"version":          config.CertManagerVersion,
		"namespace":        config.CertManagerNamespace,
		"create-namespace": "",
	}
	values := map[string]string{"installCRDs": "true"}
	if err := helm.Install(t, chartUrl, helmChartName, flags, values); err != nil {
		return fmt.Errorf("failed to install cert-manager Helm chart: %s", err)
	}
	return nil
}

// hasDeploymentRequiredReplicas returns a condition function that indicates whether the given deployment
// currently has the required amount of replicas in the ready state as specified in spec.replicas
func hasDeploymentRequiredReplicas(dep *appsv1.Deployment) wait.ConditionWithContextFunc {
	return func(ctx context.Context) (bool, error) {
		err := e2eutil.TestClient.Get(ctx,
			types.NamespacedName{
				Name:      dep.Name,
				Namespace: e2eutil.OperatorNamespace,
			},
			dep)
		if err != nil {
			if apiErrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("error getting operator deployment: %s", err)
		}
		if dep.Status.ReadyReplicas == *dep.Spec.Replicas {
			return true, nil
		}
		fmt.Printf("Deployment not ready! ReadyReplicas: %d, Spec.Replicas: %d\n", dep.Status.ReadyReplicas, *dep.Spec.Replicas)
		return false, nil
	}
}

// InstallCommunityOperatorViaHelm installs the community operator using the public MongoDB Helm chart.
func InstallCommunityOperatorViaHelm(ctx context.Context, t *testing.T, config TestConfig, namespace string, additionalHelmArgs ...HelmArg) error {
	e2eutil.OperatorNamespace = config.Namespace

	// Uninstall any existing chart with the same name
	if err := helm.Uninstall(t, CommunityHelmChartAndDeploymentName, namespace); err != nil {
		return err
	}

	// Add the MongoDB repo if needed
	addRepoCmd := exec.CommandContext(ctx, "helm", "repo", "add", "mongodb", "https://mongodb.github.io/helm-charts")
	if output, err := addRepoCmd.CombinedOutput(); err != nil {
		t.Logf("Failed to add MongoDB Helm repo: %s", string(output))
		return err
	}

	updateCmd := exec.CommandContext(ctx, "helm", "repo", "update")
	if output, err := updateCmd.CombinedOutput(); err != nil {
		t.Logf("Failed to update Helm repos: %s", string(output))
		return err
	}

	// Configure helm flags and args
	helmFlags := map[string]string{
		"namespace": namespace,
	}

	helmArgs := getMCOHelmArgs(config, namespace, "mdb", false, additionalHelmArgs...)
	helmArgs["operator.name"] = CommunityHelmChartAndDeploymentName

	// Apply any additional helm args
	for _, arg := range additionalHelmArgs {
		helmArgs[arg.Name] = arg.Value
	}

	// Use the helm package to install the chart
	if err := helm.Install(t, "mongodb/community-operator", CommunityHelmChartAndDeploymentName, helmFlags, helmArgs); err != nil {
		return fmt.Errorf("failed to install community operator: %s", err)
	}

	t.Logf("Community operator installed successfully")

	// Wait for the deployment to be ready
	dep, err := waite2e.ForDeploymentToExist(ctx, CommunityHelmChartAndDeploymentName, time.Second*10, time.Minute*1, namespace)
	if err != nil {
		return err
	}

	if err := wait.PollUntilContextTimeout(ctx, time.Second*2, 120*time.Second, true, hasDeploymentRequiredReplicas(&dep)); err != nil {
		return errors.New("error building community operator deployment: the deployment does not have the required replicas")
	}

	fmt.Println("Successfully installed the community operator deployment")
	return nil
}

// UninstallCommunityOperatorViaHelm uninstalls the community operator using the public MongoDB Helm chart.
func UninstallCommunityOperatorViaHelm(ctx context.Context, t *testing.T, namespace string) error {
	cmd := exec.CommandContext(ctx, "helm", "uninstall", CommunityHelmChartAndDeploymentName, "--namespace", namespace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Failed to uninstall community operator: %s", string(output))
		return err
	}
	t.Logf("Community operator uninstalled: %s", string(output))
	return nil
}

// ScaleOperatorDeployment scales the operator deployment to the specified number of replicas
// and waits for all replicas to become ready.
func ScaleOperatorDeployment(ctx context.Context, t *testing.T, namespace, deploymentName string, replicas int32) error {
	cmd := exec.CommandContext(ctx, "kubectl", "scale", "deployment", deploymentName, fmt.Sprintf("--replicas=%d", replicas), "--namespace", namespace) //nolint:gosec
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Failed to scale deployment: %s", string(output))
		return err
	}
	t.Logf("Scaling deployment %s to %d replicas: %s", deploymentName, replicas, string(output))

	// Get the deployment object
	var dep appsv1.Deployment
	if err := e2eutil.TestClient.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: namespace,
	}, &dep); err != nil {
		return fmt.Errorf("failed to get deployment after scaling: %s", err)
	}

	// Update the replicas spec to match what we're expecting
	dep.Spec.Replicas = &replicas

	// Wait for the deployment to reach the desired ready replicas
	if err := wait.PollUntilContextTimeout(ctx, time.Second*2, time.Minute*2, true, hasDeploymentRequiredReplicas(&dep)); err != nil {
		return fmt.Errorf("error waiting for deployment %s to scale to %d replicas: %s", deploymentName, replicas, err)
	}

	// Only for scale-to-zero: additionally wait for pods to be fully gone
	if replicas == 0 {
		t.Logf("Waiting for pods to fully terminate...")
		err := wait.PollUntilContextTimeout(ctx, time.Second*5, time.Minute*2, true, func(ctx context.Context) (bool, error) {
			// List pods belonging to this deployment
			var podList corev1.PodList
			if err := e2eutil.TestClient.Client.List(ctx, &podList,
				client.InNamespace(namespace),
				client.MatchingLabels(map[string]string{"name": deploymentName})); err != nil {
				t.Logf("Error listing pods: %v", err)
				return false, nil
			}

			if len(podList.Items) > 0 {
				t.Logf("Still waiting for %d pods to terminate", len(podList.Items))
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			return fmt.Errorf("pods did not fully terminate: %v", err)
		}
		t.Logf("All pods successfully terminated")
	}

	t.Logf("Successfully scaled deployment %s to %d replicas and all are ready", deploymentName, replicas)
	return nil
}
