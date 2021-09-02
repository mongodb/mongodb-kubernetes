package operator

import (
	"context"
	"fmt"
	"reflect"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/host"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/hashicorp/go-multierror"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/process"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/agents"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/authentication"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/certs"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/connection"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/project"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
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
	// TODO: uncomment this after CLOUDP-96054 is resolved
	// agents.UpgradeAllIfNeeded(r.client, r.omConnectionFactory, getWatchedNamespace())

	log := zap.S().With("MultiReplicaSet", request.NamespacedName)
	log.Info("-> MultiReplicaSet.Reconcile")

	// Fetch the MongoDBMulti instance
	mrs := mdbmultiv1.MongoDBMulti{}
	if reconcileResult, err := r.prepareResourceForReconciliation(request, &mrs, log); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		log.Errorf("error preparing resource for reconciliation: %s", err)
		return reconcileResult, err
	}

	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, &mrs, log)
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
		memberClient := r.memberClusterClientsMap[item.ClusterName]
		// Ensure TLS for multi-cluster statefulset
		if status := certs.EnsureSSLCertsForStatefulSet(memberClient, *mrs.Spec.Security, certs.MultiReplicaSetConfig(mrs, i, item.Members), log); !status.IsOK() {
			log.Error("failed to ensure Statefulset for MDB Multi")
			return r.updateStatus(&mrs, status, log)
		}

		// copy the agent api key to the member cluster.
		err := secret.CopySecret(r.client, memberClient,
			types.NamespacedName{Name: fmt.Sprintf("%s-group-secret", conn.GroupID()), Namespace: mrs.Namespace},
			types.NamespacedName{Name: fmt.Sprintf("%s-group-secret", conn.GroupID()), Namespace: mrs.Namespace},
		)

		if err != nil {
			log.Errorf(err.Error())
			return reconcile.Result{}, err
		}

		sts := construct.MultiClusterStatefulSet(mrs, i, item.Members, conn)
		if err := memberClient.Create(context.TODO(), &sts); err != nil {
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

	log.Infof("Finished reconciliation for MultiReplicaSetSpec: %v", mrs.Spec)
	return r.updateStatus(&mrs, workflow.OK(), log)
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func updateOmDeploymentRs(conn om.Connection, mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	hostnames := make([]string, 0)

	for clusterNum, spec := range mrs.GetOrderedClusterSpecList() {
		hostnames = append(hostnames, dns.GetMultiClusterAgentHostnames(mrs.Name, mrs.Namespace, clusterNum, spec.Members)...)
	}

	err := agents.WaitForRsAgentsToRegisterReplicasSpecifiedMultiCluster(conn, hostnames, log)
	if err != nil {
		return err
	}

	processes := process.CreateMongodProcessesWithLimitMulti(mrs)
	rs := om.NewReplicaSetWithProcesses(om.NewReplicaSet(mrs.Name, mrs.Spec.Version), processes)

	status, additionalReconciliationRequired := updateOmMultiSCRAMAuthentication(conn, rs.GetProcessNames(), &mrs, log)
	if !status.IsOK() {
		return fmt.Errorf("failed to enabled SCRAM Authorization for MongoDB Multi Replicaset")
	}

	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			d.MergeReplicaSet(rs, log)
			d.AddMonitoringAndBackup(log, false)
			d.ConfigureTLS(mrs.Spec.GetSecurity().TLSConfig)
			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	if additionalReconciliationRequired {
		// TODO: fix this decide when to use Pending vs Reconciling
		return fmt.Errorf("failed to complete reconciliation")
	}

	if err := om.WaitForReadyState(conn, rs.GetProcessNames(), log); err != nil {
		return err
	}
	return nil
}

func getService(mrs mdbmultiv1.MongoDBMulti, clusterNum, podNum int) corev1.Service {
	svcLabels := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(mrs.Name, clusterNum, podNum),
		"controller":                         "mongodb-enterprise-operator",
		"mongodbmulti":                       fmt.Sprintf("%s-%s", mrs.Namespace, mrs.Name),
	}

	labelSelectors := map[string]string{
		"statefulset.kubernetes.io/pod-name": dns.GetMultiPodName(mrs.Name, clusterNum, podNum),
		"controller":                         "mongodb-enterprise-operator",
	}

	return service.Builder().
		SetName(dns.GetServiceName(mrs.Name, clusterNum, podNum)).
		SetNamespace(mrs.Namespace).
		SetPort(27017).
		SetPortName("mongodb").
		SetSelector(labelSelectors).
		SetLabels(svcLabels).
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
		key := fmt.Sprintf("%s", dns.GetMultiPodName(mrs.Name, clusterNum, podNum))
		value := fmt.Sprintf("%s.%s.svc.cluster.local", dns.GetServiceName(mrs.Name, clusterNum, podNum), mrs.Namespace)
		data[key] = value
	}

	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hostname-override",
			Namespace: mrs.Namespace,
			Labels: map[string]string{
				"controller":   "mongodb-enterprise-operator",
				"mongodbmulti": fmt.Sprintf("%s-%s", mrs.Namespace, mrs.Name),
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
	c, err := controller.New(util.MongoDbMultiController, mgr, controller.Options{Reconciler: reconciler})
	if err != nil {
		return err
	}

	eventHandler := ResourceEventHandler{deleter: reconciler}
	err = c.Watch(&source.Kind{Type: &mdbmultiv1.MongoDBMulti{}}, &eventHandler, predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldResource := e.ObjectOld.(*mdbmultiv1.MongoDBMulti)
			newResource := e.ObjectNew.(*mdbmultiv1.MongoDBMulti)
			return reflect.DeepEqual(oldResource.GetStatus(), newResource.GetStatus())
		},
	})

	if err != nil {
		return err
	}

	// register watcher across member clusters
	for k, v := range memberClustersMap {
		err := c.Watch(source.NewKindWithCache(&appsv1.StatefulSet{}, v.GetCache()), &khandler.EnqueueRequestForOwnerMultiCluster{}, watch.PredicatesForMultiStatefulSet())
		if err != nil {
			return fmt.Errorf("failed to set Watch on member cluster: %s, err: %v", k, err)
		}
	}

	// TODO: add watch predicates for other objects like sts/secrets/configmaps while we implement the reconcile
	// logic for those objects
	zap.S().Infof("Registered controller %s", util.MongoDbMultiReplicaSetController)
	return err
}

