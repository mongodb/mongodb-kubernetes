package main

import (
	"context"
	"fmt"
	"k8s.io/client-go/rest"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type podListingReconciler struct {
	referenceClusterClient, otherCluster, otherCluster2 client.Client
}

func (p podListingReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	fmt.Printf("Reconciling on pod %s/%s\n", request.Name, request.Namespace)

	podInthisCluster := corev1.Pod{}
	if err := p.referenceClusterClient.Get(ctx, types.NamespacedName{Name: request.Name, Namespace: request.Namespace}, &podInthisCluster); err != nil {
		if !errors.IsNotFound(err) && err != nil {
			return reconcile.Result{}, err
		}
	}

	if podInthisCluster.Name != "" {
		fmt.Printf("Pod in this cluster!\n")
		fmt.Printf("Found Pod: %s in namespace %s!\n", podInthisCluster.Name, podInthisCluster.Namespace)
	}

	pod := corev1.Pod{}
	if err := p.otherCluster.Get(ctx, types.NamespacedName{Name: request.Name, Namespace: request.Namespace}, &pod); err != nil {
		if !errors.IsNotFound(err) && err != nil {
			return reconcile.Result{}, err
		}
	}

	if pod.Name != "" {
		fmt.Printf("Pod in cluster 1!\n")
		fmt.Printf("Found Pod: %s in namespace %s!\n", pod.Name, pod.Namespace)
	}

	pod2 := corev1.Pod{}
	if err := p.otherCluster2.Get(ctx, types.NamespacedName{Name: request.Name, Namespace: request.Namespace}, &pod2); err != nil {
		if !errors.IsNotFound(err) && err != nil {
			fmt.Printf("Error getting pod in sample namespace: %s this is expected\n", err)
			return reconcile.Result{}, err
		}
	}

	if pod2.Name != "" {
		fmt.Printf("Pod in cluster 2!\n")
		fmt.Printf("Found Pod: %s in namespace %s!\n", pod2.Name, pod2.Namespace)
	}

	return reconcile.Result{}, nil
}

func NewPodListingReconciler(mgr manager.Manager, mirrorCluster, mirrorCluster1 cluster.Cluster) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Watch Pods in the reference cluster
		For(&corev1.Pod{}).
		// Watch pods in the mirror cluster
		Watches(
			source.NewKindWithCache(&corev1.Pod{}, mirrorCluster.GetCache()),
			&handler.EnqueueRequestForObject{},
		).
		Watches(
			source.NewKindWithCache(&corev1.Pod{}, mirrorCluster1.GetCache()),
			&handler.EnqueueRequestForObject{},
		).
		Complete(&podListingReconciler{
			otherCluster:           mirrorCluster.GetClient(),
			otherCluster2:          mirrorCluster1.GetClient(),
			referenceClusterClient: mgr.GetClient(),
		})

}

func newConfig(context, kubeConfigPath string) (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		}).ClientConfig()
}

func main() {
	cfg1, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created client for central cluster")

	mgr, err := manager.New(cfg1, manager.Options{})
	if err != nil {
		panic(err)
	}

	fmt.Println("successfully manager client for central cluster")

	cfg2, err := newConfig("e2e.cluster1.mongokubernetes.com", "/etc/config/kubeconfig/kubeconfig")
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created client for cluster1")

	cfg3, err := newConfig("e2e.cluster2.mongokubernetes.com", "/etc/config/kubeconfig/kubeconfig")
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created client for cluster2")

	mirrorCluster, err := cluster.New(cfg2)
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created mirror cluster 1")

	mirrorCluster1, err := cluster.New(cfg3)
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created mirror cluster 2")

	if err := mgr.Add(mirrorCluster); err != nil {
		panic(err)
	}

	if err := mgr.Add(mirrorCluster1); err != nil {
		panic(err)
	}

	if err := NewPodListingReconciler(mgr, mirrorCluster, mirrorCluster1); err != nil {
		panic(err)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		panic(err)
	}
}
