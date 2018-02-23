package main

import (
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
)

func BaseContainer() corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:            ContainerName,
				Image:           ContainerImage,
				ImagePullPolicy: ContainerImagePullPolicy,
				EnvFrom:         BaseEnvFrom(),
			},
		},
	}
}

func BaseEnvFrom() []corev1.EnvFromSource {
	return []corev1.EnvFromSource{
		{
			ConfigMapRef: &corev1.ConfigMapEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: ContainerConfigMapName,
				},
			},
		},
	}
}

// MakeInReference is required to return a *int32, which can't be declared as a literal.
func MakeIntReference(i int32) *int32 {
	return &i
}

// AttributeUpdate is just a mock of how a attribute can be declared as updated from an
// old value to a new value. The values should be interfaces and we'll have to reflect on them.
// Or hard-code the names and types of expected values in a very go idiomatic way.
type AttributeUpdate struct {
	AttributeName string
	OldValue      interface{}
	NewValue      interface{}
}

func GetResourceUpdates(oldObj, newObj interface{}) ([]AttributeUpdate, error) {
	oldObjType := reflect.TypeOf(oldObj)
	newObjType := reflect.TypeOf(newObj)

	if oldObjType != newObjType {
		// this should not happen
		return nil, errors.New("Object are not the same type!")
	}
	if reflect.TypeOf(oldObj) == reflect.TypeOf(MongoDbStandalone) {
		fmt.Println("It is a standalone!")
	}

	return []AttributeUpdate{}, nil
}