// OnDelete cleans up Ops Manager state and all Kubernetes resources associated with this instance.
func (r *ReconcileMongoDbMultiReplicaSet) OnDelete(obj runtime.Object, log *zap.SugaredLogger) error {
	mrs := obj.(*mdbmultiv1.MongoDBMulti)
	return r.deleteManagedResources(*mrs, log)
}

// cleanOpsManagerState removes the project configuration (processes, auth settings etc.) from the corresponding OM project.
func (r *ReconcileMongoDbMultiReplicaSet) cleanOpsManagerState(mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(r.client, &mrs, log)
	if err != nil {
		return err
	}

	log.Infow("Removing replica set from Ops Manager", "config", mrs.Spec)
	conn, err := connection.PrepareOpsManagerConnection(r.client, projectConfig, credsConfig, r.omConnectionFactory, mrs.Namespace, log)
	if err != nil {
		return err
	}

	processNames := make([]string, 0)
	err = conn.ReadUpdateDeployment(
		func(d om.Deployment) error {
			processNames = d.GetProcessNames(om.ReplicaSet{}, mrs.Name)
			// error means that replica set is not in the deployment - it's ok and we can proceed (could happen if
			// deletion cleanup happened twice and the first one cleaned OM state already)
			if e := d.RemoveReplicaSetByName(mrs.Name, log); e != nil {
				log.Warnf("Failed to remove replica set from automation config: %s", e)
			}

			return nil
		},
		log,
	)
	if err != nil {
		return err
	}

	hostsToRemove := mrs.GetMultiClusterAgentHostnames()
	log.Infow("Stop monitoring removed hosts in Ops Manager", "removedHosts", hostsToRemove)

	if err = host.StopMonitoring(conn, hostsToRemove, log); err != nil {
		return err
	}

	opts := authentication.Options{
		AuthoritativeSet: false,
		ProcessNames:     processNames,
	}

	if err := authentication.Disable(conn, opts, true, log); err != nil {
		return err
	}
	log.Infof("Removed deployment %s from Ops Manager at %s", mrs.Name, conn.BaseURL())
	return nil
}

