package main

import (
	"context"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

type mongoDBReconciler struct {
	namespace           string
	operatorClusterName string
	clusterMap          map[string]client.Client
	deploy              bool
	diffwatchImage      string
}

func newMongoDBReconciler(operatorClusterName string, namespace string, clusterMap map[string]client.Client, deployPods bool, diffwatchImage string) *mongoDBReconciler {
	return &mongoDBReconciler{
		namespace:           namespace,
		operatorClusterName: operatorClusterName,
		clusterMap:          clusterMap,
		deploy:              deployPods,
		diffwatchImage:      diffwatchImage,
	}
}

func (r *mongoDBReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := zap.S()
	mdb := mdbv1.MongoDB{}
	logger.Debugf("Received MongoDB reconcile event: %+v", request)

	operatorClient := r.clusterMap[r.operatorClusterName]
	if err := operatorClient.Get(ctx, request.NamespacedName, &mdb); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error getting MongoDB resource %+v: %w", request.NamespacedName, err)
	}

	logger.Debugf("Command line equivalent: mdbdebug --type mdb --context %s --namespace %s --name %s", r.operatorClusterName, request.Namespace, request.Name)
	attachCommands, err := debugMongoDB(ctx, r.clusterMap, r.operatorClusterName, request.Namespace, request.Name, r.deploy, r.diffwatchImage)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error deploying debug for MongoDB resource %+v: %w", request.NamespacedName, err)
	}

	if err = createOrUpdateAttachCommandsCM(ctx, logger, request.Namespace, request.Name, "mdb", attachCommands, operatorClient); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, err
	}

	return reconcile.Result{}, nil
}

type mongoDBMultiClusterReconciler struct {
	namespace           string
	operatorClusterName string
	clusterMap          map[string]client.Client
	diffwatchImage      string
}

func newMongoDBMultiClusterReconciler(operatorClusterName string, namespace string, clusterMap map[string]client.Client, diffwatchImage string) *mongoDBMultiClusterReconciler {
	return &mongoDBMultiClusterReconciler{
		namespace:           namespace,
		operatorClusterName: operatorClusterName,
		clusterMap:          clusterMap,
		diffwatchImage:      diffwatchImage,
	}
}

func (r *mongoDBMultiClusterReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := zap.S()
	mdb := mdbmulti.MongoDBMultiCluster{}
	logger.Debugf("Received MongoDBMultiCluster reconcile event: %+v", request)

	operatorClient := r.clusterMap[r.operatorClusterName]
	if err := operatorClient.Get(ctx, request.NamespacedName, &mdb); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error getting MongoDBMultiCluster resource %+v: %w", request.NamespacedName, err)
	}

	attachCommands, err := debugMongoDB(ctx, r.clusterMap, r.operatorClusterName, request.Namespace, request.Name, true, r.diffwatchImage)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error deploying debug for MongoDBMultiCluster resource %+v: %w", request.NamespacedName, err)
	}

	if err = createOrUpdateAttachCommandsCM(ctx, logger, request.Namespace, request.Name, "", attachCommands, operatorClient); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, err
	}

	return reconcile.Result{}, nil
}

type opsManagerReconciler struct {
	namespace           string
	operatorClusterName string
	clusterMap          map[string]client.Client
	deploy              bool
	diffwatchImage      string
}

func newOpsManagerReconciler(operatorClusterName string, namespace string, clusterMap map[string]client.Client, deployPods bool, diffwatchImage string) *opsManagerReconciler {
	return &opsManagerReconciler{
		namespace:           namespace,
		operatorClusterName: operatorClusterName,
		clusterMap:          clusterMap,
		deploy:              deployPods,
		diffwatchImage:      diffwatchImage,
	}
}

