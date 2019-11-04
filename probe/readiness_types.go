package main

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ConfigMapReader is an interface which allows to read the config map
// Needed mainly for unit testing as it seems there is no easy way to mock out 'kubernetes.Clientset'
type ConfigMapReader interface {
	readConfigMap(namespace, configMapName string) (*corev1.ConfigMap, error)
}

type Health struct {
	Healthiness  map[string]processHealth     `json:"statuses"`
	ProcessPlans map[string]mmsDirectorStatus `json:"mmsStatus"`
}

type processHealth struct {
	IsInGoalState   bool  `json:"IsInGoalState"`
	LastMongoUpTime int64 `json:"LastMongoUpTime"`
	ExpectedToBeUp  bool  `json:"ExpectedToBeUp"`
}

func (h processHealth) String() string {
	return fmt.Sprintf("ExpectedToBeUp: %t, IsInGoalState: %t, LastMongoUpTime: %v", h.ExpectedToBeUp,
		h.IsInGoalState, time.Unix(h.LastMongoUpTime, 0))
}

// These structs are copied from go_planner mmsdirectorstatus.go. Some fields are pruned as not used.
type mmsDirectorStatus struct {
	Name                              string        `json:"name"`
	LastGoalStateClusterConfigVersion int64         `json:"lastGoalVersionAchieved"`
	Plans                             []*PlanStatus `json:"plans"`
}

type PlanStatus struct {
	Moves     []*MoveStatus `json:"moves"`
	Started   *time.Time    `json:"started"`
	Completed *time.Time    `json:"completed"`
}

type MoveStatus struct {
	Steps []*StepStatus `json:"steps"`
}
type StepStatus struct {
	Step      string     `json:"step"`
	Started   *time.Time `json:"started"`
	Completed *time.Time `json:"completed"`
	Result    string     `json:"result"`
}

// Default production implementation for ConfigMapReader which reads from API server
type KubernetesConfigMapReader struct {
	clientset *kubernetes.Clientset
}

func NewKubernetesConfigMapReader() *KubernetesConfigMapReader {
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return &KubernetesConfigMapReader{clientset: clientset}
}

func (r *KubernetesConfigMapReader) readConfigMap(namespace, configMapName string) (*corev1.ConfigMap, error) {
	return r.clientset.CoreV1().ConfigMaps(namespace).Get(configMapName, metav1.GetOptions{})
}
