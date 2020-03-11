package operator

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube/configmap"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube/service"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeHelper is the helper for dealing with Kubernetes. If any Kubernetes logic requires more than some trivial operation
// - it should be put here
type KubeHelper struct {
	client          client.Client
	serviceClient   service.Client
	configmapClient configmap.Client
}

// NewKubeHelper constructs an instance of KubeHelper with all clients initialized
// using the same instance of client
func NewKubeHelper(client client.Client) KubeHelper {
	return KubeHelper{
		client:          client,
		configmapClient: configmap.NewClient(client),
		serviceClient:   service.NewClient(client),
	}
}

type AuthMode string

const (
	NumAgents = 3
)

// Credentials contains the configuration expected from the `credentials` (Secret)` attribute in
// `.spec.credentials`.
type Credentials struct {
	// +required
	User string

	// +required
	PublicAPIKey string
}

// StatefulSetHelperCommon is the basic struct the same for all Statefulset helpers (MongoDB, OpsManager)
type StatefulSetHelperCommon struct {
	// Attributes that are part of StatefulSet
	Owner     Updatable
	Name      string
	Service   string
	Namespace string

	// ClusterDomain is the cluster name that's usually 'cluster.local' but it
	// can be changed by the customer.
	ClusterDomain string
	Replicas      int
	ServicePort   int32
	Version       string
	ContainerName string
	PodSpec       *mdbv1.PodSpecWrapper

	// Not part of StatefulSet object
	Helper *KubeHelper
	Logger *zap.SugaredLogger
}

// StatefulSetHelper is a struct that holds different attributes needed to build
// a StatefulSet for MongoDB CR. It is used as a convenient way of passing many different parameters in one
// struct, instead of multiple parameters.
type StatefulSetHelper struct {
	StatefulSetHelperCommon

	Persistent *bool
	PodVars    *PodVars

	ResourceType mdbv1.ResourceType

	// The following attributes are not part of StatefulSet object

	// ExposedExternally sets this StatefulSetHelper to receive a `Service` that will allow it to be
	// visible from outside the Kubernetes cluster.
	ExposedExternally bool

	Project            mdbv1.ProjectConfig
	Security           *mdbv1.Security
	ReplicaSetHorizons []mdbv1.MongoDBHorizonConfig
	CertificateHash    string
}

func (ss StatefulSetHelper) hasHorizons() bool {
	return len(ss.ReplicaSetHorizons) > 0
}

// getAdditionalCertDomainsForMember gets any additional domains that the
// certificate for the given member of the stateful set should be signed for.
func (ss StatefulSetHelper) getAdditionalCertDomainsForMember(member int) (hostnames []string) {
	_, podnames := ss.getDNSNames()
	for _, certDomain := range ss.Security.TLSConfig.AdditionalCertificateDomains {
		hostnames = append(hostnames, podnames[member]+"."+certDomain)
	}
	if ss.hasHorizons() {
		// at this point len(ss.ReplicaSetHorizons) should be equal to the number
		// of members in the replica set
		for _, externalHost := range ss.ReplicaSetHorizons[member] {
			// need to use the URL struct directly instead of url.Parse as
			// Parse expects the URL to have a scheme.
			hostURL := url.URL{Host: externalHost}
			hostnames = append(hostnames, hostURL.Hostname())
		}
	}
	return hostnames
}

type OpsManagerStatefulSetHelper struct {
	StatefulSetHelperCommon

	// MongoDBOpsManagerSpec reference to the actual Spec received.
	Spec mdbv1.MongoDBOpsManagerSpec

	EnvVars []corev1.EnvVar
}

type BackupStatefulSetHelper struct {
	OpsManagerStatefulSetHelper

	HeadDbPersistenceConfig *mdbv1.PersistenceConfig
}

// ShardedClusterKubeState holds the Kubernetes configuration for the set of StatefulSets composing
// our ShardedCluster:
// 1 StatefulSet holding Mongos (TODO: this might need to be changed to Deployments or Kubernetes ReplicaSets)
// 1 StatefulSet holding ConfigServers
// N StatefulSets holding each a different shard
type ShardedClusterKubeState struct {
	mongosSetHelper    *StatefulSetHelper
	configSrvSetHelper *StatefulSetHelper
	shardsSetsHelpers  []*StatefulSetHelper
}

