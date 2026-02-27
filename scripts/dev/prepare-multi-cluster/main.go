package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	serviceAccountName       = "operator-tests-multi-cluster-service-account"
	tokenSecretSuffix        = "-token-secret"
	projectConfigMapName     = "my-project"
	credentialsSecretName    = "my-credentials"
	kubeconfigSecretName     = "test-pod-kubeconfig"
	multiClusterSecretName   = "test-pod-multi-cluster-config"
	imageRegistriesSecret    = "image-registries-secret"
	kubernetesServiceName    = "kubernetes"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg := loadConfig()

	fmt.Println("Preparing multi-cluster environment")
	fmt.Printf("  Central cluster: %s\n", cfg.centralCluster)
	fmt.Printf("  Member clusters: %v\n", cfg.memberClusters)
	fmt.Printf("  Namespace: %s\n", cfg.namespace)

	errorMap := make(map[string][]error)
	var errorMapMu sync.Mutex
	collectErrorFor := func(cluster string) func(error, string) {
		return func(err error, msg string) {
			if err != nil && !kerrors.IsNotFound(err) {
				errorMapMu.Lock()
				errorMap[cluster] = append(errorMap[cluster], fmt.Errorf("%s: %v", msg, err))
				errorMapMu.Unlock()
			}
		}
	}

	// All unique clusters (central may overlap with member)
	allClusters := uniqueClusters(cfg.centralCluster, cfg.memberClusters)

	// Pre-create clients for all clusters
	clients := make(map[string]*kubernetes.Clientset)
	dynamicClients := make(map[string]dynamic.Interface)
	for _, cluster := range allClusters {
		client, dynClient, err := initKubeClient(cluster)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create client for %s: %v\n", cluster, err)
			os.Exit(1)
		}
		clients[cluster] = client
		dynamicClients[cluster] = dynClient
	}

	// Phase 1: KIND kubeconfig overrides
	var kindKubeconfig string
	if cfg.clusterType == "kind" {
		fmt.Println("Phase 1: Overriding kubeconfig API servers for KIND clusters")
		kindKubeconfig = overrideKindKubeconfig(ctx, cfg, clients, allClusters, collectErrorFor("kind-override"))
	}

	// Phase 2: Ensure namespaces + label istio-injection (parallel per member cluster)
	fmt.Println("Phase 2: Ensuring namespaces")
	ensureNamespace(ctx, clients[cfg.centralCluster], cfg.centralCluster, cfg.namespace, cfg.taskID, collectErrorFor(cfg.centralCluster))
	runParallel(cfg.memberClusters, func(cluster string) {
		collectError := collectErrorFor(cluster)
		ensureNamespace(ctx, clients[cluster], cluster, cfg.namespace, cfg.taskID, collectError)
		labelNamespaceIstio(ctx, clients[cluster], cluster, cfg.namespace, collectError)
		if cfg.applyMTLS {
			applyPeerAuthentication(ctx, dynamicClients[cluster], cluster, cfg.namespace, collectError)
		}
	})

	// Phase 3: Create RBAC + secrets (parallel, no deps between clusters)
	// Manual WaitGroup here since we're running different operations across different clusters,
	// unlike Phase 2/4 where every cluster does the same thing.
	fmt.Println("Phase 3: Creating RBAC, kubeconfig secret, project config, and credentials")
	var phase3wg sync.WaitGroup

	// SA + ClusterRoleBinding in all clusters
	for _, cluster := range allClusters {
		phase3wg.Add(1)
		go func(c string) {
			defer phase3wg.Done()
			collectError := collectErrorFor(c)
			ensureServiceAccount(ctx, clients[c], c, cfg.namespace, collectError)
			ensureClusterRoleBinding(ctx, clients[c], c, cfg.namespace, collectError)
		}(cluster)
	}

	// Kubeconfig secret in test-pod cluster
	phase3wg.Add(1)
	go func() {
		defer phase3wg.Done()
		kubeconfigPath := cfg.kubeconfigPath
		if cfg.clusterType == "kind" && kindKubeconfig != "" {
			kubeconfigPath = kindKubeconfig
		}
		createKubeconfigSecret(ctx, clients[cfg.testPodCluster], cfg.testPodCluster, cfg.namespace, kubeconfigPath, collectErrorFor(cfg.testPodCluster))
	}()

	// Project ConfigMap in central cluster
	phase3wg.Add(1)
	go func() {
		defer phase3wg.Done()
		createProjectConfigMap(ctx, clients[cfg.centralCluster], cfg.centralCluster, cfg.namespace, cfg, collectErrorFor(cfg.centralCluster))
	}()

	// Credentials Secret in central cluster
	phase3wg.Add(1)
	go func() {
		defer phase3wg.Done()
		createCredentialsSecret(ctx, clients[cfg.centralCluster], cfg.centralCluster, cfg.namespace, cfg, collectErrorFor(cfg.centralCluster))
	}()

	phase3wg.Wait()

	// Phase 4: Create token secrets + poll until populated (parallel per cluster)
	fmt.Println("Phase 4: Retrieving service account tokens")
	tokens := make(map[string]string)
	var tokensMu sync.Mutex

	runParallel(allClusters, func(cluster string) {
		collectError := collectErrorFor(cluster)
		token, err := getOrCreateTokenSecret(ctx, clients[cluster], cluster, cfg.namespace)
		if err != nil {
			collectError(err, fmt.Sprintf("failed to get token for cluster %s", cluster))
			return
		}
		tokensMu.Lock()
		tokens[cluster] = token
		tokensMu.Unlock()
	})

	// Phase 5: Aggregate tokens, create multi-cluster config secret
	fmt.Println("Phase 5: Creating multi-cluster config secret")
	createMultiClusterConfigSecret(ctx, clients[cfg.testPodCluster], cfg, tokens, collectErrorFor(cfg.testPodCluster))

	// Clean up KIND temp kubeconfig
	if cfg.clusterType == "kind" && kindKubeconfig != "" {
		os.Remove(kindKubeconfig)
	}

	// Phase 6: Extract config files locally (parallel)
	fmt.Println("Phase 6: Extracting config files locally")
	extractConfigFiles(ctx, clients[cfg.testPodCluster], cfg, collectErrorFor("extract"))

	// Phase 7: Copy/build kubectl-mongodb binary
	fmt.Println("Phase 7: Setting up kubectl-mongodb binary")
	if err := setupKubectlMongodb(cfg); err != nil {
		collectErrorFor("kubectl-mongodb")(err, "failed to set up kubectl-mongodb")
	}

	// Report errors
	if len(errorMap) > 0 {
		fmt.Fprintf(os.Stderr, "Errors occurred during multi-cluster preparation:\n")
		for source, errs := range errorMap {
			fmt.Fprintf(os.Stderr, "  %s:\n", source)
			for _, err := range errs {
				fmt.Fprintf(os.Stderr, "    %v\n", err)
			}
		}
		os.Exit(1)
	}

	fmt.Println("Multi-cluster preparation complete")
}

