package operator

import (
	"errors"
	"fmt"
	"strings"

	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type KubeHelper struct {
	kubeApi KubeApi
}

type ProjectConfig struct {
	BaseUrl     string
	ProjectName string
	OrgId       string
}

type Credentials struct {
	User         string
	PublicApiKey string
}

// StatefulSetBuildingParams is a struct that holds different attributes needed to build
// a StatefulSet. It is used as a convenient way of passing many different parameters in one
// struct, instead of multiple parameters.
type StatefulSetHelper struct {
	// Attributes that are part of StatefulSet
	Owner      metav1.Object
	Name       string
	Service    string
	Namespace  string
	Replicas   int
	Persistent *bool
	PodSpec    mongodb.PodSpecWrapper
	PodVars    *PodVars

	// Not part of StatefulSet object
	Helper            *KubeHelper
	ExposedExternally bool
	ServicePort       int32
	Logger            *zap.SugaredLogger
}

// NewStatefulSet returns a default `StatefulSetHelper`. The defaults are as follows:
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

func (s *StatefulSetHelper) SetServicePort(port int32) *StatefulSetHelper {
	s.ServicePort = port
	return s
}

func (s *StatefulSetHelper) SetLogger(log *zap.SugaredLogger) *StatefulSetHelper {
	s.Logger = log
	return s
}

func (s *StatefulSetHelper) BuildStatefulSet() *appsv1.StatefulSet {
	return buildStatefulSet(*s)
}

func (s *StatefulSetHelper) CreateOrUpdateInKubernetes() error {
	sets := s.BuildStatefulSet()
	_, err := s.Helper.createOrUpdateStatefulsetWithService(
		s.Owner,
		s.ServicePort,
		s.Namespace,
		s.ExposedExternally,
		s.Logger,
		sets,
	)
	if err != nil {
		return err
	}

	return nil
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
	if _, err := k.kubeApi.getStatefulSet(ns, set.Name); err != nil {
		if _, err := k.kubeApi.createStatefulSet(ns, set); err != nil {
			return nil, err
		}
	} else {
		if _, err := k.kubeApi.updateStatefulSet(ns, set); err != nil {
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
		return nil, errors.New(fmt.Sprintf("Statefulset or its pods failed to reach READY state. Check the events for "+
			"statefulset and pods: kubectl describe sts %s -n %s; kubectl describe po %s -n %s;...", set.Name,
			set.Namespace, names[0], set.Namespace))
	}
	log.Infow(event+" statefulset", "time", time.Since(start))

	return discoverServicePort(service)
}

func (k *KubeHelper) waitForStatefulsetAndPods(ns, stsName string, log *zap.SugaredLogger) bool {
	waitSeconds := util.ReadEnvVarOrPanicInt(util.StatefulSetWaitSecondsEnv)
	retrials := util.ReadEnvVarOrPanicInt(util.StatefulSetWaitRetriesEnv)

	return util.DoAndRetry(func() (string, bool) {
		set, _ := k.kubeApi.getStatefulSet(ns, stsName)
		msg := fmt.Sprintf("%d of %d replicas are ready", set.Status.ReadyReplicas, *set.Spec.Replicas)
		return msg, set.Status.ReadyReplicas == *set.Spec.Replicas
	}, log, retrials, waitSeconds)
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

	service, err := k.kubeApi.getService(ns, serviceName)

	if err != nil {
		log.Info("Service doesn't exist - creating it")
		service, err = k.kubeApi.createService(ns, buildService(owner, serviceName, label, ns, servicePort, exposeExternally))
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

// readProjectConfig returns a config map
func (k *KubeHelper) readProjectConfig(ns, configMapName string) (*ProjectConfig, error) {
	cmap, err := k.kubeApi.getConfigMap(ns, configMapName)
	if err != nil {
		return nil, err
	}

	data := cmap.Data

	baseUrl, ok := data[util.OmBaseUrl]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Property \"%s\" is not specified in config map %s", util.OmBaseUrl, configMapName))
	}
	projectName, ok := data[util.OmProjectName]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Property \"%s\" is not specified in config map %s ", util.OmProjectName, configMapName))
	}
	orgId := data[util.OmOrgId]

	return &ProjectConfig{
		BaseUrl:     baseUrl,
		ProjectName: projectName,
		OrgId:       orgId,
	}, nil
}

func (k *KubeHelper) readCredentials(namespace, name string) (*Credentials, error) {
	secret, err := k.readSecret(namespace, name)
	if err != nil {
		return nil, err
	}

	publicApiKey, ok := secret[util.OmPublicApiKey]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Missing '%s' attribute from 'credentials'", util.OmPublicApiKey))
	}
	user, ok := secret[util.OmUser]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Missing '%s' attribute from 'credentials'", util.OmUser))
	}

	return &Credentials{
		User:         user,
		PublicApiKey: publicApiKey,
	}, nil
}

func (k *KubeHelper) readAgentApiKeyForProject(namespace, agentKeySecretName string) (string, error) {
	secret, err := k.readSecret(namespace, agentKeySecretName)
	if err != nil {
		return "", err
	}

	key, ok := secret[util.OmAgentApiKey]
	if !ok {
		return "", fmt.Errorf("Could not find key \"%s\" in secret %s", util.OmAgentApiKey, agentKeySecretName)
	}

	return strings.TrimSuffix(string(key), "\n"), nil
}

func (k *KubeHelper) readSecret(namespace, name string) (map[string]string, error) {
	secret, e := k.kubeApi.getSecret(namespace, name)
	if e != nil {
		return nil, e
	}

	secrets := make(map[string]string)
	for k, v := range secret.Data {
		secrets[k] = strings.TrimSuffix(string(v[:]), "\n")
	}
	return secrets, nil
}

func (k *KubeHelper) updateConfigMap(namespace, name string, data map[string]string) error {
	configMap, e := k.kubeApi.getConfigMap(namespace, name)
	if e != nil {
		return e
	}
	configMap.Data = data

	_, e = k.kubeApi.updateConfigMap(namespace, configMap)
	if e != nil {
		return e
	}
	return nil
}

func discoverServicePort(service *corev1.Service) (*int32, error) {
	if l := len(service.Spec.Ports); l != 1 {
		return nil, errors.New(fmt.Sprintf("Only one port is expected for the service but found %d!", l))
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
		return errors.New(fmt.Sprintf("Existing service %s has incorrect label selector: %s instead of %s", label,
			service.Spec.Selector["app"], label))
	}
	return nil
}