// NewStatefulSetHelper returns a default `StatefulSetHelper` for the database statefulset. The defaults are as follows:
//
// * Name: Same as the Name of the owner
// * Namespace: Same as the Namespace of the owner
// * Replicas: 1
// * ExposedExternally: false
// * ServicePort: `MongoDbDefaultPort` (27017)
//
// Note, that it's the same for both MongodbResource Statefulset and AppDB Statefulset. So the object passed
// can be either 'MongoDB' or 'MongoDBOpsManager' - in the latter case the configuration for AppDB is used.
// We pass the 'MongoDBOpsManager' instead of 'AppDB' as the former is the owner of the object - no AppDB CR exists
func (k *KubeHelper) NewStatefulSetHelper(obj Updatable) *StatefulSetHelper {
	var containerName string
	var mongodbSpec mdbv1.MongoDbSpec
	switch v := obj.(type) {
	case *mdbv1.MongoDB:
		containerName = util.DatabaseContainerName
		mongodbSpec = v.Spec
	case *mdbv1.MongoDBOpsManager:
		containerName = util.AppDbContainerName
		mongodbSpec = v.Spec.AppDB.MongoDbSpec
	default:
		panic("Wrong type provided, only MongoDB or AppDB are expected!")
	}

	return &StatefulSetHelper{
		StatefulSetHelperCommon: StatefulSetHelperCommon{
			ContainerName: containerName,
			Owner:         obj,
			Name:          obj.GetName(),
			Namespace:     obj.GetNamespace(),
			Replicas:      mongodbSpec.Members,
			Helper:        k,
			ServicePort:   util.MongoDbDefaultPort,
			Version:       mongodbSpec.GetVersion(),
			ClusterDomain: mongodbSpec.GetClusterDomain(),
			Logger:        zap.S(),                                        // by default, must be overridden by clients
			PodSpec:       NewDefaultPodSpecWrapper(*mongodbSpec.PodSpec), // by default, may be overridden by clients
		},
		Persistent:        mongodbSpec.Persistent,
		ExposedExternally: mongodbSpec.ExposedExternally,
	}
}

func (k *KubeHelper) NewOpsManagerStatefulSetHelper(opsManager mdbv1.MongoDBOpsManager) *OpsManagerStatefulSetHelper {
	spec := mdbv1.NewPodSpecWrapperBuilderFromSpec(opsManager.Spec.PodSpec).Build()
	spec.Default = mdbv1.OpsManagerPodSpecDefaultValues()
	return &OpsManagerStatefulSetHelper{
		StatefulSetHelperCommon: StatefulSetHelperCommon{
			Owner:         &opsManager,
			Name:          opsManager.GetName(),
			Namespace:     opsManager.GetNamespace(),
			ContainerName: util.OpsManagerContainerName,
			Replicas:      opsManager.Spec.Replicas,
			Helper:        k,
			ServicePort:   util.OpsManagerDefaultPort,
			Version:       opsManager.Spec.Version,
			Service:       opsManager.SvcName(),
			PodSpec:       spec,
		},
		Spec:    opsManager.Spec,
		EnvVars: opsManagerConfigurationToEnvVars(opsManager),
	}
}

func (k *KubeHelper) NewBackupStatefulSetHelper(opsManager mdbv1.MongoDBOpsManager) *BackupStatefulSetHelper {
	helper := BackupStatefulSetHelper{
		OpsManagerStatefulSetHelper: *k.NewOpsManagerStatefulSetHelper(opsManager),
	}
	helper.Name = opsManager.BackupStatefulSetName()
	helper.ContainerName = util.BackupDaemonContainerName
	helper.Service = ""
	helper.Replicas = 1
	if opsManager.Spec.Backup.HeadDB != nil {
		helper.HeadDbPersistenceConfig = opsManager.Spec.Backup.HeadDB
	}
	spec := mdbv1.NewPodSpecWrapperBuilderFromSpec(opsManager.Spec.Backup.PodSpec).Build()
	spec.Default = mdbv1.OpsManagerPodSpecDefaultValues()
	helper.PodSpec = spec
	return &helper
}

// SetName can override the value of `StatefulSetHelper.Name` which is set to
// `owner.GetName()` initially.
func (s *StatefulSetHelper) SetName(name string) *StatefulSetHelper {
	s.Name = name
	return s
}
func (s *StatefulSetHelper) SetOwner(obj Updatable) *StatefulSetHelper {
	s.Owner = obj
	return s
}

func (s *StatefulSetHelper) SetService(service string) *StatefulSetHelper {
	s.Service = service
	return s
}

func (s *StatefulSetHelper) SetReplicas(replicas int) *StatefulSetHelper {
	s.Replicas = replicas
	return s
}

func (s *StatefulSetHelper) SetPersistence(persistent *bool) *StatefulSetHelper {
	s.Persistent = persistent
	return s
}

func (s *StatefulSetHelper) SetPodSpec(podSpec *mdbv1.PodSpecWrapper) *StatefulSetHelper {
	s.PodSpec = podSpec
	return s
}

func (s *StatefulSetHelper) SetPodVars(podVars *PodVars) *StatefulSetHelper {
	s.PodVars = podVars
	return s
}

func (s *StatefulSetHelper) SetExposedExternally(exposedExternally bool) *StatefulSetHelper {
	s.ExposedExternally = exposedExternally
	return s
}

func (s *StatefulSetHelper) SetProjectConfig(project mdbv1.ProjectConfig) *StatefulSetHelper {
	s.Project = project
	return s
}

func (s *StatefulSetHelper) SetServicePort(port int32) *StatefulSetHelper {
	s.ServicePort = port
	return s
}

func (s *StatefulSetHelper) SetLogger(log *zap.SugaredLogger) *StatefulSetHelper {
	s.Logger = log
	return s
}

func (s *StatefulSetHelper) SetTLS(tlsConfig *mdbv1.TLSConfig) *StatefulSetHelper {
	if s.Security == nil {
		s.Security = &mdbv1.Security{}
	}
	s.Security.TLSConfig = tlsConfig
	return s
}

func (s *StatefulSetHelper) SetClusterName(name string) *StatefulSetHelper {
	if name == "" {
		s.ClusterDomain = "cluster.local"
	} else {
		s.ClusterDomain = name
	}

	return s
}

