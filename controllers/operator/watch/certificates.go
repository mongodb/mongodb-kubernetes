package watch

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
)

// Certificates registers a watch on the cert-manager Certificates owned by one type of resource, so
// that a Certificate status change (issued, renewed, or failing) re-triggers the reconcile of the
// resource that owns it. Only the operator's own Certificates carry an owner of these types, so the
// watch ignores anything a user creates.
//
// Several controllers watch the same Certificates with different owner types. That costs one watch
// stream, not one per controller: the manager's cache keeps a single informer for Certificates, and
// the owner type only decides which controller each event is sent to.
func Certificates(mgr manager.Manager, c controller.Controller, owner client.Object) error {
	return c.Watch(source.Kind[client.Object](mgr.GetCache(), &certmanagerv1.Certificate{},
		handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), owner, handler.OnlyControllerOwner())))
}
