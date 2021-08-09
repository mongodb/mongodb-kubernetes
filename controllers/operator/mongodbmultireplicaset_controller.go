package operator

import (
	"context"
	"fmt"
	"reflect"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/process"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	khandler "github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileMongoDbMultiReplicaSet reconciles a MongoDB ReplicaSet across multiple Kubernetes clusters
type ReconcileMongoDbMultiReplicaSet struct {
	*ReconcileCommonController
	watch.ResourceWatcher
	omConnectionFactory     om.ConnectionFactory
	memberClusterClientsMap map[string]kubernetesClient.Client // holds the client for each of the memberclusters(where the MongoDB ReplicaSet is deployed)
}

var _ reconcile.Reconciler = &ReconcileMongoDbMultiReplicaSet{}

func newMultiClusterReplicaSetReconciler(mgr manager.Manager, omFunc om.ConnectionFactory, memberClustersMap map[string]cluster.Cluster) *ReconcileMongoDbMultiReplicaSet {
	clientsMap := make(map[string]kubernetesClient.Client)

	// extract client from each cluster object.
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v.GetClient())
	}

	return &ReconcileMongoDbMultiReplicaSet{
		ReconcileCommonController: newReconcileCommonController(mgr),
		ResourceWatcher:           watch.NewResourceWatcher(),
		omConnectionFactory:       omFunc,
		memberClusterClientsMap:   clientsMap,
	}
}

func copySecret(fromClient secret.Getter, toClient secret.GetUpdateCreator, sourceSecretNsName, destNsName types.NamespacedName) error {
	s, err := fromClient.GetSecret(sourceSecretNsName)
	if err != nil {
		return err
	}

	secretCopy := secret.Builder().
		SetName(destNsName.Name).
		SetNamespace(destNsName.Namespace).
		SetByteData(s.Data).
		Build()

	return secret.CreateOrUpdate(toClient, secretCopy)
}

// Reconcile reads that state of the cluster for a MongoDbMultiReplicaSet object and makes changes based on the state read
// and what is in the MongoDbMultiReplicaSet.Spec
func (r *ReconcileMongoDbMultiReplicaSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MultiReplicaSet", request.NamespacedName)
	log.Info("-> MultiReplicaSet.Reconcile")

	// Fetch the MongoDBMulti instance
	mrs := mdbmultiv1.MongoDBMulti{}
	if reconcileResult, err := r.prepareResourceForReconciliation(request, &mrs, log); reconcileResult != (reconcile.Result{}) {
		log.Errorf("error preparing resource for reconciliation: %s", err)
		return reconcileResult, err
	}

	// read Ops Manager configuration from the same namespace as the operator.
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, &mrs)
	if err != nil {
		log.Errorf("error reading project config and credentials: %s", err)
		return r.updateStatus(&mrs, workflow.Failed("Error reading project config and credentials: %s", err), log)
	}

	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		log.Errorf("error establishing connection to Ops Manager: %s", err)
		return reconcile.Result{}, err
	}

	log = log.With("MemberCluster Namespace", mrs.Namespace)

	for i, item := range mrs.GetOrderedClusterSpecList() {
		client := r.memberClusterClientsMap[item.ClusterName]
		// copy the agent api key to the member cluster.
		err := copySecret(r.client, client,
			types.NamespacedName{Name: fmt.Sprintf("%s-group-secret", conn.GroupID()), Namespace: mrs.Namespace},
			types.NamespacedName{Name: fmt.Sprintf("%s-group-secret", conn.GroupID()), Namespace: mrs.Namespace},
		)

		if err != nil {
			log.Errorf(err.Error())
			return reconcile.Result{}, err
		}

		sts := construct.MultiClusterStatefulSet(mrs, i, item.Members, conn)
		if err := client.Create(context.TODO(), &sts); err != nil {
			if !errors.IsAlreadyExists(err) {
				log.Errorf("Failed to create StatefulSet in cluster: %s, err: %s", item.ClusterName, err)
				return reconcile.Result{}, err
			}
		}
		log.Infof("Successfully ensure StatefulSet in cluster: %s", item.ClusterName)
	}

	err = r.reconcileServices(log, mrs)
	if err != nil {
		log.Error(err)
		return reconcile.Result{}, err
	}

	// create configmap with the hostnameoverride
	err = r.reconcileHostnameOverrideConfigMap(log, mrs)
	if err != nil {
		log.Error(err)
		return reconcile.Result{}, err
	}

	if err := updateOmDeploymentRs(conn, mrs, log); err != nil {
		log.Errorf(err.Error())
		return reconcile.Result{}, err
	}

	log.Infow("Successfully finished reconcilliation", "MultiReplicaSetSpec", mrs.Spec)
	return r.updateStatus(&mrs, workflow.OK(), log)
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func updateOmDeploymentRs(conn om.Connection, mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {

	hostnames := mrs.GetMultiClusterAgentHostnames()
	err := agents.WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster(conn, hostnames, log)
	if err != nil {
		return err
	}

	processes := process.CreateMongodProcessesWithLimitMulti(mrs)
	rs := om.NewReplicaSetWithProcesses(om.NewReplicaSet(mrs.Name, mrs.Spec.Version), processes)

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.MergeReplicaSet(rs, log)
			d.AddMonitoringAndBackup(log, false)
			d.AddAgentVersionConfig()
			return nil
		},
		log,
	)

	if err != nil {
		return err
	}

	if err := om.WaitForReadyState(conn, rs.GetProcessNames(), log); err != nil {
		return err
	}

	return nil
}