func (s StatefulSetHelper) IsTLSEnabled() bool {
	return s.Security != nil && s.Security.TLSConfig != nil && s.Security.TLSConfig.Enabled
}

func (s *StatefulSetHelper) SetVersion(version string) *StatefulSetHelper {
	s.Version = version
	return s
}

func (s *StatefulSetHelper) SetContainerName(containerName string) *StatefulSetHelper {
	s.ContainerName = containerName
	return s
}

func (s StatefulSetHelper) BuildStatefulSet() (appsv1.StatefulSet, error) {
	return buildStatefulSet(s)
}

func (s StatefulSetHelper) BuildAppDbStatefulSet() (appsv1.StatefulSet, error) {
	return buildAppDbStatefulSet(s)
}

// CreateOrUpdateInKubernetes creates (updates if it exists) the StatefulSet with its Service.
// It returns any errors coming from Kubernetes API.
func (s StatefulSetHelper) CreateOrUpdateInKubernetes() error {
	sts, err := s.BuildStatefulSet()
	if err != nil {
		return err
	}
	set, err := s.Helper.createOrUpdateStatefulset(
		s.Namespace,
		s.Logger,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := objectKey(s.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, mdbv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = s.Helper.createOrUpdateService(internalService, s.Logger)
	if err != nil {
		return err
	}

	if s.ExposedExternally {
		namespacedName := objectKey(s.Namespace, set.Spec.ServiceName+"-external")
		externalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, mdbv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeNodePort})
		err = s.Helper.createOrUpdateService(externalService, s.Logger)
	}

	return err
}

func (s OpsManagerStatefulSetHelper) BuildStatefulSet() (appsv1.StatefulSet, error) {
	return buildOpsManagerStatefulSet(s)
}

func (s BackupStatefulSetHelper) BuildStatefulSet() (appsv1.StatefulSet, error) {
	return buildBackupDaemonStatefulSet(s)
}

func (s *OpsManagerStatefulSetHelper) SetService(service string) *OpsManagerStatefulSetHelper {
	s.Service = service
	return s
}

func (s *OpsManagerStatefulSetHelper) SetName(name string) *OpsManagerStatefulSetHelper {
	s.Name = name
	return s
}

func (s *BackupStatefulSetHelper) SetHeadDbStorageRequirements(persistenceConfig *mdbv1.PersistenceConfig) *BackupStatefulSetHelper {
	s.HeadDbPersistenceConfig = persistenceConfig
	return s
}

func (s *OpsManagerStatefulSetHelper) SetLogger(log *zap.SugaredLogger) *OpsManagerStatefulSetHelper {
	s.Logger = log
	return s
}

func (s *OpsManagerStatefulSetHelper) SetVersion(version string) *OpsManagerStatefulSetHelper {
	s.Version = version
	return s
}

func (s OpsManagerStatefulSetHelper) CreateOrUpdateInKubernetes() error {
	sts, err := s.BuildStatefulSet()
	if err != nil {
		return err
	}
	set, err := s.Helper.createOrUpdateStatefulset(
		s.Namespace,
		s.Logger,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := objectKey(s.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, mdbv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = s.Helper.createOrUpdateService(internalService, s.Logger)
	if err != nil {
		return err
	}

	if s.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		namespacedName := objectKey(s.Namespace, set.Spec.ServiceName+"-ext")
		externalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, *s.Spec.MongoDBOpsManagerExternalConnectivity)
		err = s.Helper.createOrUpdateService(externalService, s.Logger)
	}

	return err
}

func (s BackupStatefulSetHelper) CreateOrUpdateInKubernetes() error {
	sts, err := s.BuildStatefulSet()
	if err != nil {
		return err
	}
	_, err = s.Helper.createOrUpdateStatefulset(
		s.Namespace,
		s.Logger,
		&sts,
	)
	if err != nil {
		return err
	}

	// We don't create a service for backup as it doesn't expose any endpoints
	return nil
}

