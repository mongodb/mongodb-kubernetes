package operator

import (
	"runtime"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"

	"reflect"

	"fmt"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	TestProjectConfigMapName  = om.TestGroupName
	TestCredentialsSecretName = "my-credentials"
	TestNamespace             = "my-namespace"
)

type MockedKubeApi struct {
	sets       map[string]*appsv1.StatefulSet
	services   map[string]*corev1.Service
	configMaps map[string]*corev1.ConfigMap
	secrets    map[string]*corev1.Secret
	// mocked client keeps track of all implemented functions called - uses reflection Func for this to enable type-safety
	// and make function names rename easier
	history []*runtime.Func
	// the delay for statefulsets "creation"
	StsCreationDelayMillis time.Duration
}

func newMockedKubeApi() *MockedKubeApi {
	return newMockedKubeApiDetailed(om.TestGroupName, "")
}

func newMockedKubeApiDetailed(projectName, organizationId string) *MockedKubeApi {
	api := MockedKubeApi{}
	api.sets = make(map[string]*appsv1.StatefulSet)
	api.services = make(map[string]*corev1.Service)
	api.configMaps = make(map[string]*corev1.ConfigMap)
	api.secrets = make(map[string]*corev1.Secret)

	// initialize config map and secret to emulate user preparing environment
	project := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: TestProjectConfigMapName, Namespace: TestNamespace},
		Data:       map[string]string{util.OmBaseUrl: "http://mycompany.com:8080", util.OmProjectName: projectName, util.OmOrgId: organizationId}}
	api.createConfigMap(TestNamespace, project)

	credentials := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TestCredentialsSecretName, Namespace: TestNamespace},
		StringData: map[string]string{util.OmUser: "test@mycompany.com", util.OmPublicApiKey: "36lj245asg06s0h70245dstgft"}}
	api.createSecret(TestNamespace, credentials)

	// no delay in creation by default
	api.StsCreationDelayMillis = 0

	// ugly but seems the only way to clean om global variable for current connection (as golang doesnt' have setup()/teardown()
	// methods for testing
	om.CurrMockedConnection = nil

	return &api
}

func (k *MockedKubeApi) getStatefulSet(ns, name string) (*appsv1.StatefulSet, error) {
	k.addToHistory(reflect.ValueOf(k.getStatefulSet))
	if _, exists := k.sets[ns+name]; !exists {
		return nil, errors.New(fmt.Sprintf("Statefulset %s doesn't exists!", name))
	}
	return k.sets[ns+name], nil
}

func (k *MockedKubeApi) createStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	k.addToHistory(reflect.ValueOf(k.createStatefulSet))
	if _, err := k.getStatefulSet(ns, set.Name); err == nil {
		return nil, errors.New(fmt.Sprintf("Statefulset %s already exists!", set.Name))
	}
	k.doUpdateStatefulset(ns, set)
	return set, nil
}

func (k *MockedKubeApi) updateStatefulSet(ns string, set *appsv1.StatefulSet) (*appsv1.StatefulSet, error) {
	k.addToHistory(reflect.ValueOf(k.updateStatefulSet))
	if _, err := k.getStatefulSet(ns, set.Name); err != nil {
		return nil, err
	}

	k.doUpdateStatefulset(ns, set)
	return set, nil
}

// doUpdateStatefulset emulates statefulsets reaching their desired state, also OM automation agents get "registered"
func (k *MockedKubeApi) doUpdateStatefulset(ns string, set *appsv1.StatefulSet) {
	k.sets[ns+set.Name] = set

	if k.StsCreationDelayMillis == 0 {
		markStatefulSetsReady(set)
	} else {
		go func() {
			time.Sleep(k.StsCreationDelayMillis * time.Millisecond)
			markStatefulSetsReady(set)
		}()
	}

}

