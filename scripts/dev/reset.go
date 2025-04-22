package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

type DynamicResource struct {
	GVR          schema.GroupVersionResource
	ResourceName string
}

var (
	zero                 int64 = 0
	deleteOptionsNoGrace       = v1.DeleteOptions{
		GracePeriodSeconds: &zero,
	}
)

// waitForBackupPodDeletion waits for the backup daemon pod to be deleted
func waitForBackupPodDeletion(kubeClient *kubernetes.Clientset, namespace string) error {
	podName := "backup-daemon-0"
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Wait until pods named "backup-daemon-0" are deleted
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctxWithTimeout.Done():
			fmt.Println("Warning: failed to remove backup daemon statefulset")
			return ctxWithTimeout.Err() // error will be context.DeadlineExceeded

		case <-ticker.C:
			// Periodically check if the pod is deleted
			_, err := kubeClient.CoreV1().Pods(namespace).Get(ctxWithTimeout, podName, v1.GetOptions{})
			if kerrors.IsNotFound(err) {
				// Pod has been deleted
				return nil
			} else if err != nil {
				return err
			}
		}
	}
}

// deleteDynamicResources deletes a list of dynamic resources
func deleteDynamicResources(ctx context.Context, dynamicClient dynamic.Interface, namespace string, resources []DynamicResource, collectError func(error, string)) {
	for _, resource := range resources {
		err := dynamicClient.Resource(resource.GVR).Namespace(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
		collectError(err, fmt.Sprintf("failed to delete %s", resource.ResourceName))
	}
}

// deleteCRDs deletes a list of CustomResourceDefinitions
func deleteCRDs(ctx context.Context, dynamicClient dynamic.Interface, crdNames []string, collectError func(error, string)) {
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	for _, crdName := range crdNames {
		err := dynamicClient.Resource(crdGVR).Delete(ctx, crdName, deleteOptionsNoGrace)
		collectError(err, fmt.Sprintf("failed to delete CRD %s", crdName))
	}
}

// deleteRolesAndBindings deletes roles and rolebindings containing 'mongodb' in their names
func deleteRolesAndBindings(ctx context.Context, kubeClient *kubernetes.Clientset, namespace string, collectError func(error, string)) {
	// Delete roles
	roleList, err := kubeClient.RbacV1().Roles(namespace).List(ctx, v1.ListOptions{})
	collectError(err, "failed to list roles")
	if err == nil {
		for _, role := range roleList.Items {
			if strings.Contains(role.Name, "mongodb") {
				err = kubeClient.RbacV1().Roles(namespace).Delete(ctx, role.Name, deleteOptionsNoGrace)
				collectError(err, fmt.Sprintf("failed to delete role %s", role.Name))
			}
		}
	}

	// Delete rolebindings
	rbList, err := kubeClient.RbacV1().RoleBindings(namespace).List(ctx, v1.ListOptions{})
	collectError(err, "failed to list rolebindings")
	if err == nil {
		for _, rb := range rbList.Items {
			if strings.Contains(rb.Name, "mongodb") {
				err = kubeClient.RbacV1().RoleBindings(namespace).Delete(ctx, rb.Name, deleteOptionsNoGrace)
				collectError(err, fmt.Sprintf("failed to delete rolebinding %s", rb.Name))
			}
		}
	}
}

// resetContext deletes cluster-scoped resources in the given context
func resetContext(ctx context.Context, contextName string, deleteCRD bool, collectError func(error, string)) {
	fmt.Printf("Resetting context %s\n", contextName)

	kubeClient, _, err := initKubeClient(contextName)
	if err != nil {
		collectError(err, fmt.Sprintf("failed to initialize Kubernetes client for context %s", contextName))
		return
	}

	// Delete ClusterRoleBindings with names containing "mongodb"
	crbList, err := kubeClient.RbacV1().ClusterRoleBindings().List(ctx, v1.ListOptions{})
	collectError(err, fmt.Sprintf("failed to list ClusterRoleBindings in context %s", contextName))
	if err == nil {
		for _, crb := range crbList.Items {
			if strings.Contains(crb.Name, "mongodb") {
				err = kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, crb.Name, deleteOptionsNoGrace)
				collectError(err, fmt.Sprintf("failed to delete ClusterRoleBinding %s in context %s", crb.Name, contextName))
			}
		}
	}

	// Delete ClusterRoles with names containing "mongodb"
	crList, err := kubeClient.RbacV1().ClusterRoles().List(ctx, v1.ListOptions{})
	collectError(err, fmt.Sprintf("failed to list ClusterRoles in context %s", contextName))
	if err == nil {
		for _, cr := range crList.Items {
			if strings.Contains(cr.Name, "mongodb") {
				err = kubeClient.RbacV1().ClusterRoles().Delete(ctx, cr.Name, deleteOptionsNoGrace)
				collectError(err, fmt.Sprintf("failed to delete ClusterRole %s in context %s", cr.Name, contextName))
			}
		}
	}

	clusterNamespaces, err := getTestNamespaces(ctx, kubeClient, contextName)
	if err != nil {
		collectError(err, fmt.Sprintf("failed to list TestNamespaces in context %s", contextName))
		return
	}
	if len(clusterNamespaces) == 0 {
		// This env variable is used for single cluster tests
		namespace := os.Getenv("NAMESPACE") // nolint:forbidigo
		clusterNamespaces = append(clusterNamespaces, namespace)
	}
	fmt.Printf("%s: resetting namespaces: %v\n", contextName, clusterNamespaces)
	for _, ns := range clusterNamespaces {
		resetNamespace(ctx, contextName, ns, deleteCRD, collectError)
	}

	fmt.Printf("Finished resetting context %s\n", contextName)
}