// CreateOrUpdateAppDBInKubernetes creates the StatefulSet specific for AppDB.
func (s *StatefulSetHelper) CreateOrUpdateAppDBInKubernetes() error {
	appDbSts, err := s.BuildAppDbStatefulSet()
	if err != nil {
		return err
	}
	set, err := s.Helper.createOrUpdateStatefulset(
		s.Namespace,
		s.Logger,
		&appDbSts,
	)
	if err != nil {
		return err
	}

	namespacedName := objectKey(s.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, mdbv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = s.Helper.createOrUpdateService(internalService, s.Logger)
	return err
}

// getDNSNamesForStatefulSet Returns a list of hostnames and names for the N Pods that are part of this StatefulSet
// The `fqdns` refer to the FQDN names of the Pods, that makes them reachable and distinguishable at cluster level.
// The `names` array refers to the hostname of each Pod.
func (s *StatefulSetHelper) getDNSNames() ([]string, []string) {
	var members int

	if s.ResourceType == mdbv1.Standalone {
		members = 1
	} else {
		members = s.Replicas
	}

	return util.GetDNSNames(s.Name, s.Service, s.Namespace, s.ClusterDomain, members)
}

func (s *StatefulSetHelper) SetCertificateHash(certHash string) *StatefulSetHelper {
	s.CertificateHash = certHash
	return s
}

func (s *StatefulSetHelper) SetReplicaSetHorizons(replicaSetHorizons []mdbv1.MongoDBHorizonConfig) *StatefulSetHelper {
	s.ReplicaSetHorizons = replicaSetHorizons
	return s
}

func (s *StatefulSetHelper) SetSecurity(security *mdbv1.Security) *StatefulSetHelper {
	s.Security = security
	return s
}

// needToPublishStateFirst will check if the Published State of the StatfulSet backed MongoDB Deployments
// needs to be updated first. In the case of unmounting certs, for instance, the certs should be not
// required anymore before we unmount them, or the automation-agent and readiness probe will never
// reach goal state.
func (s *StatefulSetHelper) needToPublishStateFirst(log *zap.SugaredLogger) bool {
	currentSet := appsv1.StatefulSet{}
	namespacedName := objectKey(s.Namespace, s.Name)
	err := s.Helper.client.Get(context.TODO(), namespacedName, &currentSet)

	if err != nil {
		if apiErrors.IsNotFound(err) {
			// No need to publish state as this is a new StatefulSet
			log.Debugf("New StatefulSet %s", namespacedName)
			return false
		}

		log.Debugw(fmt.Sprintf("Error getting StatefulSet %s", namespacedName), "error", err)
		return false
	}

	volumeMounts := currentSet.Spec.Template.Spec.Containers[0].VolumeMounts
	if s.Security != nil {
		if !s.Security.TLSConfig.Enabled && volumeMountWithNameExists(volumeMounts, SecretVolumeName) {
			log.Debug("About to set `security.tls.enabled` to false. automationConfig needs to be updated first")
			return true
		}

		if s.Security.TLSConfig.CA == "" && volumeMountWithNameExists(volumeMounts, SecretVolumeCAName) {
			log.Debug("About to set `security.tls.CA` to empty. automationConfig needs to be updated first")
			return true
		}

	}

	if s.PodVars.SSLMMSCAConfigMap == "" && volumeMountWithNameExists(volumeMounts, CaCertName) {
		log.Debug("About to set `SSLMMSCAConfigMap` to empty. automationConfig needs to be updated first")
		return true
	}

	if s.Security.Authentication.GetAgentMechanism() != util.X509 && volumeMountWithNameExists(volumeMounts, util.AgentSecretName) {
		log.Debug("About to set `project.AuthMode` to empty. automationConfig needs to be updated first")
		return true
	}

	return false
}

func volumeMountWithNameExists(mounts []corev1.VolumeMount, volumeName string) bool {
	for _, mount := range mounts {
		if mount.Name == volumeName {
			return true
		}
	}

	return false
}

// createOrUpdateStatefulset will create or update a StatefulSet in Kubernetes.
//
// The method has to be flexible (create/update) as there are cases when custom resource is created but statefulset - not
// Service named "serviceName" is created optionally (it may already exist - created by either user or by operator before)
// Note the logic for "exposeExternally" parameter: if it is true then the second service is created of type "NodePort"
// (the random port will be allocated by Kubernetes) otherwise only one service of type "ClusterIP" is created and it
// won't be connectible from external (unless pods in statefulset expose themselves to outside using "hostNetwork: true")
// Function returns the service port number assigned
func (k *KubeHelper) createOrUpdateStatefulset(ns string, log *zap.SugaredLogger, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	log = log.With("statefulset", objectKey(ns, set.Name))
	var msg string
	existingStatefulSet := appsv1.StatefulSet{}
	if err := k.client.Get(context.TODO(), objectKey(ns, set.Name), &existingStatefulSet); err != nil {
		if apiErrors.IsNotFound(err) {
			if err = k.client.Create(context.TODO(), set); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
		msg = "Created"
	} else {
		// preserve existing certificate hash if new one is not set
		existingCertHash := existingStatefulSet.Spec.Template.Annotations["certHash"]
		newCertHash := set.Spec.Template.Annotations["certHash"]
		if existingCertHash != "" && newCertHash == "" {
			set.Spec.Template.Annotations["certHash"] = existingCertHash
		}
		if err = k.client.Update(context.TODO(), set); err != nil {
			return nil, err
		}
		msg = "Updated"
	}
	log.Debugf("%s StatefulSet", msg)

	return set, nil
}

// isStatefulSetUpdated will check if every Replica from the StatefulSet has been updated.
// The StatefulSet controller updates Pods one at a time, and each one is considered "ready" and
// "updated". We expect that the StatefulSet is completely Updated when all of the Pods have been
// updated (moved to latest version) and ready (they have reached Ready state after being updated).
// This function also sleeps for `K8S_CACHES_REFRESH_TIME_SEC` to somehow avoid fetching a cached
// result from the Kubernetes API.
// There is a short loop inside to check everything during 15 seconds. This will allow to discover "ok" result
// faster for users and tests (as the next reconciliation will happen in 10 seconds), though will still
// provide good interactivity for user requests
func (k *KubeHelper) isStatefulSetUpdated(namespace, name string, log *zap.SugaredLogger) bool {
	// environment variables are used only for tests
	waitSeconds := util.ReadEnvVarIntOrDefault(util.PodWaitSecondsEnv, 3)
	retrials := util.ReadEnvVarIntOrDefault(util.PodWaitRetriesEnv, 5)
	log = log.With("statefulset", objectKey(namespace, name))

	time.Sleep(time.Duration(util.ReadEnvVarIntOrDefault(util.K8sCacheRefreshEnv, util.DefaultK8sCacheRefreshTimeSeconds)) * time.Second)

	return util.DoAndRetry(func() (string, bool) {
		set := &appsv1.StatefulSet{}
		err := k.client.Get(context.TODO(), objectKey(namespace, name), set)

		if err != nil {
			return fmt.Sprintf("Error reading statefulset %s: %s", objectKey(namespace, name), err), false
		}

		replicas := *set.Spec.Replicas
		allUpdated := replicas == set.Status.UpdatedReplicas
		allReady := replicas == set.Status.ReadyReplicas

		return fmt.Sprintf("Replicas count: total %d, updated %d, ready %d", *set.Spec.Replicas,
			set.Status.UpdatedReplicas, set.Status.ReadyReplicas), allUpdated && allReady
	}, log, retrials, waitSeconds)
}

func (k *KubeHelper) deleteStatefulSet(key client.ObjectKey) error {
	set := &appsv1.StatefulSet{}
	if err := k.client.Get(context.TODO(), key, set); err != nil {
		return err
	}

	if err := k.client.Delete(context.TODO(), set); err != nil {
		return err
	}
	return nil
}

func (k *KubeHelper) createOrUpdateService(desiredService corev1.Service, log *zap.SugaredLogger) error {
	log = log.With("service", desiredService.ObjectMeta.Name)
	namespacedName := objectKey(desiredService.ObjectMeta.Namespace, desiredService.ObjectMeta.Name)

	existingService, err := k.serviceClient.Get(namespacedName)
	method := ""
	if err != nil {
		if apiErrors.IsNotFound(err) {
			err = k.serviceClient.Create(desiredService)
			if err != nil {
				return err
			}
		} else {
			return err
		}
		method = "Created"
	} else {
		mergedService := service.Merge(existingService, desiredService)
		err = k.serviceClient.Update(mergedService)
		if err != nil {
			return err
		}
		method = "Updated"
	}

	log.Debugw(fmt.Sprintf("%s Service", method), "type", desiredService.Spec.Type, "port", desiredService.Spec.Ports[0])
	return nil
}

// readProjectConfig returns a "Project" config which is a ConfigMap with a series of attributes
// like `projectName`, `baseUrl` and a series of attributes related to SSL.
func (k *KubeHelper) readProjectConfig(namespace, name string) (*mdbv1.ProjectConfig, error) {
	data, err := k.configmapClient.GetData(objectKey(namespace, name))
	if err != nil {
		return nil, err
	}

	baseURL, ok := data[util.OmBaseUrl]
	if !ok {
		return nil, fmt.Errorf(`Property "%s" is not specified in config map %s`, util.OmBaseUrl, name)
	}
	projectName := data[util.OmProjectName]
	orgID := data[util.OmOrgId]

	sslRequireValid := true
	sslRequireValidData, ok := data[util.SSLRequireValidMMSServerCertificates]
	if ok {
		sslRequireValid = sslRequireValidData != "false"
	}

	sslCaConfigMap, ok := data[util.SSLMMSCAConfigMap]
	caFile := ""
	if ok {
		cacrt, err := k.configmapClient.GetData(objectKey(namespace, sslCaConfigMap))
		if err != nil {
			return nil, fmt.Errorf("Could not read the specified ConfigMap %s/%s (%e)", namespace, sslCaConfigMap, err)
		}
		for k, v := range cacrt {
			if k == CaCertMMS {
				caFile = v
				break
			}
		}
	}

	var useCustomCA bool
	useCustomCAData, ok := data[util.UseCustomCAConfigMap]
	if ok {
		useCustomCA = useCustomCAData != "false"
	}

	return &mdbv1.ProjectConfig{
		BaseURL:     baseURL,
		ProjectName: projectName,
		OrgID:       orgID,

		// Options related with SSL on OM side.
		SSLProjectConfig: mdbv1.SSLProjectConfig{
			// Relevant to
			// + operator (via golang http configuration)
			// + curl (via command line argument [--insecure])
			// + automation-agent (via env variable configuration [SSL_REQUIRE_VALID_MMS_CERTIFICATES])
			// + EnvVarSSLRequireValidMMSCertificates and automation agent option
			// + -sslRequireValidMMSServerCertificates
			SSLRequireValidMMSServerCertificates: sslRequireValid,

			// SSLMMSCAConfigMap is name of the configmap with the CA. This CM
			// will be mounted in the database Pods.
			SSLMMSCAConfigMap: sslCaConfigMap,

			// This needs to be passed for the operator itself to be able to
			// recognize the CA -- as it can't be mounted on an already running
			// Pod.
			SSLMMSCAConfigMapContents: caFile,
		},

		AuthMode:    data[util.OmAuthMode],
		Credentials: data[util.OmCredentials],

		UseCustomCA: useCustomCA,
	}, nil
}

func (k *KubeHelper) readCredentials(namespace, name string) (*Credentials, error) {
	location := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	secret, err := k.readSecret(location)
	if err != nil {
		return nil, fmt.Errorf("Error getting secret %s: %s", name, err)
	}

	publicAPIKey, ok := secret[util.OmPublicApiKey]
	if !ok {
		return nil, fmt.Errorf("Property \"%s\" is not specified in secret %s", util.OmPublicApiKey, name)
	}
	user, ok := secret[util.OmUser]
	if !ok {
		return nil, fmt.Errorf("Property \"%s\" is not specified in secret %s", util.OmUser, name)
	}

	return &Credentials{
		User:         user,
		PublicAPIKey: publicAPIKey,
	}, nil
}

func (k *KubeHelper) readAgentApiKeyForProject(namespace, agentKeySecretName string) (string, error) {
	secret, err := k.readSecret(objectKey(namespace, agentKeySecretName))
	if err != nil {
		return "", err
	}

	key, ok := secret[util.OmAgentApiKey]
	if !ok {
		return "", fmt.Errorf("Could not find key \"%s\" in secret %s", util.OmAgentApiKey, agentKeySecretName)
	}

	return strings.TrimSuffix(key, "\n"), nil
}

func (k *KubeHelper) readSecret(nsName client.ObjectKey) (map[string]string, error) {
	secret := &corev1.Secret{}
	e := k.client.Get(context.TODO(), nsName, secret)
	if e != nil {
		return nil, e
	}

	secrets := make(map[string]string)
	for k, v := range secret.Data {
		secrets[k] = strings.TrimSuffix(string(v[:]), "\n")
	}
	return secrets, nil
}

// readSecretKey returns the value stored with the corresponding key from the provided
// ObjectKey
func (k *KubeHelper) readSecretKey(nsName client.ObjectKey, key string) (string, error) {
	secretData, err := k.readSecret(nsName)
	if err != nil {
		return "", err
	}
	if val, ok := secretData[key]; !ok {
		return "", fmt.Errorf("secret/%s did not contain the key %s", nsName.Name, key)
	} else {
		return val, nil
	}
}

// computeConfigMap fetches the existing config map and applies the computation function to it and pushes changes back
// The computation function is expected to update the data in config map or return false if no update/create is needed
// (Name for the function is chosen as an analogy to Map.compute() function in Java)
func (k *KubeHelper) computeConfigMap(nsName client.ObjectKey, callback func(*corev1.ConfigMap) bool, owner Updatable) error {
	existingConfigMap, err := k.configmapClient.Get(objectKey(nsName.Namespace, nsName.Name))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			existingConfigMap = configmap.Builder().
				SetName(nsName.Name).
				SetNamespace(nsName.Namespace).
				SetOwnerReferences(baseOwnerReference(owner)).
				Build()

			if !callback(&existingConfigMap) {
				return nil
			}
			if err := k.configmapClient.Create(existingConfigMap); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		if !callback(&existingConfigMap) {
			return nil
		}
		if err := k.configmapClient.Update(existingConfigMap); err != nil {
			return err
		}
	}
	return nil
}

// TODO: leave this because the OM controller might end up using this:
// https://github.com/10gen/ops-manager-kubernetes/pull/469/files#r340725250
//func (k *KubeHelper) createOrUpdateConfigMap(nsName client.ObjectKey, data map[string]string, owner Updatable) error {
//existingConfigMap := &corev1.ConfigMap{}
//newConfigMap := &corev1.ConfigMap{
//Data: data,
//ObjectMeta: metav1.ObjectMeta{
//Name:            nsName.Name,
//Namespace:       nsName.Namespace,
//OwnerReferences: baseOwnerReference(owner),
//}}

//if err := k.client.Get(context.TODO(), nsName, existingConfigMap); err != nil {
//if apiErrors.IsNotFound(err) {
//if err = k.client.Create(context.TODO(), newConfigMap); err != nil {
//return err
//}
//} else {
//return err
//}
//} else {
//if err = k.client.Update(context.TODO(), newConfigMap); err != nil {
//return err
//}
//}
//return nil
//}

// CreateOrUpdateSecret will create (if it does not exist) or update (if it does) a secret.
func (k *KubeHelper) createOrUpdateSecret(name, namespace string, pemFiles *pemCollection, labels map[string]string) error {
	secret := &corev1.Secret{}
	err := k.client.Get(context.TODO(), objectKey(namespace, name), secret)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// assume the secret was not found, need to create it
			// passing 'nil' as an owner reference as we haven't decided yet if we need to remove certificates
			return k.createSecret(objectKey(namespace, name), pemFiles.merge(), labels, nil)
		}
		return err
	}

	// if the secret already exists, it might contain entries that we want merged:
	// for each Pod we'll have the key and the certificate, but we might also have the
	// certificate added in several stages. If a certificate/key exists, and this

	pemData := pemFiles.mergeWith(secret.Data)
	secret.StringData = pemData
	return k.client.Update(context.TODO(), secret)
}

