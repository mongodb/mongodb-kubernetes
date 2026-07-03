package memberwatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster/failedcluster"
)

const testRequiredHealthyStreak = 5

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	_ = os.Setenv("PERFORM_FAILOVER", "false") // nolint:forbidigo
	defer func(key string) {
		_ = os.Unsetenv(key) // nolint:forbidigo
	}("PERFORM_FAILOVER")
}

func TestIsMemberClusterHealthy(t *testing.T) {
	// mark cluster as healthy because "200" status code
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(200)
	}))

	memberHealthCheck := NewMemberHealthCheck(server.URL, []byte("ca-data"), "bhjkb", zap.S())
	healthy := memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, true, healthy)

	// mark cluster unhealthy because != "200" status code
	var requestCount int
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		requestCount++
		rw.WriteHeader(500)
	}))

	memberHealthCheck = NewMemberHealthCheck(
		server.URL,
		[]byte("ca-data"),
		"hhfhj",
		zap.S(),
		WithRetryConfig(0, 0, 2), // No delay between retries, retry 2 times
	)
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())

	assert.Equal(t, false, healthy)
	// Verify retries actually happened: initial request + 2 retries = 3 total
	assert.Equal(t, 3, requestCount, "Expected 3 requests (1 initial + 2 retries)")

	// mark cluster unhealthy because of error
	memberHealthCheck = NewMemberHealthCheck("", []byte("ca-data"), "bhdjbh", zap.S())
	healthy = memberHealthCheck.IsClusterHealthy(zap.S())
	assert.Equal(t, false, healthy)
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	return scheme
}

func newTestMDBMulti(name, ns string, annotations map[string]string) *mdbmulti.MongoDBMultiCluster {
	return &mdbmulti.MongoDBMultiCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: annotations},
		Spec: mdbmulti.MongoDBMultiSpec{
			ClusterSpecList: mdb.ClusterSpecList{{ClusterName: "cluster1", Members: 2}},
		},
	}
}

// TestAnnotationIsAdded verifies that when a cluster reports unhealthy the failed-cluster
// annotation is written to all MongoDBMultiCluster resources.
func TestAnnotationIsAdded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mrs := newTestMDBMulti("mdbmc", "ns", nil)
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		mock := NewMockedMemberHealthCheck("server1").(*MockedMemberHealthCheck)
		mock.Healthy = false

		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": mock},
			HealthyStreak:         map[string]int{"cluster1": 0},
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		require.Eventually(t, func() bool {
			got := &mdbmulti.MongoDBMultiCluster{}
			if err := fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got); err != nil {
				return false
			}
			return isInFailedClusterAnnotation(got.Annotations, "cluster1")
		}, 5*time.Second, 50*time.Millisecond)
	})
}

// TestAnnotationIsRemovedWhenClusterRecovers verifies that once a cluster sustains
// requiredHealthyStreak consecutive healthy checks, the failed-cluster annotation is
// removed and a reconcile event is enqueued.
func TestAnnotationIsRemovedWhenClusterRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		failedAnnotation := getFailedClusterList([]string{"cluster1"})
		mrs := newTestMDBMulti("mdbmc", "ns", map[string]string{
			failedcluster.FailedClusterAnnotation: failedAnnotation,
		})
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		// Healthy: true is the default from NewMockedMemberHealthCheck.
		// Pre-seed the streak one below the threshold so a single iteration tips it over.
		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": NewMockedMemberHealthCheck("server1")},
			HealthyStreak:         map[string]int{"cluster1": testRequiredHealthyStreak - 1},
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		select {
		case evt := <-watchChannel:
			assert.Equal(t, "mdbmc", evt.Object.GetName())
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for reconcile event after cluster recovery")
		}

		assert.Equal(t, testRequiredHealthyStreak, checker.HealthyStreakFor("cluster1"))

		got := &mdbmulti.MongoDBMultiCluster{}
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got))
		assert.False(t, isInFailedClusterAnnotation(got.Annotations, "cluster1"))
	})
}

