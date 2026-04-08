package user

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type MongoDBUserValidator struct{}

var _ admission.CustomValidator = &MongoDBUserValidator{}

func (v *MongoDBUserValidator) ValidateCreate(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *MongoDBUserValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldUser := oldObj.(*MongoDBUser)
	newUser := newObj.(*MongoDBUser)
	if isMigratedFromVM(newUser) && !isMigratedFromVM(oldUser) {
		return nil, fmt.Errorf("migratedFromVm cannot be set to true after initial creation")
	}
	return nil, nil
}

func (v *MongoDBUserValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func isMigratedFromVM(u *MongoDBUser) bool {
	return u.Spec.MigratedFromVM != nil && *u.Spec.MigratedFromVM
}
