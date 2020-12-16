package operator

import (
	"context"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/certs"

	enterprisesvc "github.com/10gen/ops-manager-kubernetes/pkg/kube/service"
	enterprisests "github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"net/url"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// KubeHelper is the helper for dealing with Kubernetes. If any Kubernetes logic requires more than some trivial operation
// - it should be put here
type KubeHelper struct {
	client kubernetesClient.Client
}

// NewKubeHelper constructs an instance of KubeHelper with all clients initialized
// using the same instance of client
func NewKubeHelper(client client.Client) KubeHelper {
	return KubeHelper{
		client: kubernetesClient.NewClient(client),
	}
}

type AuthMode string

const (
	NumAgents                    = 3
	externalConnectivityPortName = "external-connectivity-port"
	backupPortName               = "backup-port"
)

// StatefulSetHelperCommon is the basic struct the same for all Statefulset helpers (MongoDB, OpsManager)
type StatefulSetHelperCommon struct {
	// Attributes that are part of StatefulSet
	Owner     v1.CustomResourceReadWriter
	Name      string
	Service   string
	Namespace string

	// ClusterDomain is the cluster name that's usually 'cluster.local' but it
	// can be changed by the customer.
	ClusterDomain            string
	Replicas                 int
	ServicePort              int32
	Version                  string
	ContainerName            string
	PodSpec                  *mdbv1.PodSpecWrapper
	StatefulSetConfiguration *mdbv1.StatefulSetConfiguration

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
	PodVars    *PodEnvVars

	StartupOptions mdbv1.StartupParameters

	ResourceType mdbv1.ResourceType

	// The following attributes are not part of StatefulSet object

	// ExposedExternally sets this StatefulSetHelper to receive a `Service` that will allow it to be
	// visible from outside the Kubernetes cluster.
	ExposedExternally bool

	Project                   mdbv1.ProjectConfig
	Security                  *mdbv1.Security
	ReplicaSetHorizons        []mdbv1.MongoDBHorizonConfig
	CertificateHash           string
	CurrentAgentAuthMechanism string
}

func (ss StatefulSetHelper) GetOwnerRefs() []metav1.OwnerReference {
	return baseOwnerReference(ss.Owner)
}

func (ss StatefulSetHelper) GetName() string {
	return ss.Name
}

func (ss StatefulSetHelper) GetService() string {
	return ss.Service
}

func (ss StatefulSetHelper) GetNamespace() string {
	return ss.Namespace
}

func (ss StatefulSetHelper) GetReplicas() int {
	return ss.Replicas
}

func (ss StatefulSetHelper) GetBaseUrl() string {
	if ss.PodVars == nil {
		return ""
	}
	return ss.PodVars.BaseURL
}

func (ss StatefulSetHelper) GetProjectID() string {
	if ss.PodVars == nil {
		return ""
	}
	return ss.PodVars.ProjectID
}

func (ss StatefulSetHelper) GetUser() string {
	if ss.PodVars == nil {
		return ""
	}
	return ss.PodVars.User
}

func (ss StatefulSetHelper) GetLogLevel() string {
	if ss.PodVars == nil {
		return ""
	}
	return string(ss.PodVars.LogLevel)
}

func (ss StatefulSetHelper) SSLRequireValidMMSServerCertificates() bool {
	if ss.PodVars == nil {
		return false
	}
	return ss.PodVars.SSLRequireValidMMSServerCertificates
}

func (ss StatefulSetHelper) GetSSLMMSCAConfigMap() string {
	if ss.PodVars == nil {
		return ""
	}
	return ss.PodVars.SSLMMSCAConfigMap
}

func (ss StatefulSetHelper) GetCertificateHash() string {
	if ss.PodVars == nil {
		return ""
	}
	return ss.CertificateHash
}

func (ss StatefulSetHelper) GetPodSpec() *mdbv1.PodSpecWrapper {
	return ss.PodSpec
}

func (ss StatefulSetHelper) GetSecurity() *mdbv1.Security {
	return ss.Security
}

func (ss StatefulSetHelper) IsPersistent() *bool {
	return ss.Persistent
}

func (ss StatefulSetHelper) GetCurrentAgentAuthMechanism() string {
	return ss.CurrentAgentAuthMechanism
}

func (ss StatefulSetHelper) GetStartupParameters() mdbv1.StartupParameters {
	return ss.StartupOptions
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
	Spec omv1.MongoDBOpsManagerSpec

	// Annotations passed to the Ops Manager resource
	Annotations map[string]string

	// Name of the secret containing the secret to mount.
	HTTPSCertSecretName string

	// Name of the ConfigMap with a CA that verifies the AppDB TLS certs
	AppDBTlsCAConfigMapName string

	EnvVars []corev1.EnvVar

	// AppDBConnectionStringHash is the hash of the contents of the AppDB Connection String
	// if this changes in the secret, a rolling restart must be triggered.
	AppDBConnectionStringHash string
}

func (s OpsManagerStatefulSetHelper) GetOwnerRefs() []metav1.OwnerReference {
	return baseOwnerReference(s.Owner)
}

func (s OpsManagerStatefulSetHelper) GetNamespace() string {
	return s.Namespace
}

func (s OpsManagerStatefulSetHelper) GetReplicas() int {
	return s.Replicas
}

func (s OpsManagerStatefulSetHelper) GetOwnerName() string {
	if s.Owner == nil {
		return ""
	}
	return s.Owner.GetName()
}

func (s OpsManagerStatefulSetHelper) GetHTTPSCertSecretName() string {
	return s.HTTPSCertSecretName
}

func (s OpsManagerStatefulSetHelper) GetAppDBTlsCAConfigMapName() string {
	return s.AppDBTlsCAConfigMapName
}

func (s OpsManagerStatefulSetHelper) GetAppDBConnectionStringHash() string {
	return s.AppDBConnectionStringHash
}

func (s OpsManagerStatefulSetHelper) GetEnvVars() []corev1.EnvVar {
	return s.EnvVars
}

func (s OpsManagerStatefulSetHelper) GetVersion() string {
	return s.Version
}

func (s OpsManagerStatefulSetHelper) GetName() string {
	return s.Name
}

func (s OpsManagerStatefulSetHelper) GetService() string {
	return s.Service
}

type BackupStatefulSetHelper struct {
	OpsManagerStatefulSetHelper

	HeadDbPersistenceConfig *mdbv1.PersistenceConfig
}

func (s BackupStatefulSetHelper) GetHeadDbPersistenceConfig() *mdbv1.PersistenceConfig {
	return s.HeadDbPersistenceConfig
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
func NewStatefulSetHelper(obj v1.CustomResourceReadWriter) *StatefulSetHelper {
	var containerName string
	var mongodbSpec mdbv1.MongoDbSpec
	switch v := obj.(type) {
	case *mdbv1.MongoDB:
		containerName = util.DatabaseContainerName
		mongodbSpec = v.Spec
	case *omv1.MongoDBOpsManager:
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

func (k *KubeHelper) NewOpsManagerStatefulSetHelper(opsManager omv1.MongoDBOpsManager) *OpsManagerStatefulSetHelper {
	_, port := opsManager.GetSchemePort()
	tlsSecret := ""
	if opsManager.Spec.Security != nil {
		tlsSecret = opsManager.Spec.Security.TLS.SecretRef.Name
	}

	return &OpsManagerStatefulSetHelper{
		StatefulSetHelperCommon: StatefulSetHelperCommon{
			Owner:                    &opsManager,
			Name:                     opsManager.GetName(),
			Namespace:                opsManager.GetNamespace(),
			ContainerName:            util.OpsManagerContainerName,
			Replicas:                 opsManager.Spec.Replicas,
			Helper:                   k,
			ServicePort:              int32(port),
			Version:                  opsManager.Spec.Version,
			Service:                  opsManager.SvcName(),
			StatefulSetConfiguration: opsManager.Spec.StatefulSetConfiguration,
		},
		Spec:                    opsManager.Spec,
		EnvVars:                 opsManagerConfigurationToEnvVars(opsManager),
		HTTPSCertSecretName:     tlsSecret,
		AppDBTlsCAConfigMapName: opsManager.Spec.AppDB.GetCAConfigMapName(),
	}
}

func (k *KubeHelper) NewBackupStatefulSetHelper(opsManager omv1.MongoDBOpsManager) *BackupStatefulSetHelper {
	helper := BackupStatefulSetHelper{
		OpsManagerStatefulSetHelper: *k.NewOpsManagerStatefulSetHelper(opsManager),
	}
	helper.Name = opsManager.BackupStatefulSetName()
	helper.ContainerName = util.BackupDaemonContainerName
	helper.Service = opsManager.BackupSvcName()
	helper.ServicePort = 8443
	helper.Replicas = 1
	// unset the default that was configured with Ops Manager
	helper.StatefulSetConfiguration = nil

	if opsManager.Spec.Backup != nil {
		helper.StatefulSetConfiguration = opsManager.Spec.Backup.StatefulSetConfiguration
	}
	if opsManager.Spec.Backup.HeadDB != nil {
		helper.HeadDbPersistenceConfig = opsManager.Spec.Backup.HeadDB
	}
	return &helper
}

// SetName can override the value of `StatefulSetHelper.Name` which is set to
// `owner.GetName()` initially.
func (s *StatefulSetHelper) SetName(name string) *StatefulSetHelper {
	s.Name = name
	return s
}
func (s *StatefulSetHelper) SetOwner(obj v1.CustomResourceReadWriter) *StatefulSetHelper {
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

func (s *StatefulSetHelper) SetPodVars(podVars *PodEnvVars) *StatefulSetHelper {
	s.PodVars = podVars
	return s
}

func (s *StatefulSetHelper) SetStartupParameters(parameters mdbv1.StartupParameters) *StatefulSetHelper {
	s.StartupOptions = parameters
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

func (s *StatefulSetHelper) SetStatefulSetConfiguration(stsConfiguration *mdbv1.StatefulSetConfiguration) *StatefulSetHelper {
	s.StatefulSetConfiguration = stsConfiguration
	return s
}

func (s StatefulSetHelper) BuildStatefulSet() (appsv1.StatefulSet, error) {
	sts, err := buildStatefulSet(s)

	if err != nil {
		return appsv1.StatefulSet{}, fmt.Errorf("error building %s StatefulSet: %v", s.Name, err)
	}
	return sts, nil
}

func (s StatefulSetHelper) BuildAppDbStatefulSet() (appsv1.StatefulSet, error) {
	sts, err := buildAppDbStatefulSet(s)
	if err != nil {
		return appsv1.StatefulSet{}, fmt.Errorf("error building %s StatefulSet: %v", s.Name, err)
	}
	return sts, nil
}

// CreateOrUpdateInKubernetes creates (updates if it exists) the StatefulSet with its Service.
// It returns any errors coming from Kubernetes API.
func (s StatefulSetHelper) CreateOrUpdateInKubernetes(stsGetUpdateCreator statefulset.GetUpdateCreator, serviceGetUpdateCreator service.GetUpdateCreator) error {
	sts, err := s.BuildStatefulSet()
	if err != nil {
		return fmt.Errorf("error building stateful set: %v", err)
	}

	set, err := enterprisests.CreateOrUpdateStatefulset(stsGetUpdateCreator,
		s.Namespace,
		s.Logger,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := objectKey(s.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, internalService, s.Logger)
	if err != nil {
		return err
	}

	if s.ExposedExternally {
		namespacedName := objectKey(s.Namespace, set.Spec.ServiceName+"-external")
		externalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeNodePort})
		err = enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, externalService, s.Logger)
	}

	return err
}

// BuildStatefulSet builds the StatefulSet for the Ops Manager resource.
// TODO: currently only the spec.statefulSet.spec.template is merged. Other in the custom
// spec are not used.
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

func (s *OpsManagerStatefulSetHelper) SetAnnotations(annotations map[string]string) *OpsManagerStatefulSetHelper {
	s.Annotations = annotations
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

func (s *OpsManagerStatefulSetHelper) SetAppDBConnectionStringHash(hash string) *OpsManagerStatefulSetHelper {
	s.AppDBConnectionStringHash = hash
	return s
}

func (s OpsManagerStatefulSetHelper) SetBackupService(serviceGetUpdateCreator service.GetUpdateCreator, externalService corev1.Service, serviceName string) error {

	backupSvcPort, err := s.Spec.BackupSvcPort()
	if err != nil {
		return fmt.Errorf("can't parse queryable backup port: %s", err)
	}

	// If external connectivity is already configured, add a port to externalService
	if s.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		externalService.Spec.Ports[0].Name = externalConnectivityPortName
		externalService.Spec.Ports = append(externalService.Spec.Ports, corev1.ServicePort{
			Name: backupPortName,
			Port: backupSvcPort,
		})
		return enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, externalService, s.Logger)
	}
	// Otherwise create a new service
	namespacedName := objectKey(s.Namespace, serviceName+"-backup")
	backupService := buildService(namespacedName, s.Owner, "ops-manager-backup", backupSvcPort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeLoadBalancer})

	return enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, backupService, s.Logger)

}

func (s OpsManagerStatefulSetHelper) CreateOrUpdateInKubernetes(stsGetUpdateCreator statefulset.GetUpdateCreator, serviceGetUpdateCreator service.GetUpdateCreator) error {
	sts, err := s.BuildStatefulSet()
	if err != nil {
		return fmt.Errorf("error building OpsManager stateful set: %v", err)
	}

	set, err := enterprisests.CreateOrUpdateStatefulset(stsGetUpdateCreator,
		s.Namespace,
		s.Logger,
		&sts,
	)
	if err != nil {
		return err
	}

	namespacedName := objectKey(s.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, internalService, s.Logger)
	if err != nil {
		return err
	}

	externalService := corev1.Service{}
	if s.Spec.MongoDBOpsManagerExternalConnectivity != nil {
		namespacedName := objectKey(s.Namespace, set.Spec.ServiceName+"-ext")
		externalService = buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, *s.Spec.MongoDBOpsManagerExternalConnectivity)
		err = enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, externalService, s.Logger)
		if err != nil {
			return err
		}
	}

	// Need to create queryable backup service
	if s.Spec.Backup.Enabled {
		return s.SetBackupService(serviceGetUpdateCreator, externalService, set.Spec.ServiceName)
	}

	return err
}