// createSecret creates the secret. 'data' must either 'map[string][]byte' or 'map[string]string'
func (k *KubeHelper) createSecret(nsName client.ObjectKey, data interface{}, labels map[string]string, owner Updatable) error {
	secret := &corev1.Secret{}
	secret.ObjectMeta = metav1.ObjectMeta{
		Name:      nsName.Name,
		Namespace: nsName.Namespace,
	}
	if len(labels) > 0 {
		secret.ObjectMeta.Labels = labels
	}
	if owner != nil {
		secret.ObjectMeta.OwnerReferences = baseOwnerReference(owner)
	}

	switch v := data.(type) {
	case map[string][]byte:
		secret.Data = v
	case map[string]string:
		secret.StringData = v
	default:
		panic("Dev error: wrong type is passed!")
	}

	return k.client.Create(context.TODO(), secret)
}

// deleteSecret deletes the secret. Unfortunately we cannot use 'client.Delete' directly from clients as
// it requires the object
func (k *KubeHelper) deleteSecret(key client.ObjectKey) error {
	secret := &corev1.Secret{}
	if err := k.client.Get(context.TODO(), key, secret); err != nil {
		return err
	}

	if err := k.client.Delete(context.TODO(), secret); err != nil {
		return err
	}
	return nil
}

// validateSelfManagedSSLCertsForStatefulSet ensures that a stateful set using
// user-provided certificates has all of the relevant certificates in place.
func (ss *StatefulSetHelper) validateSelfManagedSSLCertsForStatefulSet(k *KubeHelper, secretName string) reconcileStatus {
	// A "Certs" attribute has been provided
	// This means that the customer has provided with a secret name they have
	// already populated with the certs and keys for this deployment.
	// Because of the async nature of Kubernetes, this object might not be ready yet,
	// in which case, we'll keep reconciling until the object is created and is correct.
	if notReadyCerts := k.verifyCertificatesForStatefulSet(ss, secretName); notReadyCerts > 0 {
		return failed("The secret object '%s' does not contain all the certificates needed."+
			"Required: %d, contains: %d", secretName,
			ss.Replicas,
			ss.Replicas-notReadyCerts,
		)
	}

	if err := k.validateCertificates(secretName, ss.Namespace, false); err != nil {
		return failedErr(err)
	}

	return ok()
}

