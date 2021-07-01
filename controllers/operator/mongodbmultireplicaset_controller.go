package operator

import (
	"context"
	"fmt"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// extract client from each cluster object
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

// For testing remove this later
func int32Ptr(i int32) *int32 { return &i }

func getStatefulSet(n int) appsv1.StatefulSet {
	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("demo-deployment-%d", n),
			Namespace: "tmp",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "demo",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "demo",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "web",
							Image: "nginx:1.12",
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									Protocol:      corev1.ProtocolTCP,
									ContainerPort: 80,
								},
							},
						},
					},
				},
			},
		},
	}
}

// Reconcile reads that state of the cluster for a MongoDbMultiReplicaSet object and makes changes based on the state read
// and what is in the MongoDbMultiReplicaSet.Spec
func (r *ReconcileMongoDbMultiReplicaSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
	log := zap.S().With("MultiReplicaSet", request.NamespacedName)
	log.Info("-> MultiReplicaSet.Reconcile")
	// create dummy statefulset in cluster2 based on the CR event creation in cluster1

	count := 0
	for k, v := range r.memberClusterClientsMap {
		sts := getStatefulSet(count)
		count += 1
		if err := v.Create(context.TODO(), &sts); err != nil {
			if !errors.IsAlreadyExists(err) {
				log.Errorf("Failed to create StatefulSet in cluster: %s, err: %s", k, err)
				// TODO: re-enqueue here
				continue
			}
		}
		log.Infof("Successfully created StatefulSet in cluster: %s", k)
	}

	mrs := &mdbmultiv1.MongoDBMulti{}
	if reconcileResult, err := r.prepareResourceForReconciliation(request, mrs, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	// by default we would create the duplicate services
	shouldCreateDuplicateServices := mrs.Spec.DuplicateServiceObjects == nil || *mrs.Spec.DuplicateServiceObjects
	err := r.reconcileServices(log, shouldCreateDuplicateServices, mrs.Name, mrs.Spec.ClusterSpecList)
	if err != nil {
		log.Error(err)
		// TODO: re-enqueue here
	}

	return reconcile.Result{}, nil
}

func getServiceSpec(replicasetName string, a, b int) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d-%d", replicasetName, a, b),
			Namespace: "tmp",
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
func (r *ReconcileMongoDbMultiReplicaSet) reconcileServices(log *zap.SugaredLogger, shouldCreateDuplicates bool, replicaSetName string, clusterList mdbmultiv1.ClusterSpecList) error {

	// iterate over each cluster and create service object corresponding to each of the pods in the multi-cluster RS.
	if shouldCreateDuplicates {
		for k, v := range r.memberClusterClientsMap {
			for i, e := range clusterList.ClusterSpecs {
				for n := 0; n < e.Members; n++ {
					svc := getServiceSpec(replicaSetName, i, n)
					err := v.Create(context.TODO(), svc)

					if err != nil && !errors.IsAlreadyExists(err) {
						return fmt.Errorf("Failed to created service: %s in cluster: %s, err: %v", svc.Name, k, err)
					}
					log.Infof("Successfully created service: %s in cluster: %s", svc.Name, k)
				}
			}
		}
		return nil
	}
	// create non-duplicate service objects
	for i, e := range clusterList.ClusterSpecs {
		client := r.memberClusterClientsMap[e.ClusterName]
		for n := 0; n < e.Members; n++ {
			svc := getServiceSpec(replicaSetName, i, n)

			err := client.Create(context.TODO(), svc)
			if err != nil {
				return fmt.Errorf("Failed to created service: %s in cluster: %s, err: %v", svc.Name, e.ClusterName, err)
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
		// Watches(&source.Kind{Type: &mdbmultiv1.MongoDBMulti{}}, eventHandler).
		// WithEventFilter(predicate.Funcs{})

	if err != nil {
		return err
	}

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
