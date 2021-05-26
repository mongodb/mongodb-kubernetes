package main

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

var (
	// TODO: make this read from secret or serviceaccount and not kubeconfig
	kcfg1 = "./configs/cluster1"
	kcfg2 = "./configs/cluster2"
)

type secretMirrorReconciler struct {
	referenceClusterClient, mirrorClusterClient client.Client
}

func (rr *secretMirrorReconciler) Reconcile(ctx context.Context, r reconcile.Request) (reconcile.Result, error) {
	s := &corev1.Secret{}
	if err := rr.referenceClusterClient.Get(context.TODO(), r.NamespacedName, s); err != nil {
		// if errors.IsNotFound {
		// 	return reconcile.Result{}, nil
		// }
		return reconcile.Result{}, err
	}

	if err := rr.mirrorClusterClient.Get(context.TODO(), r.NamespacedName, &corev1.Secret{}); err != nil {
		// if !errors.IsNotFound(err) {
		// 	return reconcile.Result{}, err
		// }

		mirrorSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: s.Name},
			Data:       s.Data,
		}
		err := rr.mirrorClusterClient.Create(context.TODO(), mirrorSecret)
		if err != nil {
			fmt.Printf("failed to create secret in mirror cluster: %v\n", err)
			return reconcile.Result{}, err
		}

		return reconcile.Result{}, nil
	}

	fmt.Println("sucessfully created secret in mirror cluster")
	return reconcile.Result{}, nil
}

func NewSecretMirrorReconciler(mgr manager.Manager, mirrorCluster cluster.Cluster) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Watch Secrets in the reference cluster
		For(&corev1.Secret{}).
		// Watch Secrets in the mirror cluster
		Watches(
			source.NewKindWithCache(&corev1.Secret{}, mirrorCluster.GetCache()),
			&handler.EnqueueRequestForObject{},
		).
		Complete(&secretMirrorReconciler{
			referenceClusterClient: mgr.GetClient(),
			mirrorClusterClient:    mirrorCluster.GetClient(),
		})

}

func main() {
	cfg1, err := clientcmd.BuildConfigFromFlags("", kcfg1)
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created client for cluster1")

	mgr, err := manager.New(cfg1, manager.Options{})
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully manager client for cluster1")

	cfg2, err := clientcmd.BuildConfigFromFlags("", kcfg2)
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created client for cluster2")

	mirrorCluster, err := cluster.New(cfg2)
	if err != nil {
		panic(err)
	}
	fmt.Println("successfully created mirror cluster")

	if err := mgr.Add(mirrorCluster); err != nil {
		panic(err)
	}

	if err := NewSecretMirrorReconciler(mgr, mirrorCluster); err != nil {
		panic(err)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		panic(err)
	}

}