// ensureOperatorManagedSSLCertsForStatefulSet ensures that a stateful set
// using operator-managed certificates has all of the relevant certificates in
// place.
func (ss *StatefulSetHelper) ensureOperatorManagedSSLCertsForStatefulSet(k *KubeHelper, secretName string, log *zap.SugaredLogger) reconcileStatus {
	certsNeedApproval := false

	if err := k.validateCertificates(secretName, ss.Namespace, true); err != nil {
		return failedErr(err)
	}

	if notReadyCerts := k.verifyCertificatesForStatefulSet(ss, secretName); notReadyCerts > 0 {
		// If the Kube CA and the operator are responsible for the certificates to be
		// ready and correctly stored in the secret object, and this secret is not "complete"
		// we'll go through the process of creating the CSR, wait for certs approval and then
		// creating a correct secret with the certificates and keys.

		// For replica set we need to create rs.Spec.Replicas certificates, one per each Pod
		fqdns, podnames := ss.getDNSNames()

		// pemFiles will store every key (during the CSR creation phase) and certificate
		// both can happen on different reconciliation stages (CSR and keys are created, then
		// reconciliation, then certs are obtained from the CA). If this happens we need to
		// store the keys in the final secret, that will be updated with the certs, once they
		// are issued by the CA.
		pemFiles := newPemCollection()

		for idx, host := range fqdns {
			csr, err := k.readCSR(podnames[idx], ss.Namespace)
			additionalCertDomains := ss.getAdditionalCertDomainsForMember(idx)
			if err != nil {
				certsNeedApproval = true
				hostnames := []string{host, podnames[idx]}
				hostnames = append(hostnames, additionalCertDomains...)
				key, err := k.createTlsCsr(podnames[idx], ss.Namespace, hostnames, host)
				if err != nil {
					return failed("Failed to create CSR, %s", err)
				}

				pemFiles.addPrivateKey(podnames[idx], string(key))
			} else if !checkCSRHasRequiredDomains(csr, additionalCertDomains) {
				log.Infow(
					"Certificate request does not have all required domains",
					"requiredDomains", additionalCertDomains,
					"host", host,
				)
				return pending("Certificate request for " + host + " doesn't have all required domains. Please manually remove the CSR in order to proceed.")
			} else if checkCSRWasApproved(csr.Status.Conditions) {
				log.Infof("Certificate for Pod %s -> Approved", host)
				pemFiles.addCertificate(podnames[idx], string(csr.Status.Certificate))
			} else {
				log.Infof("Certificate for Pod %s -> Waiting for Approval", host)
				certsNeedApproval = true
			}
		}

		// once we are here we know we have built everything we needed
		// This "secret" object corresponds to the certificates for this statefulset
		labels := make(map[string]string)
		labels["mongodb/secure"] = "certs"
		labels["mongodb/operator"] = "certs." + secretName

		// note that createOrUpdateSecret modifies pemFiles in place by merging
		// in the existing values in the secret
		err := k.createOrUpdateSecret(secretName, ss.Namespace, pemFiles, labels)
		if err != nil {
			// If we have an error creating or updating the secret, we might lose
			// the keys, in which case we return an error, to make it clear what
			// the error was to customers -- this should end up in the status
			// message.
			return failed("Failed to create or update the secret: %s", err)
		}

		certsHash, err := pemFiles.getHash()
		if err != nil {
			log.Errorw("Could not hash PEM files", "err", err)
			return failedErr(err)
		}
		ss.SetCertificateHash(certsHash)
	}

	if certsNeedApproval {
		return pending("Not all certificates have been approved by Kubernetes CA for %s", ss.Name)
	}
	return ok()
}

