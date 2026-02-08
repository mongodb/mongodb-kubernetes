package main

import (
	"context"
	"fmt"
	restclient "k8s.io/client-go/rest"
	"os"
	"path/filepath"

	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"golang.org/x/xerrors"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func LoadKubeConfigFilePath() string {
	env := os.Getenv("KUBECONFIG")
	if env != "" {
		return env
	}
	return filepath.Join(homedir.HomeDir(), ".kube", "config")
}

func createClusterMap(clusterNames []string, kubeConfigPath string) (map[string]client.Client, map[string]*restclient.Config, error) {
	clusterMap := map[string]client.Client{}
	configMap := map[string]*restclient.Config{}

	clusterClientMap, err := multicluster.CreateMemberClusterClients(clusterNames, kubeConfigPath)
	if err != nil {
		return nil, nil, xerrors.Errorf("failed to create k8s client from %s: %w", kubeConfigPath, err)
	}

	for memberClusterName, restConfig := range clusterClientMap {
		clientObj, err := client.New(restConfig, client.Options{
			Scheme: CurrentScheme(),
		})
		if err != nil {
			return nil, nil, xerrors.Errorf("failed to create k8s cluster object from %s for context %s: %w", kubeConfigPath, memberClusterName, err)
		}
		clusterMap[memberClusterName] = clientObj
		configMap[memberClusterName] = restConfig
	}

	return clusterMap, configMap, nil
}

func createOperatorClusterMapFromInClusterConfig(operatorClusterName string, inClusterConfig *restclient.Config) (map[string]client.Client, map[string]*restclient.Config, error) {
	clusterMap := map[string]client.Client{}
	configMap := map[string]*restclient.Config{}

	clientObj, err := client.New(inClusterConfig, client.Options{
		Scheme: CurrentScheme(),
	})
	if err != nil {
		return nil, nil, xerrors.Errorf("failed to create in-cluster k8s client: %w", err)
	}

	clusterMap[operatorClusterName] = clientObj
	configMap[operatorClusterName] = inClusterConfig

	return clusterMap, configMap, nil
}

func createServiceAccountAndRoles(ctx context.Context, kubeClient client.Client, namespace string) error {
	sa := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mdb-debug-sa-cluster-admin",
			Namespace: namespace,
			Labels:    mdbDebugLabels,
		},
		ImagePullSecrets: []corev1.LocalObjectReference{
			{Name: "image-registries-secret"},
		},
	}

	if err := kubeClient.Create(ctx, &sa); err != nil {
		if !errors.IsAlreadyExists(err) {
			return xerrors.Errorf("error creating service account: %w", err)
		}
	}

	roleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("mdb-debug-cluster-admin-%s", namespace),
			Labels: mdbDebugLabels,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "mdb-debug-sa-cluster-admin",
				Namespace: sa.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if err := kubeClient.Create(ctx, &roleBinding); err != nil {
		if !errors.IsAlreadyExists(err) {
			return xerrors.Errorf("error creating role binding: %w", err)
		}
	}

	return nil
}
