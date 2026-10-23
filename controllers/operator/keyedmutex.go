package operator

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

// keyedMutex serializes work for the same resource key. Work for different keys
// proceeds independently. The zero value is ready to use.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[types.NamespacedName]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

// Lock blocks until the key is free. The returned function releases the key.
func (k *keyedMutex) Lock(key types.NamespacedName) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[types.NamespacedName]*keyedMutexEntry)
	}
	entry := k.locks[key]
	if entry == nil {
		entry = &keyedMutexEntry{}
		k.locks[key] = entry
	}
	entry.refs++
	k.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
