package membercluster

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

// Watcher watches the MemberCluster CRs scoped by the manager cache (see ByObject in main.go)
// and initiates a graceful shutdown when a CR is created, its spec changes, or it is deleted,
// so the operator restarts and rebuilds its member-cluster client map from the current set of
// MemberCluster CRs.
//
// TODO(m1kola): slice-3: restart is used instead of a hot, reactive reload because the
// per-cluster clients and caches are built once at startup and threaded into every controller,
// so adding/removing a cluster currently requires re-initialising the manager. A later slice
// makes membership changes reactive (no restart). This mirrors the OperatorConfig watcher.
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
	informer, err := w.cache.GetInformer(ctx, &operatorv1.MemberCluster{})
	if err != nil {
		return err
	}
	reg, err := informer.AddEventHandler(w.newEventHandler())
	if err != nil {
		return err
	}

	// Wait until this registration's initial object replay is complete before marking the
	// watcher as synced. Until then AddFunc calls are suppressed: they are the initial replay
	// of already-existing CRs, which were already consumed to build the client map at startup.
	// informer.HasSynced() is not suitable here — it reflects the informer's global state,
	// which is already true when Start() runs.
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
					zap.S().Info("MemberCluster created - initiating graceful shutdown to rebuild member-cluster clients")
					w.cancel()
				})
			}
		},
		UpdateFunc: func(old, new any) {
			oldObj, newObj := old.(metav1.Object), new.(metav1.Object)
			// Only spec changes bump the generation; status writes (e.g. the RBACValid
			// condition) leave it unchanged and must not trigger a restart.
			if newObj.GetGeneration() != oldObj.GetGeneration() {
				w.shutdownOnce.Do(func() {
					zap.S().Info("MemberCluster changed - initiating graceful shutdown to rebuild member-cluster clients")
					w.cancel()
				})
			}
		},
		DeleteFunc: func(_ any) {
			w.shutdownOnce.Do(func() {
				zap.S().Info("MemberCluster deleted - initiating graceful shutdown to rebuild member-cluster clients")
				w.cancel()
			})
		},
	}
}