type config struct {
	clusterType           string
	memberClusters        []string
	centralCluster        string
	namespace             string
	kubeconfigPath        string
	omBaseURL             string
	omUser                string
	omAPIKey              string
	omOrgID               string
	multiClusterNoMesh    string
	multiClusterConfigDir string
	kubeconfigCreatorPath string
	projectDir            string
	testPodCluster        string
	localOperator         string
	local                 string
	taskID                string
	applyMTLS             bool
}

func loadConfig() config {
	localVal := os.Getenv("local")               // nolint:forbidigo
	noMesh := os.Getenv("MULTI_CLUSTER_NO_MESH") // nolint:forbidigo
	applyMTLS := localVal == "" && noMesh != "true"

	cfg := config{
		clusterType:           os.Getenv("CLUSTER_TYPE"),                    // nolint:forbidigo
		memberClusters:        strings.Fields(os.Getenv("MEMBER_CLUSTERS")), // nolint:forbidigo
		centralCluster:        os.Getenv("CENTRAL_CLUSTER"),                 // nolint:forbidigo
		namespace:             os.Getenv("NAMESPACE"),                       // nolint:forbidigo
		kubeconfigPath:        os.Getenv("KUBECONFIG"),                      // nolint:forbidigo
		omBaseURL:             os.Getenv("OM_BASE_URL"),                     // nolint:forbidigo
		omUser:                os.Getenv("OM_USER"),                         // nolint:forbidigo
		omAPIKey:              os.Getenv("OM_API_KEY"),                      // nolint:forbidigo
		omOrgID:               os.Getenv("OM_ORGID"),                        // nolint:forbidigo
		multiClusterNoMesh:    noMesh,
		multiClusterConfigDir: os.Getenv("MULTI_CLUSTER_CONFIG_DIR"),               // nolint:forbidigo
		kubeconfigCreatorPath: os.Getenv("MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH"), // nolint:forbidigo
		projectDir:            os.Getenv("PROJECT_DIR"),                            // nolint:forbidigo
		testPodCluster:        os.Getenv("test_pod_cluster"),                       // nolint:forbidigo
		localOperator:         os.Getenv("LOCAL_OPERATOR"),                         // nolint:forbidigo
		local:                 localVal,
		taskID:                os.Getenv("task_id"), // nolint:forbidigo
		applyMTLS:             applyMTLS,
	}

	if cfg.omUser == "" {
		cfg.omUser = "admin"
	}

	// Validate required fields
	if cfg.centralCluster == "" {
		fmt.Fprintln(os.Stderr, "CENTRAL_CLUSTER must be set")
		os.Exit(1)
	}
	if cfg.namespace == "" {
		fmt.Fprintln(os.Stderr, "NAMESPACE must be set")
		os.Exit(1)
	}
	if len(cfg.memberClusters) == 0 {
		fmt.Fprintln(os.Stderr, "MEMBER_CLUSTERS must be set")
		os.Exit(1)
	}

	return cfg
}