func getService(mrs mdbmultiv1.MongoDBMulti, clusterNum, podNum int) corev1.Service {
	labels := map[string]string{
		"statefulset.kubernetes.io/pod-name": mrs.GetPodName(clusterNum, podNum),
		"controller":                         "mongodb-enterprise-operator",
	}

	return service.Builder().
		SetName(mrs.GetServiceName(clusterNum, podNum)).
		SetNamespace(mrs.Namespace).
		SetPort(27017).
		SetPortName("mongodb").
		SetSelector(labels).
		SetLabels(labels).
		SetPublishNotReadyAddresses(true).
		Build()
}

// reconcileServices make sure that we have a service object corresponding to each statefulset pod
// in the member clusters
func (r *ReconcileMongoDbMultiReplicaSet) reconcileServices(log *zap.SugaredLogger, mrs mdbmultiv1.MongoDBMulti) error {
	// by default we would create the duplicate services
	shouldCreateDuplicates := mrs.Spec.DuplicateServiceObjects == nil || *mrs.Spec.DuplicateServiceObjects
	if shouldCreateDuplicates {
		// iterate over each cluster and create service object corresponding to each of the pods in the multi-cluster RS.
		for k, v := range r.memberClusterClientsMap {
			for clusterNum, e := range mrs.GetOrderedClusterSpecList() {
				for podNum := 0; podNum < e.Members; podNum++ {
					svc := getService(mrs, clusterNum, podNum)
					err := service.CreateOrUpdateService(v, svc)

					if err != nil && !errors.IsAlreadyExists(err) {
						return fmt.Errorf("failed to created service: %s in cluster: %s, err: %v", svc.Name, k, err)
					}
					log.Infof("Successfully created service: %s in cluster: %s", svc.Name, k)
				}
			}
		}
		return nil
	}
	// create non-duplicate service objects
	for clusterNum, e := range mrs.GetOrderedClusterSpecList() {
		client := r.memberClusterClientsMap[e.ClusterName]
		for podNum := 0; podNum < e.Members; podNum++ {
			svc := getService(mrs, clusterNum, podNum)
			err := service.CreateOrUpdateService(client, svc)
			if err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to created service: %s in cluster: %s, err: %v", svc.Name, e.ClusterName, err)
			}
			log.Infof("Successfully created service: %s in cluster: %s", svc.Name, e.ClusterName)
		}
	}
	return nil
}

func getHostnameOverrideConfigMap(mrs mdbmultiv1.MongoDBMulti, clusterNum int, members int) corev1.ConfigMap {
	data := make(map[string]string)

	for podNum := 0; podNum < members; podNum++ {
		key := fmt.Sprintf("%s", mrs.GetPodName(clusterNum, podNum))
		value := fmt.Sprintf("%s.%s.svc.cluster.local", mrs.GetServiceName(clusterNum, podNum), mrs.Namespace)
		data[key] = value
	}

	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostname-override",
			Namespace: mrs.Namespace,
			Labels: map[string]string{
				"controller": "mongodb-enterprise-operator",
			},
		},
		Data: data,
	}
	return cm
}

func (r *ReconcileMongoDbMultiReplicaSet) reconcileHostnameOverrideConfigMap(log *zap.SugaredLogger, mrs mdbmultiv1.MongoDBMulti) error {
	for i, e := range mrs.GetOrderedClusterSpecList() {
		client := r.memberClusterClientsMap[e.ClusterName]
		cm := getHostnameOverrideConfigMap(mrs, i, e.Members)

		err := configmap.CreateOrUpdate(client, cm)
		if err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create configmap: %s in cluster: %s, err: %v", cm.Name, e.ClusterName, err)
		}
		log.Infof("Successfully ensured configmap: %s in cluster: %s", cm.Name, e.ClusterName)

	}
	return nil
}

// AddMultiReplicaSetController creates a new MongoDbMultiReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddMultiReplicaSetController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	reconciler := newMultiClusterReplicaSetReconciler(mgr, om.NewOpsManagerConnection, memberClustersMap)

	ctrl, err := ctrl.NewControllerManagedBy(mgr).For(&mdbmultiv1.MongoDBMulti{}).WithEventFilter(
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldResource := e.ObjectOld.(*mdbmultiv1.MongoDBMulti)
				newResource := e.ObjectNew.(*mdbmultiv1.MongoDBMulti)
				return reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
			},
		},
	).Build(reconciler)
	if err != nil {
		return err
	}

	// register watcher across member clusters
	for k, v := range memberClustersMap {
		err := ctrl.Watch(source.NewKindWithCache(&appsv1.StatefulSet{}, v.GetCache()), &khandler.EnqueueRequestForOwnerMultiCluster{}, watch.PredicatesForMultiStatefulSet())
		if err != nil {
			return fmt.Errorf("Failed to set Watch on member cluster: %s, err: %v", k, err)
		}
	}

	// TODO: add watch predicates for other objects like sts/secrets/configmaps while we implement the reconcile
	// logic for those objects
	zap.S().Infof("Registered controller %s", util.MongoDbMultiReplicaSetController)
	return err
}
