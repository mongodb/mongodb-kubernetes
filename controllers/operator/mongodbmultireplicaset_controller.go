package operator

import (
	"context"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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

	log = log.With("MemberCluster Namespace", mrs.Spec.Namespace)
	for k, v := range r.memberClusterClientsMap {
		projectConfig, credsConfig, err := project.ReadConfigAndCredentials(v, &mrs)
		if err != nil {
			log.Errorf("error reading project config and credentials: %s", err)
			return reconcile.Result{}, err
		}

		conn, err := connection.PrepareOpsManagerConnection(v, projectConfig, credsConfig, r.omConnectionFactory, mrs.Spec.Namespace, log)
		if err != nil {
			log.Errorf("error establishing connection to Ops Manager: %s", err)
			return reconcile.Result{}, err
		}

		sts := construct.MultiClusterStatefulSet(mrs, conn)
		if err := v.Create(context.TODO(), &sts); err != nil {
			if !errors.IsAlreadyExists(err) {
				log.Errorf("Failed to create StatefulSet in cluster: %s, err: %s", k, err)
				return reconcile.Result{}, err
			}
		}
		log.Infof("Successfully created StatefulSet in cluster: %s", k)
	}

	err := r.reconcileServices(log, mrs)
	if err != nil {
		log.Error(err)
		return reconcile.Result{}, err
	}

	log.Infow("Successfully finished reconcilliation", "MultiReplicaSetSpec", mrs.Spec)
	return reconcile.Result{}, nil
}

func getServiceSpec(replicasetName, namespace string, a, b int) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d-%d", replicasetName, a, b),
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 8080,
				},
			},
			Selector:  nil,
			ClusterIP: "",
		},
	}
}

// reconcileServices make sure that we have a service object corresponding to each statefulset pod
// in the member clusters
func (r *ReconcileMongoDbMultiReplicaSet) reconcileServices(log *zap.SugaredLogger, mrs mdbmultiv1.MongoDBMulti) error {
	// by default we would create the duplicate services
	shouldCreateDuplicates := mrs.Spec.DuplicateServiceObjects == nil || *mrs.Spec.DuplicateServiceObjects
	if shouldCreateDuplicates {

		// iterate over each cluster and create service object corresponding to each of the pods in the multi-cluster RS.
		for k, v := range r.memberClusterClientsMap {
			for i, e := range mrs.Spec.ClusterSpecList.ClusterSpecs {
				for n := 0; n < e.Members; n++ {
					svc := getServiceSpec(mrs.Name, mrs.Spec.Namespace, i, n)
					err := v.Create(context.TODO(), svc)

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
	for i, e := range mrs.Spec.ClusterSpecList.ClusterSpecs {
		client := r.memberClusterClientsMap[e.ClusterName]
		for n := 0; n < e.Members; n++ {
			svc := getServiceSpec(mrs.Name, mrs.Spec.Namespace, i, n)

			err := client.Create(context.TODO(), svc)
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

	ctrl, err := ctrl.NewControllerManagedBy(mgr).For(&mdbmultiv1.MongoDBMulti{}).
		Build(reconciler)
	if err != nil {
		return err
	}

	// set up watch for Statefulset for each of the memberclusters
	for k, v := range memberClustersMap {
		err := ctrl.Watch(source.NewKindWithCache(&appsv1.StatefulSet{}, v.GetCache()), nil)
		if err != nil {
			return fmt.Errorf("Failed to set Watch on member cluster: %s, err: %v", k, err)
		}
	}

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
	zap.S().Infof("Registered controller %s", util.MongoDbReplicaSetController)
	return err
}