func initKubeClient(contextName string) (*kubernetes.Clientset, dynamic.Interface, error) {
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	)

	restConfig, err := kubeconfig.ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get client config for context %s: %v", contextName, err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create client for context %s: %v", contextName, err)
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client for context %s: %v", contextName, err)
	}

	return client, dynamicClient, nil
}

func uniqueClusters(central string, members []string) []string {
	seen := make(map[string]bool)
	var result []string
	seen[central] = true
	result = append(result, central)
	for _, m := range members {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	return result
}

func runParallel(clusters []string, fn func(string)) {
	var wg sync.WaitGroup
	for _, cluster := range clusters {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			fn(c)
		}(cluster)
	}
	wg.Wait()
}

// Phase 1: Override kubeconfig for KIND clusters
func overrideKindKubeconfig(ctx context.Context, cfg config, clients map[string]*kubernetes.Clientset, allClusters []string, collectError func(error, string)) string {
	// Load the kubeconfig file
	kubeconfigBytes, err := os.ReadFile(cfg.kubeconfigPath)
	if err != nil {
		collectError(err, "failed to read kubeconfig")
		return ""
	}

	kubeConfig, err := clientcmd.Load(kubeconfigBytes)
	if err != nil {
		collectError(err, "failed to parse kubeconfig")
		return ""
	}

	// Get ClusterIPs in parallel
	type clusterIP struct {
		cluster string
		ip      string
	}
	ipCh := make(chan clusterIP, len(allClusters))
	var wg sync.WaitGroup

	for _, cluster := range allClusters {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			svc, err := clients[c].CoreV1().Services("default").Get(ctx, kubernetesServiceName, metav1.GetOptions{})
			if err != nil {
				collectError(err, fmt.Sprintf("failed to get kubernetes service in cluster %s", c))
				return
			}
			ipCh <- clusterIP{cluster: c, ip: svc.Spec.ClusterIP}
		}(cluster)
	}
	wg.Wait()
	close(ipCh)

	for cip := range ipCh {
		serverURL := fmt.Sprintf("https://%s", cip.ip)
		fmt.Printf("Overriding api_server for %s with: %s\n", cip.cluster, serverURL)
		if cluster, ok := kubeConfig.Clusters[cip.cluster]; ok {
			cluster.Server = serverURL
		}
	}

	// Write to temp file
	tmpFile, err := os.CreateTemp("", "kind-kubeconfig-*")
	if err != nil {
		collectError(err, "failed to create temp kubeconfig")
		return ""
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	if err := clientcmd.WriteToFile(*kubeConfig, tmpPath); err != nil {
		collectError(err, "failed to write temp kubeconfig")
		return ""
	}

	return tmpPath
}

