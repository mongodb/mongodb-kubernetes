package service

import (
	"context"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestService_merge0(t *testing.T) {
	dst := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"}}
	src := corev1.Service{}
	dst = Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)

	// Name and Namespace will not be copied over.
	src = corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "new-service", Namespace: "new-namespace"}}
	dst = Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)
}

func TestService_NodePortIsNotOverwritten(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{},
	}

	dst = Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestCreateOrUpdateService_NodePortsArePreservedWhenThereIsMoreThanOnePortDefined(t *testing.T) {
	ctx := context.Background()
	port1 := corev1.ServicePort{
		Name:       "port1",
		Port:       1000,
		TargetPort: intstr.IntOrString{IntVal: 1001},
		NodePort:   30030,
	}

	port2 := corev1.ServicePort{
		Name:       "port2",
		Port:       2000,
		TargetPort: intstr.IntOrString{IntVal: 2001},
		NodePort:   40040,
	}

	fakeClient, _ := mock.NewDefaultFakeClient()
	existingService := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{port1, port2}},
	}

	err := CreateOrUpdateService(ctx, fakeClient, existingService)
	assert.NoError(t, err)

	port1WithNodePortZero := port1
	port1WithNodePortZero.NodePort = 0
	port2WithNodePortZero := port2
	port2WithNodePortZero.NodePort = 0

	newServiceWithoutNodePorts := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{port1WithNodePortZero, port2WithNodePortZero}},
	}

	err = CreateOrUpdateService(ctx, fakeClient, newServiceWithoutNodePorts)
	assert.NoError(t, err)

	changedService, err := fakeClient.GetService(ctx, types.NamespacedName{Name: "my-service", Namespace: "my-namespace"})
	require.NoError(t, err)
	require.NotNil(t, changedService)
	require.Len(t, changedService.Spec.Ports, 2)

	assert.Equal(t, port1.NodePort, changedService.Spec.Ports[0].NodePort)
	assert.Equal(t, port2.NodePort, changedService.Spec.Ports[1].NodePort)
}

func TestService_NodePortIsNotOverwrittenIfNoNodePortIsSpecified(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{}}},
	}

	dst = Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsKeptWhenChangingServiceType(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30030}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30099}},
			Type:  corev1.ServiceTypeNodePort,
		},
	}
	dst = Merge(dst, src)
	assert.Equal(t, int32(30099), dst.Spec.Ports[0].NodePort)

	src = corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30011}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}

	dst = Merge(dst, src)
	assert.Equal(t, int32(30011), dst.Spec.Ports[0].NodePort)
}

func TestService_mergeAnnotations(t *testing.T) {
	// Annotations will be added
	annotationsDest := make(map[string]string)
	annotationsDest["annotation0"] = "value0"
	annotationsDest["annotation1"] = "value1"
	annotationsSrc := make(map[string]string)
	annotationsSrc["annotation0"] = "valueXXXX"
	annotationsSrc["annotation2"] = "value2"

	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotationsSrc,
		},
	}
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-service", Namespace: "my-namespace",
			Annotations: annotationsDest,
		},
	}
	dst = Merge(dst, src)
	assert.Len(t, dst.ObjectMeta.Annotations, 3)
	assert.Equal(t, dst.ObjectMeta.Annotations["annotation0"], "valueXXXX")
}

func TestService_mergeFieldsWhenDestFieldsAreNil(t *testing.T) {
	annotationsSrc := make(map[string]string)
	annotationsSrc["annotation0"] = "value0"
	annotationsSrc["annotation1"] = "value1"

	labelsSrc := make(map[string]string)
	labelsSrc["label0"] = "labelValue0"
	labelsSrc["label1"] = "labelValue1"

	selectorsSrc := make(map[string]string)
	selectorsSrc["sel0"] = "selValue0"
	selectorsSrc["sel1"] = "selValue1"

	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotationsSrc,
			Labels:      labelsSrc,
		},
		Spec: corev1.ServiceSpec{Selector: selectorsSrc},
	}
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "my-service",
			Namespace:   "my-namespace",
			Annotations: nil,
			Labels:      nil,
		},
	}
	dst = Merge(dst, src)
	assert.Len(t, dst.ObjectMeta.Annotations, 2)
	assert.Equal(t, dst.ObjectMeta.Annotations, annotationsSrc)
	assert.Equal(t, dst.ObjectMeta.Labels, labelsSrc)
	assert.Equal(t, dst.Spec.Selector, selectorsSrc)
}
