package operator

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

func backupSnapshotScheduleTests(mdb backup.ConfigReaderUpdater, client client.Client, reconciler reconcile.Reconciler, omConnectionFactory *om.CachedOMConnectionFactory, clusterID string) func(t *testing.T) {
	ctx := context.Background()
	return func(t *testing.T) {
		t.Run("Backup schedule is not read and not updated if not specified in spec", testBackupScheduleNotReadAndNotUpdatedIfNotSpecifiedInSpec(ctx, mdb, client, reconciler, omConnectionFactory, clusterID))
		t.Run("Backup schedule is updated if specified in spec", testBackupScheduleIsUpdatedIfSpecifiedInSpec(ctx, mdb, client, reconciler, omConnectionFactory, clusterID))
		t.Run("Backup schedule is not updated if not changed", testBackupScheduleNotUpdatedIfNotChanged(ctx, mdb, client, reconciler, omConnectionFactory, clusterID))
	}
}

func testBackupScheduleNotReadAndNotUpdatedIfNotSpecifiedInSpec(ctx context.Context, mdb backup.ConfigReaderUpdater, client client.Client, reconciler reconcile.Reconciler, omConnectionFactory *om.CachedOMConnectionFactory, clusterID string) func(t *testing.T) {
	return func(t *testing.T) {
		require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(mdb), mdb))
		insertDefaultBackupSchedule(t, omConnectionFactory, clusterID)

		mdb.GetBackupSpec().SnapshotSchedule = nil

		err := client.Update(ctx, mdb)
		assert.NoError(t, err)

		omConnectionFactory.GetConnection().(*om.MockedOmConnection).CleanHistory()
		checkReconcile(ctx, t, reconciler, mdb)
		omConnectionFactory.GetConnection().(*om.MockedOmConnection).CheckOperationsDidntHappen(t, reflect.ValueOf(omConnectionFactory.GetConnection().UpdateSnapshotSchedule))
		omConnectionFactory.GetConnection().(*om.MockedOmConnection).CheckOperationsDidntHappen(t, reflect.ValueOf(omConnectionFactory.GetConnection().ReadSnapshotSchedule))
	}
}

func testBackupScheduleIsUpdatedIfSpecifiedInSpec(ctx context.Context, mdb backup.ConfigReaderUpdater, client client.Client, reconciler reconcile.Reconciler, omConnectionFactory *om.CachedOMConnectionFactory, clusterID string) func(t *testing.T) {
	return func(t *testing.T) {
		require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(mdb), mdb))
		insertDefaultBackupSchedule(t, omConnectionFactory, clusterID)

		mdb.GetBackupSpec().SnapshotSchedule = &mdbv1.SnapshotSchedule{
			SnapshotIntervalHours:          ptr.To(1),
			SnapshotRetentionDays:          ptr.To(2),
			DailySnapshotRetentionDays:     ptr.To(3),
			WeeklySnapshotRetentionWeeks:   ptr.To(4),
			MonthlySnapshotRetentionMonths: ptr.To(5),
			PointInTimeWindowHours:         ptr.To(6),
			ReferenceHourOfDay:             ptr.To(7),
			ReferenceMinuteOfHour:          ptr.To(8),
			FullIncrementalDayOfWeek:       ptr.To("Sunday"),
			ClusterCheckpointIntervalMin:   ptr.To(9),
		}

		err := client.Update(ctx, mdb)
		require.NoError(t, err)
		require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(mdb), mdb))

		checkReconcile(ctx, t, reconciler, mdb)

		snapshotSchedule, err := omConnectionFactory.GetConnection().ReadSnapshotSchedule(clusterID)
		require.NoError(t, err)
		assertSnapshotScheduleEqual(t, mdb.GetBackupSpec().SnapshotSchedule, snapshotSchedule)
	}
}

