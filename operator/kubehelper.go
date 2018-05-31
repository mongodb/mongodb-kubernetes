package operator

import (
	"errors"
	"fmt"
	"strings"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1beta1"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type KubeHelper struct {
	kubeApi kubernetes.Interface
}

type ProjectConfig struct {
	BaseUrl   string
	ProjectId string
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

// NewStatefulSet returns a default `StatefulSetHelper`. The defauls are as follows:
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
		Persistent: BooleanRef(true),

		ExposedExternally: false,
		ServicePort:       MongoDbDefaultPort,
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
	_, err := s.Helper.createOrUpdateStatefulsetsWithService(
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

// createOrUpdateStatefulsetsWithService creates or updates the set of statefulsets in Kubernetes mapped to the service with name "serviceName"
// The method has to be flexible (create/update) as there are cases when custom resource is created but statefulset - not
// Service named "serviceName" is created optionally (it may already exist - created by either user or by operator before)
// the vararg for statefulsets will be needed for sharded cluster (seems all replicasets there will share the same service..)
// Note the logic for "servicePort" parameter: if it is nil then the type of service created is "NodePort" (the random port
// will be allocated by Kubernetes) otherwise the type of service is default one ("ClusterIP") and it won't be connectible
// from external (unless pods in statefulset expose themselves to outside using "hostNetwork: true")
// Function returns the service port number assigned
func (k *KubeHelper) createOrUpdateStatefulsetsWithService(owner metav1.Object, servicePort int32,
	namespace string, exposeExternally bool, log *zap.SugaredLogger, statefulsets ...*appsv1.StatefulSet) (*int32, error) {

	service, err := k.ensureServicesExist(owner, statefulsets[0].Spec.ServiceName, servicePort, namespace,
		exposeExternally, log, statefulsets...)

	if err != nil {
		return nil, err
	}

	for _, s := range statefulsets {
		log = log.With("statefulset", s.Name)

		if _, err := k.kubeApi.AppsV1().StatefulSets(namespace).Get(s.Name, v1.GetOptions{}); err != nil {
			if _, err := k.kubeApi.AppsV1().StatefulSets(namespace).Create(s); err != nil {
				return nil, err
			}
			log.Infow("Created statefulset")
		} else {
			if _, err := k.kubeApi.AppsV1().StatefulSets(namespace).Update(s); err != nil {
				return nil, err
			}
			log.Infow("Updated statefulset")
		}
	}

	return discoverServicePort(service)
}

// ensureServicesExist checks if the necessary services exist and creates them if not. If the service name is not
// provided - creates it based on the first replicaset name provided
// TODO it must remove the external service in case it's no more needed
func (k *KubeHelper) ensureServicesExist(owner metav1.Object, serviceName string, servicePort int32, nameSpace string,
	exposeExternally bool, log *zap.SugaredLogger, statefulsets ...*appsv1.StatefulSet) (*corev1.Service, error) {

	ensureStatefulsetsHaveServiceLabel(serviceName, statefulsets)

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

func (k *KubeHelper) readOrCreateService(owner metav1.Object, serviceName string, label string, servicePort int32, nameSpace string,
	exposeExternally bool, log *zap.SugaredLogger) (*corev1.Service, error) {
	log = log.With("service", serviceName)

	service, err := k.kubeApi.CoreV1().Services(nameSpace).Get(serviceName, v1.GetOptions{})

	if err != nil {
		log.Info("Service doesn't exist - creating it")
		service, err = k.createService(owner, serviceName, label, servicePort, nameSpace, exposeExternally)
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

func (k *KubeHelper) createService(owner metav1.Object, name string, label string, port int32, ns string, exposeExternally bool) (*corev1.Service, error) {
	return k.kubeApi.CoreV1().Services(ns).Create(buildService(owner, name, label, ns, port, exposeExternally))
}

// readProjectConfig returns a config map
func (k *KubeHelper) readProjectConfig(namespace, name string) (*ProjectConfig, error) {
	cmap, err := k.readConfigMap(namespace, name)
	if err != nil {
		return nil, err
	}

	baseUrl, ok := cmap[OmBaseUrl]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Error getting %s from `project`", OmBaseUrl))
	}
	projectId, ok := cmap[OmProjectId]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Error getting %s from `project`", OmProjectId))
	}

	if strings.HasSuffix(baseUrl, "/") {
		cmap[OmBaseUrl] = strings.TrimSuffix(baseUrl, "/")
		if err = k.updateConfigMap(namespace, name, cmap); err == nil {
			zap.S().Infow("`baseUrl` has been corrected to not include a trailing 'slash' character.")
		}
	}

	return &ProjectConfig{
		BaseUrl:   baseUrl,
		ProjectId: projectId,
	}, nil
}

func (k *KubeHelper) readCredentials(namespace, name string) (*Credentials, error) {
	secret, err := k.readSecret(namespace, name)
	if err != nil {
		return nil, err
	}

	publicApiKey, ok := secret[OmPublicApiKey]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Missing '%s' attribute from 'credentials'", OmPublicApiKey))
	}
	user, ok := secret[OmUser]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Missing '%s' attribute from 'credentials'", OmUser))
	}

	return &Credentials{
		User:         user,
		PublicApiKey: publicApiKey,
	}, nil
}

func (k *KubeHelper) readAgentApiKeyForProject(namespace, name string) (string, error) {
	secret, err := k.readSecret(namespace, name)
	if err != nil {
		return "", err
	}

	api, ok := secret[OmAgentApiKey]
	if !ok {
		return "", errors.New(fmt.Sprintf("Could not find Agent API Key for project '%s'", name))
	}

	return api, nil
}

func (k *KubeHelper) readConfigMap(ns, name string) (map[string]string, error) {
	configMap, e := k.kubeApi.CoreV1().ConfigMaps(ns).Get(name, v1.GetOptions{})
	if e != nil {
		return nil, e
	}
	return configMap.Data, nil
}

func (k *KubeHelper) readSecret(namespace, name string) (map[string]string, error) {
	secret, e := k.kubeApi.CoreV1().Secrets(namespace).Get(name, v1.GetOptions{})
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
	configMap, e := k.kubeApi.CoreV1().ConfigMaps(namespace).Get(name, v1.GetOptions{})
	if e != nil {
		return e
	}
	configMap.Data = data

	_, e = k.kubeApi.CoreV1().ConfigMaps(namespace).Update(configMap)
	if e != nil {
		return e
	}
	return nil
}

func (k *KubeHelper) readStatefulSet(namespace, name string) (*appsv1.StatefulSet, error) {
	set, e := k.kubeApi.AppsV1().StatefulSets(namespace).Get(name, v1.GetOptions{})
	if e != nil {
		return nil, e
	}
	return set, nil
}

func discoverServicePort(service *corev1.Service) (*int32, error) {
	if l := len(service.Spec.Ports); l != 1 {
		return nil, errors.New(fmt.Sprintf("Only one port is expected for the service but found %d!", l))
	}

	if service.Spec.Type == corev1.ServiceTypeNodePort {
		nodePort := Int32Ref(service.Spec.Ports[0].NodePort)
		zap.S().Infof(">> The node port for external connections is %d!", *nodePort)
		return nodePort, nil
	}
	return Int32Ref(service.Spec.Ports[0].Port), nil
}

// ensureStatefulsetsHaveServiceLabel makes sure all the statefulsets contain the correct label (to be mapped on service)
func ensureStatefulsetsHaveServiceLabel(serviceName string, sets []*appsv1.StatefulSet) {
	for _, s := range sets {
		if len(s.ObjectMeta.Labels) == 0 {
			s.ObjectMeta.Labels = make(map[string]string)
		}
		s.ObjectMeta.Labels["app"] = serviceName
	}
}

// validateExistingService checks if the existing service is created correctly. This means it must contain correct labels
func validateExistingService(label string, service *corev1.Service) error {
	if service.Spec.Selector["app"] != label {
		return errors.New(fmt.Sprintf("Existing service %s has incorrect label selector: %s instead of %s", label,
			service.Spec.Selector["app"], label))
	}
	return nil
}
