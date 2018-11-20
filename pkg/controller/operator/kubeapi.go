package operator

import (
	"context"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeApi is an interface for all Kubernetes API related methods
type KubeApi interface {
	getStatefulSet(ns, name string) (*appsv1.StatefulSet, error)
	createStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error)
	updateStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error)

	getService(ns, name string) (*corev1.Service, error)
	createService(ns string, service *corev1.Service) (*corev1.Service, error)

	getConfigMap(ns, name string) (*corev1.ConfigMap, error)
	updateConfigMap(ns string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error)

	getSecret(ns, name string) (*corev1.Secret, error)
	createSecret(ns string, secret *corev1.Secret) (*corev1.Secret, error)
}

type RestKubeApi struct {
	KubeApi kubernetes.Interface
}

func (k *RestKubeApi) getStatefulSet(ns, name string) (*appsv1.StatefulSet, error) {
	return k.KubeApi.AppsV1().StatefulSets(ns).Get(name, metav1.GetOptions{})
}

func (k *RestKubeApi) createStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	return k.KubeApi.AppsV1().StatefulSets(ns).Create(set)
}

func (k *RestKubeApi) updateStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	return k.KubeApi.AppsV1().StatefulSets(ns).Update(set)
}

func (k *RestKubeApi) getService(ns, name string) (*corev1.Service, error) {
	return k.KubeApi.CoreV1().Services(ns).Get(name, metav1.GetOptions{})
}
func (k *RestKubeApi) createService(ns string, service *corev1.Service) (*corev1.Service, error) {
	return k.KubeApi.CoreV1().Services(ns).Create(service)
}
func (k *RestKubeApi) getConfigMap(ns, name string) (*corev1.ConfigMap, error) {
	return k.KubeApi.CoreV1().ConfigMaps(ns).Get(name, metav1.GetOptions{})
}

func (k *RestKubeApi) updateConfigMap(ns string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	return k.KubeApi.CoreV1().ConfigMaps(ns).Update(configMap)
}

func (k *RestKubeApi) getSecret(ns, name string) (*corev1.Secret, error) {
	return k.KubeApi.CoreV1().Secrets(ns).Get(name, metav1.GetOptions{})
}

func (k *RestKubeApi) createSecret(ns string, secret *corev1.Secret) (*corev1.Secret, error) {
	return k.KubeApi.CoreV1().Secrets(ns).Create(secret)
}

// TODO temp

type NewKubeApi struct {
	Client client.Client
}

func (k *NewKubeApi) getStatefulSet(ns, name string) (*appsv1.StatefulSet, error) {
	ans := &appsv1.StatefulSet{}
	err := k.Client.Get(context.TODO(), nsName(ns, name), ans)
	return ans, err
}

func (k *NewKubeApi) createStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	err := k.Client.Create(context.TODO(), set)
	return set, err
}

func (k *NewKubeApi) updateStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	err := k.Client.Update(context.TODO(), set)
	return set, err
}

func (k *NewKubeApi) getService(ns, name string) (*corev1.Service, error) {
	ans := &corev1.Service{}
	err := k.Client.Get(context.TODO(), nsName(ns, name), ans)
	return ans, err
}
func (k *NewKubeApi) createService(ns string, service *corev1.Service) (*corev1.Service, error) {
	err := k.Client.Create(context.TODO(), service)
	return service, err
}
func (k *NewKubeApi) getConfigMap(ns, name string) (*corev1.ConfigMap, error) {
	ans := &corev1.ConfigMap{}
	err := k.Client.Get(context.TODO(), nsName(ns, name), ans)

	return ans, err
}

func (k *NewKubeApi) updateConfigMap(ns string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	err := k.Client.Update(context.TODO(), configMap)
	return configMap, err
}

func (k *NewKubeApi) getSecret(ns, name string) (*corev1.Secret, error) {
	ans := &corev1.Secret{}
	err := k.Client.Get(context.TODO(), nsName(ns, name), ans)

	return ans, err
}

func (k *NewKubeApi) createSecret(ns string, secret *corev1.Secret) (*corev1.Secret, error) {
	err := k.Client.Create(context.TODO(), secret)
	return secret, err
}