// TestNoEventBeforeStreakThreshold verifies that a healthy check below the streak
// threshold does not enqueue a reconcile event.
func TestNoEventBeforeStreakThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mrs := newTestMDBMulti("mdbmc", "ns", nil)
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": NewMockedMemberHealthCheck("server1")},
			HealthyStreak:         map[string]int{"cluster1": testRequiredHealthyStreak - 2}, // streak = 3
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		assert.Never(t, func() bool {
			return len(watchChannel) > 0
		}, 500*time.Millisecond, 50*time.Millisecond)

		// Confirm the health check ran and incremented the streak without crossing the threshold.
		assert.Eventually(t, func() bool {
			return checker.HealthyStreakFor("cluster1") == testRequiredHealthyStreak-1
		}, 5*time.Second, 50*time.Millisecond)
	})
}

// TestNoEventWhenStreakAtCapWithoutAnnotation verifies that a cluster whose streak
// is already at the cap does not fire a reconcile event when no failed annotation exists.
func TestNoEventWhenStreakAtCapWithoutAnnotation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mrs := newTestMDBMulti("mdbmc", "ns", nil)
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": NewMockedMemberHealthCheck("server1")},
			HealthyStreak:         map[string]int{"cluster1": testRequiredHealthyStreak},
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		assert.Never(t, func() bool {
			return len(watchChannel) > 0
		}, 500*time.Millisecond, 50*time.Millisecond)

		// Confirm the streak stayed capped and didn't wrap or overflow.
		assert.Eventually(t, func() bool {
			return checker.HealthyStreakFor("cluster1") == testRequiredHealthyStreak
		}, 5*time.Second, 50*time.Millisecond)
	})
}

// TestStreakResetsOnUnhealthyWhenAlmostRecovered verifies that a cluster with a
// near-complete healthy streak still gets the failed annotation added if it reports
// unhealthy, proving the streak was reset to zero.
func TestStreakResetsOnUnhealthyWhenAlmostRecovered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mrs := newTestMDBMulti("mdbmc", "ns", nil)
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		mock := NewMockedMemberHealthCheck("server1").(*MockedMemberHealthCheck)
		mock.Healthy = false

		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": mock},
			HealthyStreak:         map[string]int{"cluster1": testRequiredHealthyStreak - 1}, // streak = 4
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		// Let the watcher run its health-check iteration to completion and block on the
		// next 10s tick. At that durable blocking point both the streak reset and the
		// annotation write from this iteration have already happened, so we can assert on
		// them directly instead of polling with Eventually/Never (whose own timers and
		// goroutines interact nondeterministically with the synctest fake clock).
		synctest.Wait()

		// The unhealthy report reset the near-complete streak back to zero...
		assert.Equal(t, 0, checker.HealthyStreakFor("cluster1"))

		// ...and the cluster was recorded in the failed-cluster annotation.
		got := &mdbmulti.MongoDBMultiCluster{}
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got))
		assert.True(t, isInFailedClusterAnnotation(got.Annotations, "cluster1"))
	})
}

// TestNoEventWhenClusterNotInAnnotationAtThreshold verifies that reaching the streak
// threshold does not enqueue an event when the cluster is not in the failed annotation.
func TestNoEventWhenClusterNotInAnnotationAtThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mrs := newTestMDBMulti("mdbmc", "ns", nil) // no failed annotation
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": NewMockedMemberHealthCheck("server1")},
			HealthyStreak:         map[string]int{"cluster1": testRequiredHealthyStreak - 1}, // streak = 4
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		assert.Never(t, func() bool {
			return len(watchChannel) > 0
		}, 500*time.Millisecond, 50*time.Millisecond)

		// Confirm the streak reached the threshold even though no event was fired (no annotation present).
		assert.Eventually(t, func() bool {
			return checker.HealthyStreakFor("cluster1") == testRequiredHealthyStreak
		}, 5*time.Second, 50*time.Millisecond)
	})
}

