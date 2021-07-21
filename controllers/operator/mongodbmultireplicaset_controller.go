package operator

import (
	"context"
	"fmt"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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
	if reconcileResult, err := r.prepareResourceForReconciliation(request, &mrs, log); reconcileResult != nil {
		log.Errorf("error preparing resource for reconciliation: %s", err)
		return *reconcileResult, err
	}

	// read Ops Manager configuration from the same namespace as the operator.
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, &mrs)
	if err != nil {
		log.Errorf("error reading project config and credentials: %s", err)
		return reconcile.Result{}, err
	}

	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		log.Errorf("error establishing connection to Ops Manager: %s", err)
		return reconcile.Result{}, err
	}

	log = log.With("MemberCluster Namespace", mrs.Spec.Namespace)

	for i, item := range mrs.GetOrderedClusterSpecList() {
		client := r.memberClusterClientsMap[item.ClusterName]
		// copy the agent api key to the member cluster.
		err := copySecret(r.client, client,
			types.NamespacedName{Name: fmt.Sprintf("%s-group-secret", conn.GroupID()), Namespace: mrs.Namespace},
			types.NamespacedName{Name: fmt.Sprintf("%s-group-secret", conn.GroupID()), Namespace: mrs.Spec.Namespace},
		)

		if err != nil {
			log.Errorf(err.Error())
			return reconcile.Result{}, err
		}

		for num := 0; num < item.Members; num++ {
			sts := construct.MultiClusterStatefulSet(mrs, i, num, conn)
			if err := client.Create(context.TODO(), &sts); err != nil {
				if !errors.IsAlreadyExists(err) {
					log.Errorf("Failed to create StatefulSet in cluster: %s, err: %s", item.ClusterName, err)
					return reconcile.Result{}, err
				}
			}
			log.Infof("Successfully created StatefulSet in cluster: %s", item.ClusterName)
		}
	}

	err = r.reconcileServices(log, mrs)
	if err != nil {
		log.Error(err)
		return reconcile.Result{}, err
	}

	if err := updateOmDeploymentRs(conn, mrs, log); err != nil {
		log.Errorf(err.Error())
		return reconcile.Result{}, err
	}

	log.Infow("Successfully finished reconcilliation", "MultiReplicaSetSpec", mrs.Spec)
	return reconcile.Result{}, nil
}

func getMultiClusterAgentHostnames(mrs mdbmultiv1.MongoDBMulti) []string {
	hostnames := make([]string, 0)
	for i, spec := range mrs.GetOrderedClusterSpecList() {
		for j := 0; j < spec.Members; j++ {
			hostnames = append(hostnames, mrs.GetServiceFQDN(j, i))
		}
	}
	return hostnames
}

// TODO: duplicated function from process package, remove/refactor
func createMongodProcessesWithLimit(mrs mdbmultiv1.MongoDBMulti) []om.Process {
	hostnames := getMultiClusterAgentHostnames(mrs)
	processes := make([]om.Process, len(hostnames))

	for idx := range hostnames {
		processes[idx] = om.NewMongodProcessMulti(fmt.Sprintf("%s-%d", mrs.Name, idx), hostnames[idx], mrs.Spec.Version)
	}

	return processes
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func updateOmDeploymentRs(conn om.Connection, mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {

	hostnames := getMultiClusterAgentHostnames(mrs)
	err := agents.WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster(conn, hostnames, log)
	if err != nil {
		return err
	}

	processes := createMongodProcessesWithLimit(mrs)
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

	// if err := om.WaitForReadyState(conn, rs.GetProcessNames(), log); err != nil {
	// 	return err
	// }

	return nil
}

func getService(mrs mdbmultiv1.MongoDBMulti, stsNum, clusterNum int) corev1.Service {
	labels := map[string]string{
		"app":        mrs.GetServiceName(stsNum, clusterNum),
		"controller": "mongodb-enterprise-operator",
	}

	return service.Builder().
		SetName(mrs.GetServiceName(stsNum, clusterNum)).
		SetNamespace(mrs.Spec.Namespace).
		SetPort(27017).
		SetPortName("mongodb").
		SetSelector(labels).
		SetLabels(labels).
		SetClusterIP("None").
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
			for i, e := range mrs.GetOrderedClusterSpecList() {
				for n := 0; n < e.Members; n++ {
					svc := getService(mrs, n, i)
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
	for i, e := range mrs.GetOrderedClusterSpecList() {
		client := r.memberClusterClientsMap[e.ClusterName]
		for n := 0; n < e.Members; n++ {
			svc := getService(mrs, n, i)
			err := service.CreateOrUpdateService(client, svc)
			if err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to created service: %s in cluster: %s, err: %v", svc.Name, e.ClusterName, err)
			}
			log.Infof("Successfully created service: %s in cluster: %s", svc.Name, e.ClusterName)
		}
	}
	return nil
}

// AddMultiReplicaSetController creates a new MongoDbMultiReplicaset Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func AddMultiReplicaSetController(mgr manager.Manager, memberClustersMap map[string]cluster.Cluster) error {
	reconciler := newMultiClusterReplicaSetReconciler(mgr, om.NewOpsManagerConnection, memberClustersMap)

	// TODO: add events handler for MongoDBMulti CR
	//eventHandler := MongoDBMultiResourceEventHandler{}

	_, err := ctrl.NewControllerManagedBy(mgr).For(&mdbmultiv1.MongoDBMulti{}).
		Build(reconciler)
	if err != nil {
		return err
	}

	// set up watch for Statefulset for each of the memberclusters
	//for k, v := range memberClustersMap {
	//	err := ctrl.Watch(source.NewKindWithCache(&appsv1.StatefulSet{}, v.GetCache()), nil)
	//	if err != nil {
	//		return fmt.Errorf("Failed to set Watch on member cluster: %s, err: %v", k, err)
	//	}
	//}

	// Watches(&source.Kind{Type: &mdbmultiv1.MongoDBMulti{}}, eventHandler).
	// WithEventFilter(predicate.Funcs{})

	// c, err := controller.New(util.MongoDbMultiReplicaSetController, mgr, controller.Options{Reconciler: reconciler})
	// if err != nil {
	// 	return err
	// }

	// Watch for changes to primary resource MongoDbReplicaSet
	// err = c.Watch(&source.Kind{Type: &mdbmultiv1.MongoDBMulti{}}, eventHandler, predicate.Funcs{})
	// if err != nil {
	// 	return err
	// }

	// TODO: add watch predicates for other objects like sts/secrets/configmaps while we implement the reconcile
	// logic for those objects
	zap.S().Infof("Registered controller %s", util.MongoDbMultiReplicaSetController)
	return err
}
