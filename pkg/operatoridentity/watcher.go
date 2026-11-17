package operatoridentity

import (
	"context"
	"sync"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	corev1 "k8s.io/api/core/v1"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
)

// Watcher watches the shared cache for the mck-member-cluster ConfigMap in the
// operator namespace and initiates a graceful shutdown when the operator's
// cluster identity changes, so the operator restarts and reloads it.
type Watcher struct {
	cache             cache.Cache
	operatorNamespace string
	bootIdentity      string
	cancel            context.CancelFunc
	restartOnce       sync.Once
}

func NewWatcher(c cache.Cache, operatorNamespace string, bootIdentity string, cancel context.CancelFunc) *Watcher {
	return &Watcher{cache: c, operatorNamespace: operatorNamespace, bootIdentity: bootIdentity, cancel: cancel}
}

func (w *Watcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &corev1.ConfigMap{})
	if err != nil {
		return err
	}
	if _, err := informer.AddEventHandler(w.newEventHandler()); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

func (w *Watcher) newEventHandler() toolscache.ResourceEventHandlerFuncs {
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { w.handle(obj, false) },
		UpdateFunc: func(_, newObj any) { w.handle(newObj, false) },
		DeleteFunc: func(obj any) { w.handle(obj, true) },
	}
}

func (w *Watcher) handle(obj any, deleted bool) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok && deleted {
		if tombstone, isTombstone := obj.(toolscache.DeletedFinalStateUnknown); isTombstone {
			cm, ok = tombstone.Obj.(*corev1.ConfigMap)
		}
	}
	if !ok || cm.Name != resourcenames.MemberClusterIdentityConfigMapName || cm.Namespace != w.operatorNamespace {
		return
	}

	identity := ""
	if !deleted {
		identity = identityFrom(cm)
	}
	w.restartOnChange(identity)
}

func (w *Watcher) restartOnChange(identity string) {
	if identity == w.bootIdentity {
		return
	}
	w.restartOnce.Do(func() {
		zap.S().Infof("Operator cluster identity changed %q -> %q - initiating graceful shutdown to reload it", w.bootIdentity, identity)
		w.cancel()
	})
}

func identityFrom(cm *corev1.ConfigMap) string {
	return cm.Data[resourcenames.MemberClusterIdentityClusterNameKey]
}
