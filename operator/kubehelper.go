package operator

import (
	"errors"
	"fmt"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
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
func (k *KubeHelper) createOrUpdateStatefulsetsWithService(serviceName string, servicePort int32,
	nameSpace string, exposeExternally bool, statefulsets ...*appsv1.StatefulSet) (*int32, error) {

	service, err := k.ensureServicesExist(serviceName, servicePort, nameSpace, exposeExternally, statefulsets...)

	if err != nil {
		return nil, err
	}

	for _, s := range statefulsets {
		log := zap.S().With("statefulset", s.Name)

		if _, err := k.kubeApi.AppsV1().StatefulSets(nameSpace).Get(s.Name, v1.GetOptions{}); err != nil {
			if _, err := k.kubeApi.AppsV1().StatefulSets(nameSpace).Create(s); err != nil {
				return nil, err
			}
			log.Infow("Created statefulset")
		} else {
			if _, err := k.kubeApi.AppsV1().StatefulSets(nameSpace).Update(s); err != nil {
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
func (k *KubeHelper) ensureServicesExist(serviceName string, servicePort int32, nameSpace string,
	exposeExternally bool, statefulsets ...*appsv1.StatefulSet) (*corev1.Service, error) {

	sName := getOrFormatServiceName(serviceName, statefulsets[0].Name)

	ensureStatefulsetsHaveServiceLabel(sName, statefulsets)

	// we always create the headless service to achieve Kubernetes internal connectivity
	service, err := k.readOrCreateService(sName, sName, servicePort, nameSpace, false)

	if err != nil {
		return nil, err
	}

	if exposeExternally {
		// for providing external connectivity we need the NodePort service
		service, err = k.readOrCreateService(sName+"-external", sName, servicePort, nameSpace, true)

		if err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (k *KubeHelper) readOrCreateService(serviceName string, label string, servicePort int32, nameSpace string,
	exposeExternally bool) (*corev1.Service, error) {
	log := zap.S().With("service", serviceName)

	service, err := k.kubeApi.CoreV1().Services(nameSpace).Get(serviceName, v1.GetOptions{})

	if err != nil {
		log.Info("Service doesn't exist - creating it")
		service, err = k.createService(serviceName, label, servicePort, nameSpace, exposeExternally)
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

func (k *KubeHelper) createService(name string, label string, port int32, ns string, exposeExternally bool) (*corev1.Service, error) {
	return k.kubeApi.CoreV1().Services(ns).Create(buildService(name, label, ns, port, exposeExternally))
}

func (k *KubeHelper) deleteService(name string, ns string) error {
	serviceName := getOrFormatServiceName("", name)
	log := zap.S().With("service", serviceName)
	externalServiceName := fmt.Sprintf("%s-external", serviceName)

	service, err := k.kubeApi.CoreV1().Services(ns).Get(serviceName, v1.GetOptions{})
	if err != nil {
		return err
	}
	if _, ok := service.ObjectMeta.Annotations[CreatedByOperator]; !ok {
		log.Info("Service was not created by operator, not deleting.")
		return nil
	}

	if err := k.kubeApi.CoreV1().Services(ns).Delete(externalServiceName, &v1.DeleteOptions{}); err != nil {
		log.Info("Not Exposed externally.")
	}

	if err := k.kubeApi.CoreV1().Services(ns).Delete(serviceName, &v1.DeleteOptions{}); err != nil {
		return err
	}

	return nil
}

func (k *KubeHelper) GetPodNames(setName, namespace, clusterName string) ([]string, error) {
	s, err := k.kubeApi.AppsV1().StatefulSets(namespace).Get(setName, v1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return GetDnsForStatefulSet(s, clusterName), nil
}

func (k *KubeHelper) readConfigMap(ns string, name string) (map[string]string, error) {
	configMap, e := k.kubeApi.CoreV1().ConfigMaps(ns).Get(name, v1.GetOptions{})
	if e != nil {
		return nil, e
	}
	return configMap.Data, nil
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