func testBackupScheduleNotUpdatedIfNotChanged(ctx context.Context, mdb backup.ConfigReaderUpdater, kubeClient client.Client, reconciler reconcile.Reconciler, omConnectionFactory *om.CachedOMConnectionFactory, clusterID string) func(t *testing.T) {
	return func(t *testing.T) {
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKeyFromApiObject(mdb), mdb))
		insertDefaultBackupSchedule(t, omConnectionFactory, clusterID)

		snapshotSchedule := &mdbv1.SnapshotSchedule{
			SnapshotIntervalHours:          ptr.To(11),
			SnapshotRetentionDays:          ptr.To(12),
			DailySnapshotRetentionDays:     ptr.To(13),
			WeeklySnapshotRetentionWeeks:   ptr.To(14),
			MonthlySnapshotRetentionMonths: ptr.To(15),
			PointInTimeWindowHours:         ptr.To(16),
			ReferenceHourOfDay:             ptr.To(17),
			ReferenceMinuteOfHour:          ptr.To(18),
			FullIncrementalDayOfWeek:       ptr.To("Thursday"),
			ClusterCheckpointIntervalMin:   ptr.To(19),
		}

		mdb.GetBackupSpec().SnapshotSchedule = snapshotSchedule

		err := kubeClient.Update(ctx, mdb)
		require.NoError(t, err)

		checkReconcile(ctx, t, reconciler, mdb)
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKeyFromApiObject(mdb), mdb))

		omSnapshotSchedule, err := omConnectionFactory.GetConnection().ReadSnapshotSchedule(clusterID)
		require.NoError(t, err)

		assertSnapshotScheduleEqual(t, mdb.GetBackupSpec().SnapshotSchedule, omSnapshotSchedule)

		omConnectionFactory.GetConnection().(*om.MockedOmConnection).CleanHistory()
		checkReconcile(ctx, t, reconciler, mdb)
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKeyFromApiObject(mdb), mdb))

		omConnectionFactory.GetConnection().(*om.MockedOmConnection).CheckOperationsDidntHappen(t, reflect.ValueOf(omConnectionFactory.GetConnection().UpdateSnapshotSchedule))

		mdb.GetBackupSpec().SnapshotSchedule.FullIncrementalDayOfWeek = ptr.To("Monday")
		err = kubeClient.Update(ctx, mdb)
		require.NoError(t, err)

		checkReconcile(ctx, t, reconciler, mdb)
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKeyFromApiObject(mdb), mdb))

		omSnapshotSchedule, err = omConnectionFactory.GetConnection().ReadSnapshotSchedule(clusterID)
		assert.NoError(t, err)
		require.NotNil(t, omSnapshotSchedule)
		require.NotNil(t, omSnapshotSchedule.FullIncrementalDayOfWeek)
		assert.Equal(t, "Monday", *omSnapshotSchedule.FullIncrementalDayOfWeek)
	}
}

func insertDefaultBackupSchedule(t *testing.T, omConnectionFactory *om.CachedOMConnectionFactory, clusterID string) {
	// insert default backup schedule
	err := omConnectionFactory.GetConnection().UpdateSnapshotSchedule(clusterID, &backup.SnapshotSchedule{
		GroupID:   om.TestGroupID,
		ClusterID: clusterID,
	})
	assert.NoError(t, err)
}

func assertSnapshotScheduleEqual(t *testing.T, expected *mdbv1.SnapshotSchedule, actual *backup.SnapshotSchedule) {
	assert.Equal(t, expected.SnapshotIntervalHours, actual.SnapshotIntervalHours)
	assert.Equal(t, expected.SnapshotRetentionDays, actual.SnapshotRetentionDays)
	assert.Equal(t, expected.DailySnapshotRetentionDays, actual.DailySnapshotRetentionDays)
	assert.Equal(t, expected.WeeklySnapshotRetentionWeeks, actual.WeeklySnapshotRetentionWeeks)
	assert.Equal(t, expected.MonthlySnapshotRetentionMonths, actual.MonthlySnapshotRetentionMonths)
	assert.Equal(t, expected.PointInTimeWindowHours, actual.PointInTimeWindowHours)
	assert.Equal(t, expected.ReferenceHourOfDay, actual.ReferenceHourOfDay)
	assert.Equal(t, expected.ReferenceMinuteOfHour, actual.ReferenceMinuteOfHour)
	assert.Equal(t, expected.FullIncrementalDayOfWeek, actual.FullIncrementalDayOfWeek)
	assert.Equal(t, expected.ClusterCheckpointIntervalMin, actual.ClusterCheckpointIntervalMin)
}

func checkReconcile(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, resource metav1.Object) {
	result, e := reconciler.Reconcile(ctx, requestFromObject(resource))
	require.NoError(t, e)
	require.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)
}