// readPemHashFromSecret reads the existing Pem from
// the secret that stores this StatefulSet's Pem collection.
func (s *StatefulSetHelper) readPemHashFromSecret() string {
	secretName := s.Name + "-cert"
	secretData, err := s.Helper.readSecret(objectKey(s.Namespace, secretName))
	if err != nil {
		s.Logger.Infof("secret %s doesn't exist yet", secretName)
		return ""
	}
	pemCollection := newPemCollection()
	for k, v := range secretData {
		pemCollection.mergeEntry(k, newPemFileFrom(v))
	}
	pemHash, err := pemCollection.getHash()
	if err != nil {
		s.Logger.Errorf("error computing pem hash: %s", err)
		return ""
	}
	return pemHash
}

// ensureSSLCertsForStatefulSet contains logic to ensure that all of the
// required SSL certs for a StatefulSet object exist.
func (k *KubeHelper) ensureSSLCertsForStatefulSet(ss *StatefulSetHelper, log *zap.SugaredLogger) reconcileStatus {
	if !ss.IsTLSEnabled() {
		// if there's no SSL certs to generate, return
		return ok()
	}

	secretName := ss.Name + "-cert"
	if ss.Security.TLSConfig.CA != "" {
		return ss.validateSelfManagedSSLCertsForStatefulSet(k, secretName)
	}
	return ss.ensureOperatorManagedSSLCertsForStatefulSet(k, secretName, log)
}

