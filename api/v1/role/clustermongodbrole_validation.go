package role

import (
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

var _ webhook.Validator = &ClusterMongoDBRole{}

func (r *ClusterMongoDBRole) ValidateCreate() (warnings admission.Warnings, err error) {
	return nil, r.ProcessValidationsOnReconcile(nil)
}

func (r *ClusterMongoDBRole) ValidateUpdate(old runtime.Object) (warnings admission.Warnings, err error) {
	return nil, r.ProcessValidationsOnReconcile(old.(*ClusterMongoDBRole))
}

func (r *ClusterMongoDBRole) ValidateDelete() (warnings admission.Warnings, err error) {
	return nil, nil
}

func (r *ClusterMongoDBRole) ProcessValidationsOnReconcile(_ *ClusterMongoDBRole) error {
	if res := mdb.RoleIsCorrectlyConfigured(r.Spec.MongoDBRole, ""); res.Level == v1.ErrorLevel {
		return xerrors.Errorf("Error validating role - %s", res.Msg)
	}
	return nil
}