func (s BackupStatefulSetHelper) CreateOrUpdateInKubernetes(stsGetUpdateCreator statefulset.GetUpdateCreator, serviceGetUpdateCreator service.GetUpdateCreator) (bool, error) {
	sts, err := s.BuildStatefulSet()
	if err != nil {
		return false, fmt.Errorf("error building stateful set: %v", err)
	}

	set, err := enterprisests.CreateOrUpdateStatefulset(
		stsGetUpdateCreator,
		s.Namespace,
		s.Logger,
		&sts,
	)

	if err != nil {
		// Check if it is a k8s error or a custom one
		if _, ok := err.(enterprisests.StatefulSetCantBeUpdatedError); !ok {
			return false, err
		}
		// In this case, we delete the old Statefulset
		s.Logger.Debug("Deleting the old backup stateful set and creating a new one")
		stsNamespacedName := kube.ObjectKey(s.Namespace, s.Name)
		err = s.Helper.client.DeleteStatefulSet(stsNamespacedName)
		if err != nil {
			return false, fmt.Errorf("failed while trying to delete previous backup daemon statefulset: %s", err)
		}
		return true, nil
	}
	namespacedName := objectKey(s.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, internalService, s.Logger)

	if err != nil {
		return false, err
	}

	return false, nil
}

