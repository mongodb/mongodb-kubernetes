package operator

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

func TestKeyedMutexSerializesSameKey(t *testing.T) {
	var k keyedMutex
	key := types.NamespacedName{Namespace: "ns", Name: "rs"}

	firstLocked := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		unlock := k.Lock(key)
		close(firstLocked)
		<-releaseFirst
		unlock()
		close(firstDone)
	}()
	<-firstLocked

	secondLocked := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		unlock := k.Lock(key)
		close(secondLocked)
		unlock()
	}()

	select {
	case <-secondLocked:
		t.Fatal("second Lock acquired the key while the first held it")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseFirst)
	<-firstDone

	select {
	case <-secondLocked:
	case <-time.After(time.Second):
		t.Fatal("second Lock did not acquire the key after the first released it")
	}
	<-secondDone
}

func TestKeyedMutexAllowsDifferentKeysInSameNamespace(t *testing.T) {
	var k keyedMutex
	keys := []types.NamespacedName{
		{Namespace: "ns", Name: "first"},
		{Namespace: "ns", Name: "second"},
		{Namespace: "other", Name: "first"},
	}

	acquired := make(chan types.NamespacedName, len(keys))
	release := make(chan struct{})
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func(key types.NamespacedName) {
			defer wg.Done()
			unlock := k.Lock(key)
			acquired <- key
			<-release
			unlock()
		}(key)
	}

	seen := make(map[types.NamespacedName]struct{}, len(keys))
	for range keys {
		select {
		case key := <-acquired:
			seen[key] = struct{}{}
		case <-time.After(time.Second):
			t.Fatal("different keys blocked each other")
		}
	}
	assert.Len(t, seen, len(keys))

	close(release)
	wg.Wait()
}

func TestKeyedMutexMultipleWaiters(t *testing.T) {
	var k keyedMutex
	key := types.NamespacedName{Namespace: "ns", Name: "rs"}
	const waiters = 8

	var active, maxActive atomic.Int32
	var wg sync.WaitGroup
	for range waiters {
		wg.Go(func() {
			unlock := k.Lock(key)
			defer unlock()

			current := active.Add(1)
			for {
				observed := maxActive.Load()
				if current <= observed || maxActive.CompareAndSwap(observed, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
		})
	}

	wg.Wait()
	assert.Equal(t, int32(1), maxActive.Load(), "more than one waiter held the same key")
	assert.Zero(t, active.Load())
}

func TestKeyedMutexRemovesEntryAfterLastRelease(t *testing.T) {
	var k keyedMutex
	key := types.NamespacedName{Namespace: "ns", Name: "rs"}

	unlockFirst := k.Lock(key)
	assert.Len(t, k.locks, 1)

	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		unlock := k.Lock(key)
		unlock()
	}()

	require.Eventually(t, func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		entry := k.locks[key]
		return entry != nil && entry.refs == 2
	}, time.Second, time.Millisecond, "waiter did not register on the key")

	unlockFirst()

	select {
	case <-waiterDone:
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire and release the key")
	}
	assert.Empty(t, k.locks)
}

func TestKeyedMutexZeroValue(t *testing.T) {
	var k keyedMutex
	key := types.NamespacedName{Namespace: "ns", Name: "rs"}

	unlock := k.Lock(key)
	require.Len(t, k.locks, 1)
	unlock()
	assert.Empty(t, k.locks)
}
