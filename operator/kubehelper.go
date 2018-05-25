package operator

import (
	"errors"
	"fmt"

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

func (k *KubeHelper) readConfigMap(ns string, name string) (map[string]string, error) {
	configMap, e := k.kubeApi.CoreV1().ConfigMaps(ns).Get(name, v1.GetOptions{})
	if e != nil {
		return nil, e
	}
	return configMap.Data, nil
}

func (k *KubeHelper) updateConfigMap(ns string, name string, data map[string]string) error {
	configMap, e := k.kubeApi.CoreV1().ConfigMaps(ns).Get(name, v1.GetOptions{})
	if e != nil {
		return e
	}
	configMap.Data = data

	_, e = k.kubeApi.CoreV1().ConfigMaps(ns).Update(configMap)
	if e != nil {
		return e
	}
	return nil
}

func (k *KubeHelper) readStatefulSet(ns string, name string) (*appsv1.StatefulSet, error) {
	set, e := k.kubeApi.AppsV1().StatefulSets(ns).Get(name, v1.GetOptions{})
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