// TestMultipleClustersIndependentStreaks verifies that streak state is tracked
// per-cluster: one cluster going unhealthy does not affect another cluster's streak.
func TestMultipleClustersIndependentStreaks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Only cluster2 is in the failed annotation; cluster1 will be added during the test.
		failedAnnotation := getFailedClusterList([]string{"cluster2"})
		mrs := newTestMDBMulti("mdbmc", "ns", map[string]string{
			failedcluster.FailedClusterAnnotation: failedAnnotation,
		})
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		mock1 := NewMockedMemberHealthCheck("server1").(*MockedMemberHealthCheck)
		mock1.Healthy = false

		checker := &MemberClusterHealthChecker{
			Cache: map[string]ClusterHealthChecker{
				"cluster1": mock1,
				"cluster2": NewMockedMemberHealthCheck("server2"),
			},
			HealthyStreak: map[string]int{
				"cluster1": 0,
				"cluster2": testRequiredHealthyStreak - 1, // streak = 4, tips to 5 this iteration
			},
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		select {
		case evt := <-watchChannel:
			assert.Equal(t, "mdbmc", evt.Object.GetName())
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for cluster2 recovery event")
		}

		// cluster2's annotation was removed — its streak reached the threshold independently of cluster1.
		assert.Equal(t, testRequiredHealthyStreak, checker.HealthyStreakFor("cluster2"))

		got := &mdbmulti.MongoDBMultiCluster{}
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got))
		assert.False(t, isInFailedClusterAnnotation(got.Annotations, "cluster2"), "cluster2 annotation should be removed")
	})
}

// TestAllMDBMultiResourcesCleared verifies that when a cluster's streak reaches the
// threshold, the failed annotation is removed from every MongoDBMultiCluster resource
// and a reconcile event is enqueued for each one.
func TestAllMDBMultiResourcesCleared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		failedAnnotation := getFailedClusterList([]string{"cluster1"})
		mrs1 := newTestMDBMulti("mdbmc-1", "ns", map[string]string{
			failedcluster.FailedClusterAnnotation: failedAnnotation,
		})
		mrs2 := newTestMDBMulti("mdbmc-2", "ns", map[string]string{
			failedcluster.FailedClusterAnnotation: failedAnnotation,
		})

		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs1, mrs2).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": NewMockedMemberHealthCheck("server1")},
			HealthyStreak:         map[string]int{"cluster1": testRequiredHealthyStreak - 1},
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		var evtNames []string
		for i := 0; i < 2; i++ {
			select {
			case evt := <-watchChannel:
				evtNames = append(evtNames, evt.Object.GetName())
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for event %d/2", i+1)
			}
		}
		assert.ElementsMatch(t, []string{"mdbmc-1", "mdbmc-2"}, evtNames)

		for _, name := range []string{"mdbmc-1", "mdbmc-2"} {
			got := &mdbmulti.MongoDBMultiCluster{}
			require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "ns"}, got))
			assert.False(t, isInFailedClusterAnnotation(got.Annotations, "cluster1"),
				"annotation should be removed from %s", name)
		}
	})
}

func TestFailedClusterAnnotationStaysWhenPerformFailoverTrue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		t.Setenv("PERFORM_FAILOVER", "true") // nolint:forbidigo

		failedAnnotation := getFailedClusterList([]string{"cluster1"})
		mrs := newTestMDBMulti("mdbmc", "ns", map[string]string{
			failedcluster.FailedClusterAnnotation: failedAnnotation,
		})
		fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(mrs).Build()
		central := kubernetesClient.NewClient(fakeClient)
		watchChannel := make(chan event.GenericEvent, 10)

		// Healthy: true is the default from NewMockedMemberHealthCheck.
		checker := &MemberClusterHealthChecker{
			Cache:                 map[string]ClusterHealthChecker{"cluster1": NewMockedMemberHealthCheck("server1")},
			HealthyStreak:         map[string]int{"cluster1": 0},
			RequiredHealthyStreak: testRequiredHealthyStreak,
		}

		go checker.WatchMemberClusterHealth(ctx, zap.S(), watchChannel, central, nil)

		assert.Never(t, func() bool {
			return len(watchChannel) > 0
		}, 500*time.Millisecond, 50*time.Millisecond)
		assert.Never(t, func() bool { return checker.HealthyStreakFor("cluster1") > 0 }, 500*time.Millisecond, 50*time.Millisecond)

		got := &mdbmulti.MongoDBMultiCluster{}
		require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{Name: "mdbmc", Namespace: "ns"}, got))
		assert.True(t, isInFailedClusterAnnotation(got.Annotations, "cluster1"))
	})
}
