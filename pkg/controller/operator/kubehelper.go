package operator

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	client "sigs.k8s.io/controller-runtime/pkg/client"

	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeHelper is the helper for dealing with Kubernetes. If any Kubernetes logic requires more than some trivial operation
// - it should be put here
type KubeHelper struct {
	client client.Client
}

// SSLProjectConfig contains the configuration options that are relevant for MMS SSL configuraiton
type SSLProjectConfig struct {
	// This is set to true if baseUrl is HTTPS
	SSLRequireValidMMSServerCertificates bool

	// Name of a configmap containing a `mms-ca.crt` entry that will be mounted
	// on every Pod.
	SSLMMSCAConfigMap string

	// SSLMMSCAConfigMap will contain the CA cert, used to push muliple
	SSLMMSCAConfigMapContents string
}

type AuthMode string

const (
	NumAgents = 3
)

// ProjectConfig contains the configuration expected from the `project` (ConfigMap) attribute in
// `.spec.project`.
type ProjectConfig struct {
	// +required
	BaseURL string
	// +required
	ProjectName string
	// +optional
	OrgID string
	// +optional
	Credentials string
	// +optional
	AuthMode string
	// +optional
	SSLProjectConfig
}

// Credentials contains the configuration expected from the `credentials` (Secret)` attribute in
// `.spec.credentials`.
type Credentials struct {
	// +required
	User string

	// +required
	PublicAPIKey string
}

