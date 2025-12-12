package operator

import (
	"context"
	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"

	mdbcv1 "github.com/mongodb/mongodb-kubernetes/api/v1/certificate"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

type MongoDBCertificateReconciler struct {
	*ReconcileCommonController
	memberClusterClientsMap       map[string]kubernetesClient.Client
	memberClusterSecretClientsMap map[string]secrets.SecretClient
}

func newMongoDBCertificateReconciler(ctx context.Context, kubeClient client.Client, memberClustersMap map[string]client.Client) *MongoDBCertificateReconciler {
	clientsMap := make(map[string]kubernetesClient.Client)
	secretClientsMap := make(map[string]secrets.SecretClient)

	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
		secretClientsMap[k] = secrets.SecretClient{
			VaultClient: nil,
			KubeClient:  clientsMap[k],
		}
	}
	return &MongoDBCertificateReconciler{
		ReconcileCommonController:     NewReconcileCommonController(ctx, kubeClient),
		memberClusterClientsMap:       clientsMap,
		memberClusterSecretClientsMap: secretClientsMap,
	}
}

func AddMongoDBCertificateController(ctx context.Context, mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	reconciler := newMongoDBCertificateReconciler(ctx, mgr.GetClient(), multicluster.ClustersMapToClientMap(memberClustersMap))
	c, err := controller.New(util.MongoDbCertificateController, mgr, controller.Options{Reconciler: reconciler, MaxConcurrentReconciles: env.ReadIntOrDefault(util.MaxConcurrentReconcilesEnv, 1)}) // nolint:forbidigo
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.ConfigMap{},
		&watch.ResourcesHandler{ResourceType: watch.ConfigMap, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{},
		&watch.ResourcesHandler{ResourceType: watch.Secret, ResourceWatcher: reconciler.resourceWatcher}))
	if err != nil {
		return err
	}

	// watch for changes to MongoDBUser resources
	eventHandler := MongoDBCertificateEventHandler{reconciler: reconciler}
	err = c.Watch(source.Kind[client.Object](mgr.GetCache(), &mdbcv1.MongoDBCertificate{}, &eventHandler))
	if err != nil {
		return err
	}

	zap.S().Infof("Registered controller %s", util.MongoDbCertificateController)
	return nil
}

// +kubebuilder:rbac:groups=mongodb.com,resources={mongodbcertificates,mongodbcertificates/status,mongodbcertificates/finalizers},verbs=*,namespace=placeholder
func (r *MongoDBCertificateReconciler) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MongoDBCertificate", request.NamespacedName)
	log.Info("-> MongoDBCertificate.Reconcile")

	certificate, err := r.getCertificate(ctx, request, log)
	certificate.Spec.CertificateWrapper.CertificateSpec = &cmv1.CertificateSpec{}
	if err != nil {
		log.Warnf("error getting certificate %s", err)
		return reconcile.Result{RequeueAfter: time.Second * util.RetryTimeSec}, nil
	}

	return r.updateStatus(ctx, certificate, workflow.OK(), log)
}

func (r *MongoDBCertificateReconciler) getCertificate(ctx context.Context, request reconcile.Request, log *zap.SugaredLogger) (*mdbcv1.MongoDBCertificate, error) {
	certificate := &mdbcv1.MongoDBCertificate{}
	if _, err := r.GetResource(ctx, request, certificate, log); err != nil {
		return nil, err
	}

	return certificate, nil
}

func (r *MongoDBCertificateReconciler) delete(ctx context.Context, obj interface{}, log *zap.SugaredLogger) error {
	certificate := obj.(*mdbcv1.MongoDBCertificate)

	r.resourceWatcher.RemoveAllDependentWatchedResources(certificate.Namespace, kube.ObjectKeyFromApiObject(certificate))

	return nil
}