func markStatefulSetsReady(set *appsv1.StatefulSet) {
	set.Status.ReadyReplicas = *set.Spec.Replicas

	if om.CurrMockedConnection != nil {
		// We also "register" automation agents.
		// So far we don't support custom cluster name
		hostnames, _ := GetDnsForStatefulSet(set, "")

		om.CurrMockedConnection.AddHosts(hostnames)
	}
}

func (k *MockedKubeApi) getService(ns, name string) (*corev1.Service, error) {
	k.addToHistory(reflect.ValueOf(k.getService))
	if _, exists := k.services[ns+name]; !exists {
		return nil, errors.New(fmt.Sprintf("Service \"%s\" doesn't exists!", name))
	}
	return k.services[ns+name], nil
}

func (k *MockedKubeApi) createService(ns string, service *corev1.Service) (*corev1.Service, error) {
	k.addToHistory(reflect.ValueOf(k.createService))
	if _, err := k.getService(ns, service.Name); err == nil {
		return nil, errors.New(fmt.Sprintf("Service \"%s\" already exists!", service.Name))
	}
	k.services[ns+service.Name] = service
	return service, nil
}

func (k *MockedKubeApi) getConfigMap(ns, name string) (*corev1.ConfigMap, error) {
	k.addToHistory(reflect.ValueOf(k.getConfigMap))
	if _, exists := k.configMaps[ns+name]; !exists {
		return nil, errors.New(fmt.Sprintf("ConfigMap \"%s\" doesn't exists!", name))
	}
	return k.configMaps[ns+name], nil
}

// internal method, used to initialize environment
func (k *MockedKubeApi) createConfigMap(ns string, configMap *corev1.ConfigMap) {
	k.configMaps[ns+configMap.Name] = configMap
}

func (k *MockedKubeApi) updateConfigMap(ns string, configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	k.addToHistory(reflect.ValueOf(k.updateConfigMap))
	if _, err := k.getConfigMap(ns, configMap.Name); err != nil {
		return nil, err
	}
	k.configMaps[ns+configMap.Name] = configMap
	return configMap, nil
}

func (k *MockedKubeApi) getSecret(ns, name string) (*corev1.Secret, error) {
	k.addToHistory(reflect.ValueOf(k.getSecret))
	if _, exists := k.secrets[ns+name]; !exists {
		return nil, errors.New(fmt.Sprintf("Secret \"%s\" doesn't exists!", name))
	}
	return k.secrets[ns+name], nil
}
func (k *MockedKubeApi) createSecret(ns string, secret *corev1.Secret) (*corev1.Secret, error) {
	k.addToHistory(reflect.ValueOf(k.createSecret))
	if _, err := k.getSecret(ns, secret.Name); err == nil {
		return nil, errors.New(fmt.Sprintf("Secret \"%s\" already exists!", secret.Name))
	}
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	for k, v := range secret.StringData {
		// seems the in-memory bytes are already
		//sDec, _ := b64.StdEncoding.DecodeString(v)
		secret.Data[k] = []byte(v)
	}
	k.secrets[ns+secret.Name] = secret
	return secret, nil
}

func (oc *MockedKubeApi) addToHistory(value reflect.Value) {
	oc.history = append(oc.history, runtime.FuncForPC(value.Pointer()))
}

func (oc *MockedKubeApi) CheckOrderOfOperations(t *testing.T, value ...reflect.Value) {
	j := 0
	matched := ""
	for _, h := range oc.history {
		if h.Name() == runtime.FuncForPC(value[j].Pointer()).Name() {
			matched += h.Name() + " "
			j++
		}
		if j == len(value) {
			break
		}
	}
	assert.Equal(t, len(value), j, "Only %d of %d expected operations happened in expected order (%s)", j, len(value), matched)
}

func (oc *MockedKubeApi) CheckOperationsDidntHappen(t *testing.T, value ...reflect.Value) {
	for _, h := range oc.history {
		for _, o := range value {
			assert.NotEqual(t, o, h, "Operation %v is not expected to happen", h)
		}
	}
}