func (r *opsManagerReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := zap.S()
	om := omv1.MongoDBOpsManager{}
	logger.Debugf("Received MongoDBOpsManager reconcile event: %+v", request)

	operatorClient := r.clusterMap[r.operatorClusterName]
	if err := operatorClient.Get(ctx, request.NamespacedName, &om); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error getting MongoDBOpsManager resource %+v: %w", request.NamespacedName, err)
	}

	logger.Debugf("Command line equivalent: mdbdebug --type om --context %s --namespace %s --name %s", r.operatorClusterName, request.Namespace, request.Name)
	attachCommands, err := debugOpsManager(ctx, r.clusterMap, r.operatorClusterName, request.Namespace, request.Name, r.deploy, r.diffwatchImage)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error deploying debug for MongoDBOpsManager resource %+v: %w", request.NamespacedName, err)
	}

	if err = createOrUpdateAttachCommandsCM(ctx, logger, request.Namespace, request.Name, "om", attachCommands, operatorClient); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, err
	}

	return reconcile.Result{}, nil
}

type mongoDBCommunityReconciler struct {
	namespace           string
	operatorClusterName string
	client              client.Client
	deploy              bool
	diffwatchImage      string
}

func newMongoDBCommunityReconciler(operatorClusterName string, namespace string, client client.Client, deployPods bool, diffwatchImage string) *mongoDBCommunityReconciler {
	return &mongoDBCommunityReconciler{
		namespace:           namespace,
		operatorClusterName: operatorClusterName,
		client:              client,
		deploy:              deployPods,
		diffwatchImage:      diffwatchImage,
	}
}

func (r *mongoDBCommunityReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := zap.S()
	mdb := mdbcv1.MongoDBCommunity{}
	logger.Debugf("Received MongoDBCommunity reconcile event: %+v", request)

	operatorClient := r.client
	if err := operatorClient.Get(ctx, request.NamespacedName, &mdb); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error getting MongoDB resource %+v: %w", request.NamespacedName, err)
	}

	logger.Debugf("Command line equivalent: mdbdebug --type mdb --context %s --namespace %s --name %s", r.operatorClusterName, request.Namespace, request.Name)
	attachCommands, err := debugMongoDBCommunity(ctx, request.Namespace, request.Name, r.operatorClusterName, kubernetesClient.NewClient(operatorClient), r.deploy, r.diffwatchImage)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error deploying debug for MongoDB resource %+v: %w", request.NamespacedName, err)
	}

	if err = createOrUpdateAttachCommandsCM(ctx, logger, request.Namespace, request.Name, "mdbc", attachCommands, operatorClient); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, err
	}

	return reconcile.Result{}, nil
}

type mongoDBSearchReconciler struct {
	namespace           string
	operatorClusterName string
	client              client.Client
	deploy              bool
	diffwatchImage      string
}

func newMongoDBSearchReconciler(operatorClusterName string, namespace string, client client.Client, deployPods bool, diffwatchImage string) *mongoDBSearchReconciler {
	return &mongoDBSearchReconciler{
		namespace:           namespace,
		operatorClusterName: operatorClusterName,
		client:              client,
		deploy:              deployPods,
		diffwatchImage:      diffwatchImage,
	}
}

func (r *mongoDBSearchReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	logger := zap.S()
	mdb := searchv1.MongoDBSearch{}
	logger.Debugf("Received MongoDBSearch reconcile event: %+v", request)

	operatorClient := r.client
	if err := operatorClient.Get(ctx, request.NamespacedName, &mdb); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error getting MongoDBSearch resource %+v: %w", request.NamespacedName, err)
	}

	logger.Debugf("Command line equivalent: mdbdebug --type mdbs --context %s --namespace %s --name %s", r.operatorClusterName, request.Namespace, request.Name)
	attachCommands, err := debugMongoDBSearch(ctx, request.Namespace, request.Name, r.operatorClusterName, kubernetesClient.NewClient(operatorClient), r.deploy, r.diffwatchImage)
	if err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, xerrors.Errorf("error deploying debug for MongoDBSearch resource %+v: %w", request.NamespacedName, err)
	}

	if err = createOrUpdateAttachCommandsCM(ctx, logger, request.Namespace, request.Name, "mdbs", attachCommands, operatorClient); err != nil {
		return reconcile.Result{RequeueAfter: time.Second * 10}, err
	}

	return reconcile.Result{}, nil
}
