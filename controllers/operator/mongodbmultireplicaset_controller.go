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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// For testing remove this later
func int32Ptr(i int32) *int32                                              { return &i }
func int64Ptr(i int64) *int64                                              { return &i }
func boolPtr(b bool) *bool                                                 { return &b }
func pvModePtr(s corev1.PersistentVolumeMode) *corev1.PersistentVolumeMode { return &s }

func ownerReference(mdbm mdbmultiv1.MongoDBMulti) []metav1.OwnerReference {
	groupVersionKind := schema.GroupVersionKind{
		Group:   mdbmultiv1.GroupVersion.Group,
		Version: mdbmultiv1.GroupVersion.Version,
		Kind:    mdbm.Kind,
	}
	ownerReference := *metav1.NewControllerRef(&mdbm, groupVersionKind)
	return []metav1.OwnerReference{ownerReference}
}

func getStatefulSet(mdbm mdbmultiv1.MongoDBMulti, ns string) appsv1.StatefulSet {
	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdbm.Name,
			Namespace: ns,
			Labels: map[string]string{
				"app":     mdbm.Name + "-svc",
				"manager": "mongodb-enterprise-operator",
			},
			OwnerReferences: ownerReference(mdbm),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":               mdbm.Name + "-svc",
					"controller":        "mongodb-enterprise-operator",
					"pod-anti-affinity": mdbm.Name,
				},
			},
			ServiceName: mdbm.Name + "-svc",
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":               mdbm.Name + "-svc",
						"controller":        "mongodb-enterprise-operator",
						"pod-anti-affinity": mdbm.Name,
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										TopologyKey: "kubernetes.io/hostname",
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"pod-anti-affinity": mdbm.Name,
											},
										},
									},
								},
							},
						},
					},
					// FIXME: Not the actual SA we want to use this has all permissions.
					ServiceAccountName: "mongodb-enterprise-operator-multi-cluster",
					Containers: []corev1.Container{
						{
							Image: "quay.io/mongodb/mongodb-enterprise-database:2.0.0",
							Name:  "mongodb-enterprise-database",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 27017,
									Protocol:      "TCP",
								},
							},
							LivenessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									Exec: &corev1.ExecAction{
										Command: []string{"/opt/scripts/probe.sh"},
									},
								},
								InitialDelaySeconds: 60,
								TimeoutSeconds:      30,
								PeriodSeconds:       30,
								SuccessThreshold:    1,
								FailureThreshold:    6,
							},
							ReadinessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									Exec: &corev1.ExecAction{
										Command: []string{"/opt/scripts/readinessprobe"},
									},
								},
								InitialDelaySeconds: 5,
								TimeoutSeconds:      5,
								PeriodSeconds:       5,
								SuccessThreshold:    1,
								FailureThreshold:    4,
							},
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:    int64Ptr(2000),
								RunAsNonRoot: boolPtr(true),
							},
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/opt/scripts/agent-launcher.sh"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
									SubPath:   "data",
								},
								{
									Name:      "data",
									MountPath: "/journal",
									SubPath:   "journal",
								},
								{
									Name:      "data",
									MountPath: "/var/log/mongodb-mms-automation",
									SubPath:   "logs",
								},
								{
									Name:      "database-scripts",
									MountPath: "/opt/scripts",
									ReadOnly:  true,
								},
							},
							Env: []corev1.EnvVar{
								{
									Name: "AGENT_API_KEY",
									// FIXME
									Value: "",
								},
								{
									Name:  "AGENT_FLAGS",
									Value: "-logFile,/var/log/mongodb-mms-automation/automation-agent.log",
								},
								{
									Name: "BASE_URL",
									// FIXME
									Value: "",
								},
								{
									Name: "GROUP_ID",
									// FIXME
									Value: "",
								},
								{
									Name: "USER_LOGIN",
									// FIXME: hard coded for now
									Value: "user.name@example.com",
								},
							},
						},
					},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						FSGroup:      int64Ptr(2000),
					},
					Volumes: []corev1.Volume{
						{
							Name: "database-scripts",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					TerminationGracePeriodSeconds: int64Ptr(600),
					InitContainers: []corev1.Container{
						{
							Name:            "mongodb-enterprise-init-database",
							Image:           "quay.io/mongodb/mongodb-enterprise-init-database:1.0.3",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:    int64Ptr(2000),
								RunAsNonRoot: boolPtr(true),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "database-scripts",
									MountPath: "/opt/scripts",
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								"storage": resource.MustParse("16G"),
							},
						},
						VolumeMode: pvModePtr(corev1.PersistentVolumeFilesystem),
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

	// Fetch the MongoDBMulti instance
	mrs := &mdbmultiv1.MongoDBMulti{}
	if reconcileResult, err := r.prepareResourceForReconciliation(request, mrs, log); reconcileResult != nil {
		return *reconcileResult, err
	}

	for k, v := range r.memberClusterClientsMap {
		sts := getStatefulSet(*mrs, mrs.Spec.Namespace)
		if err := v.Create(context.TODO(), &sts); err != nil {
			if !errors.IsAlreadyExists(err) {
				log.Errorf("Failed to create StatefulSet in cluster: %s, err: %s", k, err)
				// TODO: re-enqueue here
				continue
			}
		}
		log.Infof("Successfully created StatefulSet in cluster: %s", k)
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