// StatefulSetHelper is a struct that holds different attributes needed to build
// a StatefulSet. It is used as a convenient way of passing many different parameters in one
// struct, instead of multiple parameters.
type StatefulSetHelper struct {
	// Attributes that are part of StatefulSet
	Owner     metav1.Object
	Name      string
	Service   string
	Namespace string

	// ClusterName is the cluster name that's usually 'cluster.local' but it can be changed by the customer.
	ClusterName string
	Replicas    int
	Persistent  *bool
	PodSpec     mongodb.PodSpecWrapper
	PodVars     *PodVars

	ResourceType mongodb.ResourceType

	// Not part of StatefulSet object
	Helper            *KubeHelper
	ExposedExternally bool
	ServicePort       int32
	Logger            *zap.SugaredLogger
	Project           ProjectConfig

	Security *mongodb.Security
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

// NewStatefulSetHelper returns a default `StatefulSetHelper`. The defaults are as follows:
//
// * Name: Same as the Name of the owner
// * Namespace: Same as the Namespace of the owner
// * Replicas: 1
// * ExposedExternally: false
// * ServicePort: `MongoDbDefaultPort` (27017)
//
func (k *KubeHelper) NewStatefulSetHelper(obj metav1.Object) *StatefulSetHelper {
	return &StatefulSetHelper{
		Owner:      obj,
		Name:       obj.GetName(),
		Namespace:  obj.GetNamespace(),
		Replicas:   1,
		Persistent: util.BooleanRef(true),

		ExposedExternally: false,
		ServicePort:       util.MongoDbDefaultPort,
		Helper:            k,
	}
}

// SetName can override the value of `StatefulSetHelper.Name` which is set to
// `owner.GetName()` initially.
func (s *StatefulSetHelper) SetName(name string) *StatefulSetHelper {
	s.Name = name
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

func (s *StatefulSetHelper) SetPodSpec(podSpec mongodb.PodSpecWrapper) *StatefulSetHelper {
	s.PodSpec = podSpec
	return s
}

func (s *StatefulSetHelper) SetPodVars(podVars *PodVars) *StatefulSetHelper {
	s.PodVars = podVars
	return s
}

func (s *StatefulSetHelper) SetExposedExternally(exposed bool) *StatefulSetHelper {
	s.ExposedExternally = exposed
	return s
}

func (s *StatefulSetHelper) SetProjectConfig(project ProjectConfig) *StatefulSetHelper {
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

func (s *StatefulSetHelper) SetTLS(tlsConfig *mongodb.TLSConfig) *StatefulSetHelper {
	if s.Security == nil {
		s.Security = &mongodb.Security{}
	}
	s.Security.TLSConfig = tlsConfig
	return s
}

func (s *StatefulSetHelper) SetClusterName(name string) *StatefulSetHelper {
	if name == "" {
		s.ClusterName = "cluster.local"
	} else {
		s.ClusterName = name
	}

	return s
}

func (s StatefulSetHelper) IsTLSEnabled() bool {
	return s.Security != nil && s.Security.TLSConfig != nil && s.Security.TLSConfig.Enabled
}

func (s *StatefulSetHelper) BuildStatefulSet() *appsv1.StatefulSet {
	return buildStatefulSet(*s)
}

func (s *StatefulSetHelper) CreateOrUpdateInKubernetes() error {
	set := s.BuildStatefulSet()
	_, err := s.Helper.createOrUpdateStatefulsetWithService(
		s.Owner,
		s.ServicePort,
		s.Namespace,
		s.ExposedExternally,
		s.Logger,
		set,
	)

	return err
}

// getDNSNamesForStatefulSet Returns a list of hostnames and names for the N Pods that are part of this StatefulSet
// The `fqdns` refer to the FQDN names of the Pods, that makes them reachable and distinguishable at cluster level.
// The `names` array refers to the hostname of each Pod.
func (s *StatefulSetHelper) getDNSNames() (fqdns []string, names []string) {
	var members int

	if s.ResourceType == mongodb.Standalone {
		members = 1
	} else {
		members = s.Replicas
	}

	return GetDNSNames(s.Name, s.Service, s.Namespace, s.ClusterName, members)
}
func (s *StatefulSetHelper) SetSecurity(security *mongodb.Security) *StatefulSetHelper {
	s.Security = security
	return s
}

// createOrUpdateStatefulsetWithService creates or updates the set of statefulsets in Kubernetes mapped to the service with name "serviceName"
// The method has to be flexible (create/update) as there are cases when custom resource is created but statefulset - not
// Service named "serviceName" is created optionally (it may already exist - created by either user or by operator before)
// Note the logic for "exposeExternally" parameter: if it is true then the second service is created of type "NodePort"
// (the random port will be allocated by Kubernetes) otherwise only one service of type "ClusterIP" is created and it
// won't be connectible from external (unless pods in statefulset expose themselves to outside using "hostNetwork: true")
// Function returns the service port number assigned
func (k *KubeHelper) createOrUpdateStatefulsetWithService(owner metav1.Object, servicePort int32,
	ns string, exposeExternally bool, log *zap.SugaredLogger, set *appsv1.StatefulSet) (*int32, error) {

	start := time.Now()

	service, err := k.ensureServicesExist(owner, set.Spec.ServiceName, servicePort, ns,
		exposeExternally, log, set)

	if err != nil {
		return nil, err
	}

	log = log.With("statefulset", set.Name)
	event := "Created"
	if err = k.client.Get(context.TODO(), objectKey(ns, set.Name), &appsv1.StatefulSet{}); err != nil {
		if err = k.client.Create(context.TODO(), set); err != nil {
			return nil, err
		}
	} else {
		if err = k.client.Update(context.TODO(), set); err != nil {
			return nil, err
		}
		event = "Updated"
	}

	log.Infow("Waiting until statefulset and its pods reach READY state...")

	if !k.waitForStatefulsetAndPods(ns, set.Name, log) {
		// we don't pass cluster name as we are not interested in full DNS names
		_, names := GetDnsForStatefulSet(set, "")

		// Unfortunately Kube api for events is too weak and doesn't allow to filter by object so we cannot show
		// the real pod event message to user
		return nil, fmt.Errorf("Statefulset or its pods failed to reach READY state. Check the events for "+
			"statefulset and pods: kubectl describe sts %s -n %s; kubectl describe po %s -n %s;...", set.Name,
			set.Namespace, names[0], set.Namespace)
	}
	log.Infow(event+" statefulset", "time", time.Since(start))

	return discoverServicePort(service)
}

func (k *KubeHelper) waitForStatefulsetAndPods(ns, stsName string, log *zap.SugaredLogger) bool {
	waitSeconds := util.ReadEnvVarOrPanicInt(util.PodWaitSecondsEnv)
	retrials := util.ReadEnvVarOrPanicInt(util.PodWaitRetriesEnv)

	return util.DoAndRetry(func() (string, bool) {
		set := &appsv1.StatefulSet{}
		err := k.client.Get(context.TODO(), objectKey(ns, stsName), set)
		if err != nil {
			// Should we retry these errors?...
			return fmt.Sprintf("Error reading statefulset %s: %s", objectKey(ns, stsName), err), false
		}
		msg := fmt.Sprintf("Replicas count: expected %d, current %d", *set.Spec.Replicas, set.Status.ReadyReplicas)
		return msg, set.Status.ReadyReplicas == *set.Spec.Replicas
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

// ensureServicesExist checks if the necessary services exist and creates them if not. If the service name is not
// provided - creates it based on the first replicaset name provided
// TODO it must remove the external service in case it's no more needed
func (k *KubeHelper) ensureServicesExist(owner metav1.Object, serviceName string, servicePort int32, nameSpace string,
	exposeExternally bool, log *zap.SugaredLogger, statefulset *appsv1.StatefulSet) (*corev1.Service, error) {

	ensureStatefulsetsHaveServiceLabel(serviceName, statefulset)

	// we always create the headless service to achieve Kubernetes internal connectivity
	service, err := k.readOrCreateService(owner, serviceName, serviceName, servicePort, nameSpace, false, log)

	if err != nil {
		return nil, err
	}

	if exposeExternally {
		// for providing external connectivity we need the NodePort service
		service, err = k.readOrCreateService(owner, serviceName+"-external", serviceName, servicePort, nameSpace, true, log)

		if err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (k *KubeHelper) readOrCreateService(owner metav1.Object, serviceName string, label string, servicePort int32, ns string,
	exposeExternally bool, log *zap.SugaredLogger) (*corev1.Service, error) {
	log = log.With("service", serviceName)

	service := &corev1.Service{}
	err := k.client.Get(context.TODO(), objectKey(ns, serviceName), service)

	if err != nil {
		log.Info("Service doesn't exist - creating it")
		service = buildService(owner, serviceName, label, ns, servicePort, exposeExternally)
		err = k.client.Create(context.TODO(), service)
		if err != nil {
			return nil, err
		}
		log.Infow("Created service", "type", service.Spec.Type, "port", service.Spec.Ports[0])
	} else {
		log.Info("Service already exists!")
		if err := validateExistingService(label, service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func getNamespaceAndNameForResource(resource, defaultNamespace string) (types.NamespacedName, error) {
	s := strings.Split(resource, "/")
	if len(s) > 2 {
		return types.NamespacedName{}, fmt.Errorf("Resource identifier must be of the form 'resoureName' or 'resourceNamespace/resourceName'")
	}
	var namespace, name string
	if len(s) == 2 {
		namespace, name = s[0], s[1]
	} else {
		namespace, name = defaultNamespace, s[0]
	}
	if namespace == "" || name == "" {
		return types.NamespacedName{}, fmt.Errorf("Namespace and name and name must both be non-empty")
	}
	return objectKey(namespace, name), nil
}

// readProjectConfig returns a "Project" config which is a ConfigMap with a series of attributes
// like `projectName`, `baseUrl` and a series of attributes related to SSL.
func (k *KubeHelper) readProjectConfig(defaultNamespace, name string) (*ProjectConfig, error) {
	configMapNamespacedName, err := getNamespaceAndNameForResource(name, defaultNamespace)
	if err != nil {
		return nil, err
	}

	data, err := k.readConfigMap(defaultNamespace, name)
	if err != nil {
		return nil, err
	}

	baseURL, ok := data[util.OmBaseUrl]
	if !ok {
		return nil, fmt.Errorf(`Property "%s" is not specified in config map %s`, util.OmBaseUrl, configMapNamespacedName)
	}
	projectName, ok := data[util.OmProjectName]
	if !ok {
		return nil, fmt.Errorf(`Property %s" is not specified in config map %s`, util.OmProjectName, configMapNamespacedName)
	}
	orgID := data[util.OmOrgId]

	sslRequireValidData, ok := data[util.SSLRequireValidMMSServerCertificates]

	sslRequireValid := true
	if ok {
		sslRequireValid = sslRequireValidData == "false"
	}

	sslCaConfigMap, ok := data[util.SSLMMSCAConfigMap]
	caFile := ""
	if ok {
		cacrt, err := k.readConfigMap(defaultNamespace, sslCaConfigMap)
		if err != nil {
			return nil, fmt.Errorf("Could not read the specified ConfigMap %s/%s (%e)", defaultNamespace, sslCaConfigMap, err)
		}
		for k, v := range cacrt {
			if k == CaCertMMS {
				caFile = v
				break
			}
		}
	}

	return &ProjectConfig{
		BaseURL:     baseURL,
		ProjectName: projectName,
		OrgID:       orgID,

		// Options related with SSL on OM side.
		SSLProjectConfig: SSLProjectConfig{
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
	}, nil
}

func (k *KubeHelper) readConfigMap(namespace, name string) (map[string]string, error) {
	configMapNamespacedName, err := getNamespaceAndNameForResource(name, namespace)
	if err != nil {
		return nil, err
	}

	cmap := &corev1.ConfigMap{}
	if err = k.client.Get(context.TODO(), configMapNamespacedName, cmap); err != nil {
		return nil, err
	}

	return cmap.Data, nil
}

func (k *KubeHelper) readCredentials(defaultNamespace, name string) (*Credentials, error) {
	secretNamespacedName, err := getNamespaceAndNameForResource(name, defaultNamespace)
	if err != nil {
		return nil, err
	}

	secret, err := k.readSecret(secretNamespacedName)
	if err != nil {
		return nil, err
	}

	publicAPIKey, ok := secret[util.OmPublicApiKey]
	if !ok {
		return nil, fmt.Errorf("Property \"%s\" is not specified in secret %s", util.OmPublicApiKey, secretNamespacedName)
	}
	user, ok := secret[util.OmUser]
	if !ok {
		return nil, fmt.Errorf("Property \"%s\" is not specified in secret %s", util.OmUser, secretNamespacedName)
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
		return nil, fmt.Errorf("Error getting secret %s: %s", nsName, e)
	}

	secrets := make(map[string]string)
	for k, v := range secret.Data {
		secrets[k] = strings.TrimSuffix(string(v[:]), "\n")
	}
	return secrets, nil
}

// CreateOrUpdateSecret will create (if it does not exist) or update (if it does) a secret.
func (k *KubeHelper) createOrUpdateSecret(name, namespace string, pemFiles *pemCollection, labels map[string]string) error {
	secret := &corev1.Secret{}
	err := k.client.Get(context.TODO(), objectKey(namespace, name), secret)
	if err != nil {
		// assume the secret was not found, need to create it

		secret.ObjectMeta = metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		}
		if len(labels) > 0 {
			secret.ObjectMeta.Labels = labels
		}

		secret.StringData = pemFiles.merge()
		// return now because the object we created is already "updated"
		return k.client.Create(context.TODO(), secret)
	}

	// if the secret already exists, it might contain entries that we want merged:
	// for each Pod we'll have the key and the certificate, but we might also have the
	// certificate added in several stages. If a certificate/key exists, and this

	secret.StringData = pemFiles.mergeWith(secret.Data)
	return k.client.Update(context.TODO(), secret)
}

// ensureSSLCertsForStatefulSet contains logic to create SSL certs for a StatefulSet object
func (k *KubeHelper) ensureSSLCertsForStatefulSet(ss *StatefulSetHelper, log *zap.SugaredLogger) error {
	if !ss.IsTLSEnabled() {
		// if there's no SSL certs to generate, return
		return nil
	}

	// Flag that's set to false if any of the certificates have not been approved yet.
	certsNeedApproval := false
	secretName := ss.Name + "-cert"

	if ss.Security.TLSConfig.Secret != "" {
		// A "Certs" attribute has been provided
		// This means that the customer has provided with a secret name they have
		// already populated with the certs and keys for this deployment.
		// Because of the async nature of Kubernetes, this object might not be ready yet,
		// in which case, we'll keep reconciling until the object is created and is correct.
		if notReadyCerts := k.verifyCertificatesForStatefulSet(ss, ss.Security.TLSConfig.Secret); notReadyCerts > 0 {
			return fmt.Errorf("The secret object '%s' does not contain all the certificates needed."+
				"Required: %d, contains: %d", ss.Security.TLSConfig.Secret,
				ss.Replicas,
				ss.Replicas-notReadyCerts,
			)
		}

		// Validates that the secret is valid
		if err := k.validateCertficate(secretName, ss.Namespace, false); err != nil {
			return err
		}
	} else {

		// Validates that the secret is valid, and removes it if it is not
		if err := k.validateCertficate(secretName, ss.Namespace, true); err != nil {
			return err
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
				if err != nil {
					certsNeedApproval = true
					key, err := k.createTlsCsr(podnames[idx], ss.Namespace, []string{host, podnames[idx]}, podnames[idx])
					if err != nil {
						return fmt.Errorf("Failed to create CSR, %s", err)
					}

					pemFiles.addPrivateKey(podnames[idx], string(key))
				} else {
					if checkCSRWasApproved(csr.Status.Conditions) {
						log.Infof("Certificate for Pod %s -> Approved", host)
						pemFiles.addCertificate(podnames[idx], string(csr.Status.Certificate))
					} else {
						log.Infof("Certificate for Pod %s -> Waiting for Approval", host)
						certsNeedApproval = true
					}
				}
			}

			// once we are here we know we have built everything we needed
			// This "secret" object corresponds to the certificates for this statefulset
			labels := make(map[string]string)
			labels["mongodb/secure"] = "certs"
			labels["mongodb/operator"] = "certs." + secretName

			err := k.createOrUpdateSecret(secretName, ss.Namespace, pemFiles, labels)
			if err != nil {
				// If we have an error creating or updating the secret, we might lose
				// the keys, in which case we return an error, to make it clear what
				// the error was to customers -- this should end up in the status
				// message.
				return fmt.Errorf("Failed to create or update the secret: %s", err)
			}
		}
	}

	if certsNeedApproval {
		return fmt.Errorf("Not all certificates have been approved by Kubernetes CA")
	}

	return nil
}

// validateCertificate verifies the Secret containing the certificates and the keys is valid.
func (k *KubeHelper) validateCertficate(name, namespace string, destroy bool) error {
	secret := &corev1.Secret{}
	err := k.client.Get(context.TODO(), objectKey(namespace, name), secret)
	if err == nil {
		// Validate that the secret contains the keys, if it contains the certs.
		for _, value := range secret.Data {
			pemFile := newPemFileFromData(value)
			if !pemFile.validate() {
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
					"The certificates will be requested again.", name)
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
	for _, agent := range []string{util.AutomationAgentPemFileName, util.MonitoringAgentPemFileName, util.BackupAgentPemFilePath} {
		if _, ok := secret.Data[agent]; !ok {
			certsNotReady++
		}
	}

	return certsNotReady
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

	for _, pod := range podnames {
		certEntry := fmt.Sprintf("%s-cert", pod)
		keyEntry := fmt.Sprintf("%s-key", pod)
		_, certExist := secret.Data[certEntry]
		_, keyExist := secret.Data[keyEntry]
		if !certExist || !keyExist {
			certsNotReady++
		}
	}

	return certsNotReady
}

func discoverServicePort(service *corev1.Service) (*int32, error) {
	if l := len(service.Spec.Ports); l != 1 {
		return nil, fmt.Errorf("Only one port is expected for the service but found %d", l)
	}

	if service.Spec.Type == corev1.ServiceTypeNodePort {
		nodePort := util.Int32Ref(service.Spec.Ports[0].NodePort)
		return nodePort, nil
	}
	return util.Int32Ref(service.Spec.Ports[0].Port), nil
}

// ensureStatefulsetsHaveServiceLabel makes sure all the statefulsets contain the correct label (to be mapped on service)
func ensureStatefulsetsHaveServiceLabel(serviceName string, set *appsv1.StatefulSet) {
	if len(set.ObjectMeta.Labels) == 0 {
		set.ObjectMeta.Labels = make(map[string]string)
	}
	set.ObjectMeta.Labels["app"] = serviceName
}

// validateExistingService checks if the existing service is created correctly. This means it must contain correct labels
func validateExistingService(label string, service *corev1.Service) error {
	if service.Spec.Selector["app"] != label {
		return fmt.Errorf("Existing service %s has incorrect label selector: %s instead of %s", label,
			service.Spec.Selector["app"], label)
	}
	return nil
}