// Phase 2: Ensure namespace exists with labels and annotations
func ensureNamespace(ctx context.Context, client *kubernetes.Clientset, cluster, namespace, taskID string, collectError func(error, string)) {
	evgTaskURL := "https://evergreen.mongodb.com/task/" + taskID

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"evg": "task",
			},
			Annotations: map[string]string{
				"evg/task": evgTaskURL,
			},
		},
	}

	_, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !kerrors.IsAlreadyExists(err) {
		collectError(err, fmt.Sprintf("failed to create namespace %s in %s", namespace, cluster))
		return
	}

	// Ensure the evg=task label and evg/task annotation exist (in case namespace already existed)
	existing, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		collectError(err, fmt.Sprintf("failed to get namespace %s in %s", namespace, cluster))
		return
	}

	needsUpdate := false

	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	if existing.Labels["evg"] != "task" {
		existing.Labels["evg"] = "task"
		needsUpdate = true
	}

	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	if existing.Annotations["evg/task"] != evgTaskURL {
		existing.Annotations["evg/task"] = evgTaskURL
		needsUpdate = true
	}

	if needsUpdate {
		_, err = client.CoreV1().Namespaces().Update(ctx, existing, metav1.UpdateOptions{})
		collectError(err, fmt.Sprintf("failed to update namespace %s in %s", namespace, cluster))
	}
}

func labelNamespaceIstio(ctx context.Context, client *kubernetes.Clientset, cluster, namespace string, collectError func(error, string)) {
	ns, err := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		collectError(err, fmt.Sprintf("failed to get namespace %s in %s for istio label", namespace, cluster))
		return
	}
	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}
	if ns.Labels["istio-injection"] != "enabled" {
		ns.Labels["istio-injection"] = "enabled"
		_, err = client.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
		collectError(err, fmt.Sprintf("failed to label namespace %s with istio-injection in %s", namespace, cluster))
	}
}

// applyPeerAuthentication ensures a PeerAuthentication resource exists with STRICT mTLS
// using the dynamic client, since PeerAuthentication is an Istio CRD with no typed Go client.
func applyPeerAuthentication(ctx context.Context, dynClient dynamic.Interface, cluster, namespace string, collectError func(error, string)) {
	peerAuthGVR := schema.GroupVersionResource{
		Group:    "security.istio.io",
		Version:  "v1beta1",
		Resource: "peerauthentications",
	}

	desired := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "security.istio.io/v1beta1",
			"kind":       "PeerAuthentication",
			"metadata": map[string]interface{}{
				"name":      "default",
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"mtls": map[string]interface{}{
					"mode": "STRICT",
				},
			},
		},
	}

	existing, err := dynClient.Resource(peerAuthGVR).Namespace(namespace).Get(ctx, "default", metav1.GetOptions{})
	if kerrors.IsNotFound(err) {
		_, err = dynClient.Resource(peerAuthGVR).Namespace(namespace).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			collectError(err, fmt.Sprintf("failed to create PeerAuthentication in %s", cluster))
		}
		return
	}
	if err != nil {
		collectError(err, fmt.Sprintf("failed to get PeerAuthentication in %s", cluster))
		return
	}

	// Update spec to ensure STRICT mTLS
	existing.Object["spec"] = desired.Object["spec"]
	_, err = dynClient.Resource(peerAuthGVR).Namespace(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	collectError(err, fmt.Sprintf("failed to update PeerAuthentication in %s", cluster))
}

// Phase 3: Create ServiceAccount
func ensureServiceAccount(ctx context.Context, client *kubernetes.Clientset, cluster, namespace string, collectError func(error, string)) {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: namespace,
		},
		ImagePullSecrets: []corev1.LocalObjectReference{
			{Name: imageRegistriesSecret},
		},
	}

	existing, err := client.CoreV1().ServiceAccounts(namespace).Get(ctx, serviceAccountName, metav1.GetOptions{})
	if kerrors.IsNotFound(err) {
		_, err = client.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			collectError(err, fmt.Sprintf("failed to create SA in %s", cluster))
		}
		return
	}
	if err != nil {
		collectError(err, fmt.Sprintf("failed to get SA in %s", cluster))
		return
	}

	// Update if needed
	existing.ImagePullSecrets = sa.ImagePullSecrets
	_, err = client.CoreV1().ServiceAccounts(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	collectError(err, fmt.Sprintf("failed to update SA in %s", cluster))
}