// validateCertificate verifies the Secret containing the certificates and the keys is valid.
func (k *KubeHelper) validateCertificates(name, namespace string, destroy bool) error {
	secret := &corev1.Secret{}
	err := k.client.Get(context.TODO(), objectKey(namespace, name), secret)
	if err == nil {
		// Validate that the secret contains the keys, if it contains the certs.
		for _, value := range secret.Data {
			pemFile := newPemFileFromData(value)
			if !pemFile.isValid() {
				// if this is an invalid secret (it does not have a key), remove the
				// secret and start from scratch
				if destroy {
					err := k.client.Delete(context.TODO(), secret)
					if err != nil {
						return fmt.Errorf("The secret %s is invalid, as it does not contain valid private keys for the certificates. "+
							"We tried to remove it but another error occured. %s", name, err)
					}
				}

				return fmt.Errorf("The Secret %s containing certificates has been removed, because it was invalid. "+
					"Remove the matching CSRs manually to let Operator generate them again.", name)
			}
		}
	}

	return nil
}

func (k *KubeHelper) verifyClientCertificatesForAgents(name, namespace string) int {
	secret := &corev1.Secret{}
	err := k.client.Get(context.TODO(), objectKey(namespace, name), secret)
	if err != nil {
		return NumAgents
	}

	certsNotReady := 0
	for _, agentSecretKey := range []string{util.AutomationAgentPemSecretKey, util.MonitoringAgentPemSecretKey, util.BackupAgentPemSecretKey} {
		additionalDomains := []string{} // agents have no additional domains
		if !isValidPemSecret(secret, agentSecretKey, additionalDomains) {
			certsNotReady++
		}
	}

	return certsNotReady
}

func isValidPemSecret(secret *corev1.Secret, key string, additionalDomains []string) bool {
	data, ok := secret.Data[key]
	if !ok {
		return false
	}

	pemFile := newPemFileFromData(data)
	if !pemFile.isComplete() {
		return false
	}

	cert, err := pemFile.parseCertificate()
	if err != nil {
		return false
	}

	for _, domain := range additionalDomains {
		if !util.ContainsString(cert.DNSNames, domain) {
			return false
		}
	}
	return true
}

// verifyCertificatesForStatefulSet will return the number of certificates which are
// not ready (approved and issued) yet, if all the certificates and keys required for
// the StatefulSet `ss` exist in the secret with name `secretName`
func (k *KubeHelper) verifyCertificatesForStatefulSet(ss *StatefulSetHelper, secretName string) int {
	secret := &corev1.Secret{}
	err := k.client.Get(context.TODO(), objectKey(ss.Namespace, secretName), secret)
	if err != nil {
		return ss.Replicas
	}

	_, podnames := ss.getDNSNames()
	certsNotReady := 0

	for i, pod := range podnames {
		pem := fmt.Sprintf("%s-pem", pod)
		additionalDomains := ss.getAdditionalCertDomainsForMember(i)
		if !isValidPemSecret(secret, pem, additionalDomains) {
			certsNotReady++
		}
	}

	return certsNotReady
}

// EnvVars returns a list of corev1.EnvVar which should be passed
// to the container running Ops Manager
func opsManagerConfigurationToEnvVars(m mdbv1.MongoDBOpsManager) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for name, value := range m.Spec.Configuration {
		envVars = append(envVars, corev1.EnvVar{
			Name: mdbv1.ConvertNameToEnvVarFormat(name), Value: value,
		})
	}
	return envVars
}
