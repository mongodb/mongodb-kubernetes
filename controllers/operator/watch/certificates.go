package watch

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
)

// MongoDBCertificates registers a watch on the cert-manager Certificates owned by a MongoDB, so that a
// Certificate status change (issued, renewed, or failing) re-triggers the reconcile of the MongoDB
// that owns it. Only the operator created Certificates carry such an owner, so the watch ignores
// anything a user creates.
//
// Replica sets and sharded clusters are both of kind MongoDB, so the owner kind alone would send the
// Certificate events of one to the controller of the other, which then reconciles a resource it does
// not handle. This watch reads the owner and only re-triggers it when its spec.type is resourceType.
//
// Several controllers watch the same Certificates. That costs one watch stream, not one per
// controller: the manager's cache keeps a single informer for Certificates, and each controller only
// decides which events it takes.
func MongoDBCertificates(mgr manager.Manager, c controller.Controller, resourceType mdbv1.ResourceType) error {
	return c.Watch(source.Kind[client.Object](mgr.GetCache(), &certmanagerv1.Certificate{},
		handler.EnqueueRequestsFromMapFunc(mongoDBOwnerOfType(mgr.GetClient(), resourceType))))
}

// MongoDBMultiCertificates is MongoDBCertificates for a MongoDBMultiCluster.
func MongoDBMultiCertificates(mgr manager.Manager, c controller.Controller) error {
	return c.Watch(source.Kind[client.Object](mgr.GetCache(), &certmanagerv1.Certificate{},
		handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &mdbmultiv1.MongoDBMultiCluster{}, handler.OnlyControllerOwner())))
}

// mongoDBOwnerOfType returns a function that maps a Certificate to the MongoDB that owns it, but only
// when that MongoDB is of resourceType. It maps to nothing when the owner is something else or is gone.
func mongoDBOwnerOfType(c client.Reader, resourceType mdbv1.ResourceType) handler.MapFunc {
	return func(ctx context.Context, certificate client.Object) []reconcile.Request {
		owner := metav1.GetControllerOf(certificate)
		if owner == nil || owner.Kind != "MongoDB" || owner.APIVersion != v1.SchemeGroupVersion.String() {
			return nil
		}
		key := types.NamespacedName{Namespace: certificate.GetNamespace(), Name: owner.Name}
		mdb := &mdbv1.MongoDB{}
		if err := c.Get(ctx, key, mdb); err != nil {
			// The owner is gone, so the Certificate will be deleted with it and there is nothing to
			// reconcile. Any other read error is from the cache and the next event retries it.
			return nil
		}
		if mdb.Spec.ResourceType != resourceType {
			return nil
		}
		return []reconcile.Request{{NamespacedName: key}}
	}
}
