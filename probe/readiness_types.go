package main

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// SecretReader is an interface which allows to read the secret
// Needed mainly for unit testing as it seems there is no easy way to mock out 'kubernetes.Clientset'
type SecretReader interface {
	readSecret(namespace, secretName string) (*corev1.Secret, error)
}

type healthStatus struct {
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
	Plans                             []*planStatus `json:"plans"`
}

type planStatus struct {
	Moves     []*moveStatus `json:"moves"`
	Started   *time.Time    `json:"started"`
	Completed *time.Time    `json:"completed"`
}

type moveStatus struct {
	Steps []*stepStatus `json:"steps"`
}
type stepStatus struct {
	Step      string     `json:"step"`
	Started   *time.Time `json:"started"`
	Completed *time.Time `json:"completed"`
	Result    string     `json:"result"`
}

// Default production implementation for SecretReader which reads from API server
type kubernetesSecretReader struct {
	clientset kubernetes.Interface
}

func newKubernetesSecretReader() *kubernetesSecretReader {
	return &kubernetesSecretReader{clientset: kubernetesClientset()}
}

func (r *kubernetesSecretReader) readSecret(namespace, secretName string) (*corev1.Secret, error) {
	return r.clientset.CoreV1().Secrets(namespace).Get(secretName, metav1.GetOptions{})
}

func kubernetesClientset() kubernetes.Interface {
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return clientset
}