// deleteManagedResources deletes resources across all member clusters that are owned by this MongoDBMulti resource.
func (r *ReconcileMongoDbMultiReplicaSet) deleteManagedResources(mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	var errs error
	if err := r.cleanOpsManagerState(mrs, log); err != nil {
		errs = multierror.Append(errs, err)
	}

	for _, item := range mrs.GetOrderedClusterSpecList() {
		c := r.memberClusterClientsMap[item.ClusterName]
		if err := r.deleteClusterResources(c, mrs, log); err != nil {
			errs = multierror.Append(errs, fmt.Errorf("failed deleting dependant resources in cluster %s: %s", item.ClusterName, err))
		}
	}
	return errs
}

// deleteClusterResources removes all resources that are associated with the given MongoDBMulti resource in a given cluster.
func (r *ReconcileMongoDbMultiReplicaSet) deleteClusterResources(c kubernetesClient.Client, mrs mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) error {
	var errs error

	// cleanup resources in the namespace as the MongoDBMuliti with the corresponding label.
	cleanupOptions := mongodbMultiCleanUpOptions{
		namesapce: mrs.Namespace,
		labels: map[string]string{
			"mongodbmulti": fmt.Sprintf("%s-%s", mrs.Namespace, mrs.Name),
		},
	}

	if err := c.DeleteAllOf(context.TODO(), &corev1.Service{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed Serivces associated with %s/%s", mrs.Namespace, mrs.Name)
	}

	if err := c.DeleteAllOf(context.TODO(), &appsv1.StatefulSet{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed StatefulSets associated with %s/%s", mrs.Namespace, mrs.Name)
	}

	if err := c.DeleteAllOf(context.TODO(), &corev1.ConfigMap{}, &cleanupOptions); err != nil {
		errs = multierror.Append(errs, err)
	} else {
		log.Infof("Removed ConfigMaps associated with %s/%s", mrs.Namespace, mrs.Name)
	}

	r.RemoveMongodbWatchedResources(kube.ObjectKey(mrs.Namespace, mrs.Name))

	return errs
}

func updateOmMultiSCRAMAuthentication(conn om.Connection, processName []string, mdbm *mdbmultiv1.MongoDBMulti, log *zap.SugaredLogger) (workflow.Status, bool) {
	// check before proceding if authentication is enabled at all
	if mdbm.Spec.Security == nil || mdbm.Spec.Security.Authentication == nil {
		return workflow.OK(), false
	}

	if err := om.WaitForReadyState(conn, processName, log); err != nil {
		return workflow.Failed(err.Error()), false
	}

	// read automation config
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return workflow.Failed(err.Error()), false
	}

	scramAgentUserName := util.AutomationAgentUserName
	// only use the default name if there is not already a configure user name
	if ac.Auth.AutoUser != "" && ac.Auth.AutoUser != scramAgentUserName {
		scramAgentUserName = ac.Auth.AutoUser
	}

	authOpts := authentication.Options{
		MinimumMajorVersion: mdbm.Spec.MinimumMajorVersion(),
		Mechanisms:          mdbm.Spec.Security.Authentication.Modes,
		ProcessNames:        processName,
		AuthoritativeSet:    !mdbm.Spec.Security.Authentication.IgnoreUnknownUsers,
		AgentMechanism:      mdbm.Spec.Security.GetAgentMechanism(ac.Auth.AutoAuthMechanism),
		AutoUser:            scramAgentUserName,
	}

	log.Debugf("Using authentication options %+v", authentication.Redact(authOpts))

	shouldEnableAuthentiation := mdbm.Spec.Security.Authentication.Enabled
	if shouldEnableAuthentiation && canConfigureAuthentication(ac, mdbm.Spec.Security.Authentication.GetModes(), log) {
		log.Info("Configuring authentication for MongoDB Multi resource")

		if err := authentication.Configure(conn, authOpts, log); err != nil {
			return workflow.Failed(err.Error()), false
		}
	} else if shouldEnableAuthentiation {
		log.Debug("Attempting to enable authentication, but OpsManager state will not allow this")
		return workflow.OK(), true
	}

	return workflow.OK(), false
}

// mongodbMultiCleanUpOptions implements the required interface to be passed
// to the DeleteAllOf function, this cleans up resources of a given type with
// the provided labels in a specific namespace.
type mongodbMultiCleanUpOptions struct {
	namesapce string
	labels    map[string]string
}

func (m *mongodbMultiCleanUpOptions) ApplyToDeleteAllOf(opts *client.DeleteAllOfOptions) {
	opts.Namespace = m.namesapce
	opts.LabelSelector = labels.SelectorFromValidatedSet(m.labels)
}