// Phase 3: Create ClusterRoleBinding
func ensureClusterRoleBinding(ctx context.Context, client *kubernetes.Clientset, cluster, namespace string, collectError func(error, string)) {
	crbName := fmt.Sprintf("operator-multi-cluster-tests-role-binding-%s", namespace)

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: crbName,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      serviceAccountName,
				Namespace: namespace,
			},
		},
	}

	existing, err := client.RbacV1().ClusterRoleBindings().Get(ctx, crbName, metav1.GetOptions{})
	if kerrors.IsNotFound(err) {
		_, err = client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			collectError(err, fmt.Sprintf("failed to create CRB in %s", cluster))
		}
		return
	}
	if err != nil {
		collectError(err, fmt.Sprintf("failed to get CRB in %s", cluster))
		return
	}

	// Not updating RoleRef here, it is immutable on ClusterRoleBindings.
	existing.Subjects = crb.Subjects
	_, err = client.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
	collectError(err, fmt.Sprintf("failed to update CRB in %s", cluster))
}

// Phase 3: Create kubeconfig secret for test pod
func createKubeconfigSecret(ctx context.Context, client *kubernetes.Clientset, cluster, namespace, kubeconfigPath string, collectError func(error, string)) {
	secretName := kubeconfigSecretName

	// Delete existing
	_ = client.CoreV1().Secrets(namespace).Delete(ctx, secretName, metav1.DeleteOptions{})

	kubeconfigData, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		collectError(err, "failed to read kubeconfig for secret")
		return
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"kubeconfig": kubeconfigData,
		},
	}

	_, err = client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		collectError(err, fmt.Sprintf("failed to create kubeconfig secret in %s", cluster))
	}
}

// Phase 3: Create project ConfigMap
func createProjectConfigMap(ctx context.Context, client *kubernetes.Clientset, cluster, namespace string, cfg config, collectError func(error, string)) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projectConfigMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"projectName": namespace,
			"baseUrl":     cfg.omBaseURL,
			"orgId":       cfg.omOrgID,
		},
	}

	existing, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, projectConfigMapName, metav1.GetOptions{})
	if kerrors.IsNotFound(err) {
		_, err = client.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			collectError(err, fmt.Sprintf("failed to create project ConfigMap in %s", cluster))
		}
		return
	}
	if err != nil {
		collectError(err, fmt.Sprintf("failed to get project ConfigMap in %s", cluster))
		return
	}

	existing.Data = cm.Data
	_, err = client.CoreV1().ConfigMaps(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	collectError(err, fmt.Sprintf("failed to update project ConfigMap in %s", cluster))
}

// Phase 3: Create credentials Secret
func createCredentialsSecret(ctx context.Context, client *kubernetes.Clientset, cluster, namespace string, cfg config, collectError func(error, string)) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialsSecretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"user":         cfg.omUser,
			"publicApiKey": cfg.omAPIKey,
		},
	}

	existing, err := client.CoreV1().Secrets(namespace).Get(ctx, credentialsSecretName, metav1.GetOptions{})
	if kerrors.IsNotFound(err) {
		_, err = client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			collectError(err, fmt.Sprintf("failed to create credentials secret in %s", cluster))
		}
		return
	}
	if err != nil {
		collectError(err, fmt.Sprintf("failed to get credentials secret in %s", cluster))
		return
	}

	existing.StringData = secret.StringData
	_, err = client.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	collectError(err, fmt.Sprintf("failed to update credentials secret in %s", cluster))
}

// Phase 4: Get or create token secret and wait for it to be populated
func getOrCreateTokenSecret(ctx context.Context, client *kubernetes.Clientset, cluster, namespace string) (string, error) {
	// Check if a token secret already exists for the service account
	secretName, err := findExistingTokenSecret(ctx, client, namespace)
	if err != nil {
		return "", err
	}

	if secretName == "" {
		// Create a token secret
		secretName = serviceAccountName + tokenSecretSuffix
		tokenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Annotations: map[string]string{
					"kubernetes.io/service-account.name": serviceAccountName,
				},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		}

		_, err = client.CoreV1().Secrets(namespace).Create(ctx, tokenSecret, metav1.CreateOptions{})
		if err != nil && !kerrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("failed to create token secret in %s: %v", cluster, err)
		}
	}

	// Poll until token is populated
	var token string
	err = wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return false, nil // retry on error
		}
		tokenBytes, ok := secret.Data["token"]
		if !ok || len(tokenBytes) == 0 {
			return false, nil
		}
		token = string(tokenBytes)
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("token not populated for secret %s in cluster %s: %v", secretName, cluster, err)
	}

	return token, nil
}

func findExistingTokenSecret(ctx context.Context, client *kubernetes.Clientset, namespace string) (string, error) {
	secrets, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list secrets: %v", err)
	}

	for _, s := range secrets.Items {
		if s.Type == corev1.SecretTypeServiceAccountToken {
			if saName, ok := s.Annotations["kubernetes.io/service-account.name"]; ok && saName == serviceAccountName {
				return s.Name, nil
			}
		}
	}
	return "", nil
}

