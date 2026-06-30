package operatorconfig

import (
	"context"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
)

// +kubebuilder:rbac:groups=operator.mongodb.com,resources=operatorconfigs,verbs=get;list;watch,namespace=placeholder

// Watcher watches the single OperatorConfig instance scoped by the manager cache (see
// ByObject in main.go) and initiates a graceful shutdown when the CR is created, its spec
// changes, or it is deleted, so the operator can restart and reload its configuration.
//
// Restart is used instead of hot reload because some settings (e.g. watchedResources) alter which
// controllers and informers are active, requiring re-initialisation of the manager. Ensuring that no
// reconciler caches a config value locally would also be difficult to enforce and test.
type Watcher struct {
	cache        cache.Cache
	cancel       context.CancelFunc
	shutdownOnce sync.Once
	synced       atomic.Bool
}

func NewWatcher(c cache.Cache, cancel context.CancelFunc) *Watcher {
	return &Watcher{
		cache:  c,
		cancel: cancel,
	}
}

func (w *Watcher) Start(ctx context.Context) error {
	informer, err := w.cache.GetInformer(ctx, &operatorv1.OperatorConfig{})
	if err != nil {
		return err
	}
	reg, err := informer.AddEventHandler(w.newEventHandler())
	if err != nil {
		return err
	}

	// Wait until this registration's initial object replay is complete before marking
	// the watcher as synced. Until then, AddFunc calls are suppressed (initial sync
	// replay). informer.HasSynced() is not suitable here — it reflects the informer's
	// global state, which is already true when Start() runs.
	if !toolscache.WaitForCacheSync(ctx.Done(), reg.HasSynced) {
		return nil
	}
	w.synced.Store(true)

	<-ctx.Done()
	return nil
}

func (w *Watcher) newEventHandler() toolscache.ResourceEventHandlerFuncs {
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(_ any) {
			if w.synced.Load() {
				w.shutdownOnce.Do(func() {
					zap.S().Info("OperatorConfig created - initiating graceful shutdown for config load")
					w.cancel()
				})
			}
		},
		UpdateFunc: func(old, new any) {
			oldObj, newObj := old.(metav1.Object), new.(metav1.Object)
			if newObj.GetGeneration() != oldObj.GetGeneration() {
				w.shutdownOnce.Do(func() {
					zap.S().Info("OperatorConfig changed - initiating graceful shutdown for config reload")
					w.cancel()
				})
			}
		},
		DeleteFunc: func(_ any) {
			w.shutdownOnce.Do(func() {
				zap.S().Info("OperatorConfig deleted - initiating graceful shutdown to revert to defaults")
				w.cancel()
			})
		},
	}
}