// CreateOrUpdateAppDBInKubernetes creates the StatefulSet specific for AppDB.
func (s *StatefulSetHelper) CreateOrUpdateAppDBInKubernetes(stsGetUpdateCreator statefulset.GetUpdateCreator, serviceGetUpdateCreator service.GetUpdateCreator) error {
	appDbSts, err := s.BuildAppDbStatefulSet()
	if err != nil {
		return fmt.Errorf("error building stateful set: %v", err)
	}

	set, err := enterprisests.CreateOrUpdateStatefulset(stsGetUpdateCreator,
		s.Namespace,
		s.Logger,
		&appDbSts,
	)
	if err != nil {
		return err
	}

	namespacedName := objectKey(s.Namespace, set.Spec.ServiceName)
	internalService := buildService(namespacedName, s.Owner, set.Spec.ServiceName, s.ServicePort, omv1.MongoDBOpsManagerServiceDefinition{Type: corev1.ServiceTypeClusterIP})
	err = enterprisesvc.CreateOrUpdateService(serviceGetUpdateCreator, internalService, s.Logger)
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

func (s *StatefulSetHelper) SetCurrentAgentAuthMechanism(agentAuth string) *StatefulSetHelper {
	s.CurrentAgentAuthMechanism = agentAuth
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
func (s *StatefulSetHelper) needToPublishStateFirst(stsGetter statefulset.Getter, log *zap.SugaredLogger) bool {
	namespacedName := objectKey(s.Namespace, s.Name)
	currentSts, err := stsGetter.GetStatefulSet(namespacedName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// No need to publish state as this is a new StatefulSet
			log.Debugf("New StatefulSet %s", namespacedName)
			return false
		}

		log.Debugw(fmt.Sprintf("Error getting StatefulSet %s", namespacedName), "error", err)
		return false
	}

	volumeMounts := currentSts.Spec.Template.Spec.Containers[0].VolumeMounts
	if s.Security != nil {
		if !s.Security.TLSConfig.Enabled && volumeMountWithNameExists(volumeMounts, util.SecretVolumeName) {
			log.Debug("About to set `security.tls.enabled` to false. automationConfig needs to be updated first")
			return true
		}

		if s.Security.TLSConfig.CA == "" && volumeMountWithNameExists(volumeMounts, ConfigMapVolumeCAName) {
			log.Debug("About to set `security.tls.CA` to empty. automationConfig needs to be updated first")
			return true
		}
	}

	if s.PodVars.SSLMMSCAConfigMap == "" && volumeMountWithNameExists(volumeMounts, CaCertName) {
		log.Debug("About to set `SSLMMSCAConfigMap` to empty. automationConfig needs to be updated first")
		return true
	}

	if s.Security.GetAgentMechanism(s.CurrentAgentAuthMechanism) != util.X509 && volumeMountWithNameExists(volumeMounts, util.AgentSecretName) {
		log.Debug("About to set `project.AuthMode` to empty. automationConfig needs to be updated first")
		return true
	}

	if int32(s.Replicas) < *currentSts.Spec.Replicas {
		log.Debug("Scaling down operation. automationConfig needs to be updated first")
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

func (k KubeHelper) readSecret(nsName client.ObjectKey) (map[string]string, error) {
	s, err := k.client.GetSecret(nsName)
	if err != nil {
		return nil, err
	}

	// TODO: can we delete this?
	secretStringData := make(map[string]string)
	for k, v := range s.Data {
		secretStringData[k] = strings.TrimSuffix(string(v[:]), "\n")
	}
	return secretStringData, nil
}

// computeSecret fetches the existing Secret and applies the computation function to it and pushes changes back.
// The computation function is expected to update the data in Secret or return false if no update/create is needed
// Returns the final Secret (could be the initial one or the one after the update)
// (Name for the function is chosen as an analogy to Map.compute() function in Java)
func (k *KubeHelper) computeSecret(nsName client.ObjectKey, callback func(*corev1.Secret) bool, owner v1.CustomResourceReadWriter) (corev1.Secret, error) {
	existingSecret, err := k.client.GetSecret(nsName)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			newSecret := secret.Builder().
				SetName(nsName.Name).
				SetNamespace(nsName.Namespace).
				SetOwnerReferences(baseOwnerReference(owner)).
				Build()

			if !callback(&newSecret) {
				return corev1.Secret{}, nil
			}

			if err := k.client.Create(context.TODO(), &newSecret); err != nil {
				return corev1.Secret{}, err
			}
			return newSecret, nil
		}
		return corev1.Secret{}, err
	}
	// We are updating the existing Secret
	if !callback(&existingSecret) {
		return existingSecret, nil
	}
	if err := k.client.Update(context.TODO(), &existingSecret); err != nil {
		return existingSecret, err
	}
	return existingSecret, nil
}

// CreateOrUpdateSecret will create (if it does not exist) or update (if it does) a secret.
func (k *KubeHelper) createOrUpdateSecret(name, namespace string, pemFiles *pemCollection, labels map[string]string) error {
	secretToCreate, err := k.client.GetSecret(kube.ObjectKey(namespace, name))
	if err != nil {
		if apiErrors.IsNotFound(err) {
			pemFilesSecret := secret.Builder().
				SetName(name).
				SetNamespace(namespace).
				SetStringData(pemFiles.merge()).
				Build()
			// assume the secret was not found, need to create it
			// leave a nil owner reference as we haven't decided yet if we need to remove certificates
			return k.client.CreateSecret(pemFilesSecret)
		}
		return err
	}

	// if the secret already exists, it might contain entries that we want merged:
	// for each Pod we'll have the key and the certificate, but we might also have the
	// certificate added in several stages. If a certificate/key exists, and this

	pemData := pemFiles.mergeWith(secretToCreate.Data)
	secretToCreate.StringData = pemData
	return k.client.UpdateSecret(secretToCreate)
}

// validateSelfManagedSSLCertsForStatefulSet ensures that a stateful set using
// user-provided certificates has all of the relevant certificates in place.
func (ss *StatefulSetHelper) validateSelfManagedSSLCertsForStatefulSet(k *KubeHelper, secretName string) workflow.Status {
	// A "Certs" attribute has been provided
	// This means that the customer has provided with a secret name they have
	// already populated with the certs and keys for this deployment.
	// Because of the async nature of Kubernetes, this object might not be ready yet,
	// in which case, we'll keep reconciling until the object is created and is correct.
	if notReadyCerts := k.verifyCertificatesForStatefulSet(ss, secretName); notReadyCerts > 0 {
		return workflow.Failed("The secret object '%s' does not contain all the certificates needed."+
			"Required: %d, contains: %d", secretName,
			ss.Replicas,
			ss.Replicas-notReadyCerts,
		)
	}

	if err := k.validateCertificates(secretName, ss.Namespace); err != nil {
		return workflow.Failed(err.Error())
	}

	return workflow.OK()
}

// ensureOperatorManagedSSLCertsForStatefulSet ensures that a stateful set
// using operator-managed certificates has all of the relevant certificates in
// place.
func (ss *StatefulSetHelper) ensureOperatorManagedSSLCertsForStatefulSet(k *KubeHelper, secretName string, log *zap.SugaredLogger) workflow.Status {
	certsNeedApproval := false

	if err := k.validateCertificates(secretName, ss.Namespace); err != nil {
		return workflow.Failed(err.Error())
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
			csr, err := certs.ReadCSR(k.client, podnames[idx], ss.Namespace)
			additionalCertDomains := ss.getAdditionalCertDomainsForMember(idx)
			if err != nil {
				certsNeedApproval = true
				hostnames := []string{host, podnames[idx]}
				hostnames = append(hostnames, additionalCertDomains...)
				key, err := certs.CreateTlsCSR(k.client, podnames[idx], ss.Namespace, clusterDomainOrDefault(ss.ClusterDomain), hostnames, host)
				if err != nil {
					return workflow.Failed("Failed to create CSR, %s", err)
				}

				// This note was added on Release 1.5.1 of the Operator.
				log.Warn("The Operator is generating TLS certificates for server authentication. " + TLSGenerationDeprecationWarning)

				pemFiles.addPrivateKey(podnames[idx], string(key))
			} else if !certs.CSRHasRequiredDomains(csr, additionalCertDomains) {
				log.Infow(
					"Certificate request does not have all required domains",
					"requiredDomains", additionalCertDomains,
					"host", host,
				)
				return workflow.Pending("Certificate request for " + host + " doesn't have all required domains. Please manually remove the CSR in order to proceed.")
			} else if certs.CSRWasApproved(csr) {
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
			return workflow.Failed("Failed to create or update the secret: %s", err)
		}

		certsHash, err := pemFiles.getHash()
		if err != nil {
			log.Errorw("Could not hash PEM files", "err", err)
			return workflow.Failed(err.Error())
		}
		ss.SetCertificateHash(certsHash)
	}

	if certsNeedApproval {
		return workflow.Pending("Not all certificates have been approved by Kubernetes CA for %s", ss.Name)
	}
	return workflow.OK()
}

// readPemHashFromSecret reads the existing Pem from
// the secret that stores this StatefulSet's Pem collection.
func (s *StatefulSetHelper) readPemHashFromSecret(secretGetter secret.Getter) string {
	secretName := s.Name + "-cert"
	secretData, err := secret.ReadStringData(secretGetter, kube.ObjectKey(s.Namespace, secretName))
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
func (k *KubeHelper) ensureSSLCertsForStatefulSet(ss *StatefulSetHelper, log *zap.SugaredLogger) workflow.Status {
	if !ss.IsTLSEnabled() {
		// if there's no SSL certs to generate, return
		return workflow.OK()
	}

	secretName := ss.Name + "-cert"
	if ss.Security.TLSConfig.IsSelfManaged() {
		if ss.Security.TLSConfig.SecretRef.Name != "" {
			secretName = ss.Security.TLSConfig.SecretRef.Name
		}
		return ss.validateSelfManagedSSLCertsForStatefulSet(k, secretName)
	}
	return ss.ensureOperatorManagedSSLCertsForStatefulSet(k, secretName, log)
}

// validateCertificate verifies the Secret containing the certificates and the keys is valid.
func (k *KubeHelper) validateCertificates(name, namespace string) error {
	byteData, err := secret.ReadByteData(k.client, kube.ObjectKey(namespace, name))
	if err == nil {
		// Validate that the secret contains the keys, if it contains the certs.
		for _, value := range byteData {
			pemFile := newPemFileFromData(value)
			if !pemFile.isValid() {
				return fmt.Errorf("The Secret %s containing certificates is not valid. "+
					"Entries must contain a certificate and a private key.", name)
			}
		}
	}

	return nil
}

func (k *KubeHelper) verifyClientCertificatesForAgents(name, namespace string) int {
	s, err := k.client.GetSecret(kube.ObjectKey(namespace, name))
	if err != nil {
		return NumAgents
	}

	certsNotReady := 0
	for _, agentSecretKey := range []string{util.AutomationAgentPemSecretKey, util.MonitoringAgentPemSecretKey, util.BackupAgentPemSecretKey} {
		additionalDomains := []string{} // agents have no additional domains
		if !isValidPemSecret(&s, agentSecretKey, additionalDomains) {
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
		if !stringutil.Contains(cert.DNSNames, domain) {
			return false
		}
	}
	return true
}

// verifyCertificatesForStatefulSet will return the number of certificates which are
// not ready (approved and issued) yet, if all the certificates and keys required for
// the StatefulSet `ss` exist in the secret with name `secretName`
func (k *KubeHelper) verifyCertificatesForStatefulSet(ss *StatefulSetHelper, secretName string) int {
	s, err := k.client.GetSecret(kube.ObjectKey(ss.Namespace, secretName))
	if err != nil {
		return ss.Replicas
	}

	_, podnames := ss.getDNSNames()
	certsNotReady := 0

	for i, pod := range podnames {
		pem := fmt.Sprintf("%s-pem", pod)
		additionalDomains := ss.getAdditionalCertDomainsForMember(i)
		if !isValidPemSecret(&s, pem, additionalDomains) {
			certsNotReady++
		}
	}

	return certsNotReady
}

// EnvVars returns a list of corev1.EnvVar which should be passed
// to the container running Ops Manager
func opsManagerConfigurationToEnvVars(m omv1.MongoDBOpsManager) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for name, value := range m.Spec.Configuration {
		envVars = append(envVars, corev1.EnvVar{
			Name: omv1.ConvertNameToEnvVarFormat(name), Value: value,
		})
	}
	// Configure the AppDB Connection String property from a secret
	envVars = append(envVars, envVarFromSecret(omv1.ConvertNameToEnvVarFormat(util.MmsMongoUri), m.AppDBMongoConnectionStringSecretName(), util.AppDbConnectionStringKey))
	return envVars
}

// envVarFromSecret returns a corev1.EnvVar that is a reference to a secret with the field
// "secretKey" being used
func envVarFromSecret(envVarName, secretName, secretKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envVarName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
				Key: secretKey,
			},
		},
	}
}