// Phase 5: Create multi-cluster config secret
func createMultiClusterConfigSecret(ctx context.Context, client *kubernetes.Clientset, cfg config, tokens map[string]string, collectError func(error, string)) {
	secretName := multiClusterSecretName

	// Delete existing
	_ = client.CoreV1().Secrets(cfg.namespace).Delete(ctx, secretName, metav1.DeleteOptions{})

	data := map[string][]byte{
		"central_cluster": []byte(cfg.centralCluster),
	}

	// Add central cluster token
	if token, ok := tokens[cfg.centralCluster]; ok {
		data[cfg.centralCluster] = []byte(token)
	}

	// Add member cluster tokens and indices
	for idx, member := range cfg.memberClusters {
		if token, ok := tokens[member]; ok {
			// Only add if not same as central (avoid duplicate key)
			if member != cfg.centralCluster {
				data[member] = []byte(token)
			}
		}
		data[fmt.Sprintf("member_cluster_%d", idx+1)] = []byte(member)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cfg.namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	_, err := client.CoreV1().Secrets(cfg.namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		collectError(err, "failed to create multi-cluster config secret")
	}
}

// Phase 6: Extract config files from the multi-cluster config secret
func extractConfigFiles(ctx context.Context, client *kubernetes.Clientset, cfg config, collectError func(error, string)) {
	if cfg.multiClusterConfigDir == "" {
		return
	}

	if err := os.MkdirAll(cfg.multiClusterConfigDir, 0755); err != nil {
		collectError(err, "failed to create config dir")
		return
	}

	secretName := multiClusterSecretName
	secret, err := client.CoreV1().Secrets(cfg.namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		collectError(err, "failed to get multi-cluster config secret for extraction")
		return
	}

	// Write central_cluster name
	writeConfigFile(cfg.multiClusterConfigDir, "central_cluster", secret.Data["central_cluster"], collectError)

	// Write central cluster token
	writeConfigFile(cfg.multiClusterConfigDir, cfg.centralCluster, secret.Data[cfg.centralCluster], collectError)

	// Write member cluster data
	for idx, member := range cfg.memberClusters {
		memberKey := fmt.Sprintf("member_cluster_%d", idx+1)
		writeConfigFile(cfg.multiClusterConfigDir, memberKey, secret.Data[memberKey], collectError)
		writeConfigFile(cfg.multiClusterConfigDir, member, secret.Data[member], collectError)
	}
}

func writeConfigFile(dir, name string, data []byte, collectError func(error, string)) {
	if data == nil {
		return
	}

	// The typed API returns already-decoded data, no base64 step needed.
	filePath := filepath.Join(dir, name)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		collectError(err, fmt.Sprintf("failed to write config file %s", filePath))
	}
}

// Phase 7: Copy or build kubectl-mongodb binary
func setupKubectlMongodb(cfg config) error {
	if cfg.kubeconfigCreatorPath == "" {
		return nil
	}

	// Check for pre-compiled binary
	precompiled := filepath.Join(cfg.projectDir, "bin", "kubectl-mongodb")
	if _, err := os.Stat(precompiled); err == nil {
		fmt.Printf("Copying pre-compiled kubectl-mongodb from %s\n", precompiled)
		data, err := os.ReadFile(precompiled)
		if err != nil {
			return fmt.Errorf("failed to read pre-compiled binary: %v", err)
		}
		return os.WriteFile(cfg.kubeconfigCreatorPath, data, 0755)
	}

	// Build from source
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	fmt.Printf("Building kubectl-mongodb (GOOS=%s GOARCH=%s)\n", goos, goarch)
	cmdDir := filepath.Join(cfg.projectDir, "cmd", "kubectl-mongodb")

	env := os.Environ()
	if goroot := os.Getenv("GOROOT"); goroot != "" && goos == "linux" {
		env = append(env, fmt.Sprintf("PATH=%s:%s", filepath.Join(goroot, "bin"), os.Getenv("PATH")))
	}
	env = append(env, fmt.Sprintf("GOOS=%s", goos), fmt.Sprintf("GOARCH=%s", goarch))

	cmd := exec.Command("go", "build", "-o", cfg.kubeconfigCreatorPath, "main.go")
	cmd.Dir = cmdDir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