// resetNamespace cleans up the namespace in the given context
func resetNamespace(ctx context.Context, contextName string, namespace string, deleteCRD bool, collectError func(error, string)) {
	kubeClient, dynamicClient, err := initKubeClient(contextName)
	if err != nil {
		collectError(err, fmt.Sprintf("failed to initialize Kubernetes client for context %s", contextName))
		return
	}

	// Hack: remove the statefulset for backup daemon first - otherwise it may get stuck on removal if AppDB is removed first
	stsList, err := kubeClient.AppsV1().StatefulSets(namespace).List(ctx, v1.ListOptions{})
	collectError(err, "failed to list statefulsets")
	if err == nil {
		for _, sts := range stsList.Items {
			if strings.Contains(sts.Name, "backup-daemon") {
				err = kubeClient.AppsV1().StatefulSets(namespace).Delete(ctx, sts.Name, deleteOptionsNoGrace)
				collectError(err, fmt.Sprintf("failed to delete statefulset %s", sts.Name))
			}
		}
	}

	err = waitForBackupPodDeletion(kubeClient, namespace)
	collectError(err, "failed to delete backup daemon pod")

	// Delete all statefulsets
	err = kubeClient.AppsV1().StatefulSets(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
	collectError(err, "failed to delete statefulsets")

	// Delete all pods
	err = kubeClient.CoreV1().Pods(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
	collectError(err, "failed to delete pods")

	// Delete all deployments
	err = kubeClient.AppsV1().Deployments(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
	collectError(err, "failed to delete deployments")

	// Delete all services
	services, err := kubeClient.CoreV1().Services(namespace).List(ctx, v1.ListOptions{})
	collectError(err, "failed to list services")
	if err == nil {
		for _, service := range services.Items {
			err = kubeClient.CoreV1().Services(namespace).Delete(ctx, service.Name, deleteOptionsNoGrace)
			collectError(err, fmt.Sprintf("failed to delete service %s", service.Name))
		}
	}

	// Delete opsmanager resources
	opsManagerGVR := schema.GroupVersionResource{
		Group:    "mongodb.com",
		Version:  "v1",
		Resource: "opsmanagers",
	}
	err = dynamicClient.Resource(opsManagerGVR).Namespace(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
	collectError(err, "failed to delete opsmanager resources")

	// Delete CSRs matching the namespace
	csrList, err := kubeClient.CertificatesV1().CertificateSigningRequests().List(ctx, v1.ListOptions{})
	collectError(err, "failed to list CSRs")
	if err == nil {
		for _, csr := range csrList.Items {
			if strings.Contains(csr.Name, namespace) {
				err = kubeClient.CertificatesV1().CertificateSigningRequests().Delete(ctx, csr.Name, deleteOptionsNoGrace)
				collectError(err, fmt.Sprintf("failed to delete CSR %s", csr.Name))
			}
		}
	}

	// Delete secrets
	err = kubeClient.CoreV1().Secrets(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
	collectError(err, "failed to delete secrets")

	// Delete configmaps
	err = kubeClient.CoreV1().ConfigMaps(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
	collectError(err, "failed to delete configmaps")

	// Delete validating webhook configuration
	err = kubeClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().Delete(ctx, "mdbpolicy.mongodb.com", deleteOptionsNoGrace)
	collectError(err, "failed to delete validating webhook configuration")

	// Define dynamic resources to delete
	dynamicResources := []DynamicResource{
		{
			GVR: schema.GroupVersionResource{
				Group:    "cert-manager.io",
				Version:  "v1",
				Resource: "certificates",
			},
			ResourceName: "certificates",
		},
		{
			GVR: schema.GroupVersionResource{
				Group:    "cert-manager.io",
				Version:  "v1",
				Resource: "issuers",
			},
			ResourceName: "issuers",
		},
		{
			GVR: schema.GroupVersionResource{
				Group:    "operators.coreos.com",
				Version:  "v1alpha1",
				Resource: "catalogsources",
			},
			ResourceName: "catalogsources",
		},
		{
			GVR: schema.GroupVersionResource{
				Group:    "operators.coreos.com",
				Version:  "v1alpha1",
				Resource: "subscriptions",
			},
			ResourceName: "subscriptions",
		},
		{
			GVR: schema.GroupVersionResource{
				Group:    "operators.coreos.com",
				Version:  "v1alpha1",
				Resource: "clusterserviceversions",
			},
			ResourceName: "clusterserviceversions",
		},
	}

	// Delete dynamic resources
	deleteDynamicResources(ctx, dynamicClient, namespace, dynamicResources, collectError)

	// Delete PVCs
	err = kubeClient.CoreV1().PersistentVolumeClaims(namespace).DeleteCollection(ctx, deleteOptionsNoGrace, v1.ListOptions{})
	collectError(err, "failed to delete PVCs")

	// Delete CRDs if specified
	if deleteCRD {
		crdNames := []string{
			"mongodb.mongodb.com",
			"mongodbcommunity.mongodbcommunity.mongodb.com",
			"mongodbmulti.mongodb.com",
			"mongodbmulticluster.mongodb.com",
			"mongodbusers.mongodb.com",
			"opsmanagers.mongodb.com",
		}
		deleteCRDs(ctx, dynamicClient, crdNames, collectError)
	}

	// Delete serviceaccounts excluding 'default'
	saList, err := kubeClient.CoreV1().ServiceAccounts(namespace).List(ctx, v1.ListOptions{})
	collectError(err, "failed to list serviceaccounts")
	if err == nil {
		for _, sa := range saList.Items {
			if sa.Name != "default" {
				err = kubeClient.CoreV1().ServiceAccounts(namespace).Delete(ctx, sa.Name, deleteOptionsNoGrace)
				collectError(err, fmt.Sprintf("failed to delete serviceaccount %s", sa.Name))
			}
		}
	}

	// Delete roles and rolebindings
	deleteRolesAndBindings(ctx, kubeClient, namespace, collectError)

	fmt.Printf("Finished resetting namespace %s in context %s\n", namespace, contextName)
}

// Replaces our get_test_namespaces bash function, get the list of namespaces with evergreen label selector
func getTestNamespaces(ctx context.Context, kubeClient *kubernetes.Clientset, contextName string) ([]string, error) {
	labelSelector := "evg=task"

	namespaces, err := kubeClient.CoreV1().Namespaces().List(ctx, v1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces for context %s: %v", contextName, err)
	}

	var namespaceNames []string
	for _, ns := range namespaces.Items {
		namespaceNames = append(namespaceNames, ns.Name)
	}

	return namespaceNames, nil
}

func main() {
	ctx := context.Background()

	kubeEnvNameVar := "KUBE_ENVIRONMENT_NAME"
	kubeEnvironmentName, found := os.LookupEnv(kubeEnvNameVar) // nolint:forbidigo
	if !found {
		fmt.Println(kubeEnvNameVar, "must be set. Make sure you sourced your env file")
		os.Exit(1)
	}

	deleteCRD := env.ReadOrDefault("DELETE_CRD", "true") == "true" // nolint:forbidigo

	// Cluster is a set because central cluster can be part of member clusters
	clusters := make(map[string]bool)
	if kubeEnvironmentName == "multi" {
		memberClusters := strings.Fields(os.Getenv("MEMBER_CLUSTERS")) // nolint:forbidigo
		for _, cluster := range memberClusters {
			clusters[cluster] = true
		}
		centralClusterName := os.Getenv("CENTRAL_CLUSTER") // nolint:forbidigo
		clusters[centralClusterName] = true
	} else {
		clusterName := os.Getenv("CLUSTER_NAME") // nolint:forbidigo
		clusters[clusterName] = true
	}

	fmt.Println("Resetting clusters:")
	fmt.Println(clusters)

	// For each call to resetContext, we collect errors in a slice, and display them at the end
	errorMap := make(map[string][]error)
	var errorMapMu sync.Mutex // Secure concurrent access to the map
	var wg sync.WaitGroup

	for cluster := range clusters {
		wg.Add(1)
		go func(cluster string) {
			defer wg.Done()
			var localErrs []error
			collectError := func(err error, msg string) {
				// Ignore any "not found" error
				if err != nil && !kerrors.IsNotFound(err) {
					localErrs = append(localErrs, fmt.Errorf("%s: %v", msg, err))
				}
			}
			resetContext(ctx, cluster, deleteCRD, collectError)
			errorMapMu.Lock()
			if len(localErrs) > 0 {
				errorMap[cluster] = localErrs
			}
			errorMapMu.Unlock()
		}(cluster)
	}

	wg.Wait()

	// Print out errors for each cluster
	if len(errorMap) > 0 {
		fmt.Fprintf(os.Stderr, "Errors occurred during reset:\n")
		for cluster, errs := range errorMap {
			fmt.Fprintf(os.Stderr, "Cluster %s:\n", cluster)
			for _, err := range errs {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
			}
		}
		os.Exit(1)
	}

	fmt.Println("Done")
}

func initKubeClient(contextName string) (*kubernetes.Clientset, dynamic.Interface, error) {
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	)

	config, err := kubeconfig.ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get Kubernetes client config for context %s: %v", contextName, err)
	}

	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Kubernetes client for context %s: %v", contextName, err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client for context %s: %v", contextName, err)
	}

	return kubeClient, dynamicClient, nil
}
