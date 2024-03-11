package operator

import (
	"context"
	"reflect"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"k8s.io/utils/pointer"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func backupSnapshotScheduleTests(mdb backup.ConfigReaderUpdater, client *mock.MockedClient, reconciler reconcile.Reconciler, clusterID string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Run("Backup schedule is not read and not updated if not specified in spec", testBackupScheduleNotReadAndNotUpdatedIfNotSpecifiedInSpec(mdb, client, reconciler, clusterID))
		t.Run("Backup schedule is updated if specified in spec", testBackupScheduleIsUpdatedIfSpecifiedInSpec(mdb, client, reconciler, clusterID))
		t.Run("Backup schedule is not updated if not changed", testBackupScheduleNotUpdatedIfNotChanged(mdb, client, reconciler, clusterID))
	}
}

func testBackupScheduleNotReadAndNotUpdatedIfNotSpecifiedInSpec(mdb backup.ConfigReaderUpdater, client *mock.MockedClient, reconciler reconcile.Reconciler, clusterID string) func(t *testing.T) {
	return func(t *testing.T) {
		insertDefaultBackupSchedule(t, clusterID)

		mdb.GetBackupSpec().SnapshotSchedule = nil

		err := client.Update(context.TODO(), mdb)
		assert.NoError(t, err)

		om.CurrMockedConnection.CleanHistory()
		checkReconcile(t, reconciler, mdb)
		om.CurrMockedConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(om.CurrMockedConnection.UpdateSnapshotSchedule))
		om.CurrMockedConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(om.CurrMockedConnection.ReadSnapshotSchedule))
	}
}

func testBackupScheduleIsUpdatedIfSpecifiedInSpec(mdb backup.ConfigReaderUpdater, client *mock.MockedClient, reconciler reconcile.Reconciler, clusterID string) func(t *testing.T) {
	return func(t *testing.T) {
		insertDefaultBackupSchedule(t, clusterID)

		mdb.GetBackupSpec().SnapshotSchedule = &mdbv1.SnapshotSchedule{
			SnapshotIntervalHours:          pointer.Int(1),
			SnapshotRetentionDays:          pointer.Int(2),
			DailySnapshotRetentionDays:     pointer.Int(3),
			WeeklySnapshotRetentionWeeks:   pointer.Int(4),
			MonthlySnapshotRetentionMonths: pointer.Int(5),
			PointInTimeWindowHours:         pointer.Int(6),
			ReferenceHourOfDay:             pointer.Int(7),
			ReferenceMinuteOfHour:          pointer.Int(8),
			FullIncrementalDayOfWeek:       pointer.String("Sunday"),
			ClusterCheckpointIntervalMin:   pointer.Int(9),
		}

		err := client.Update(context.TODO(), mdb)
		require.NoError(t, err)

		checkReconcile(t, reconciler, mdb)

		snapshotSchedule, err := om.CurrMockedConnection.ReadSnapshotSchedule(clusterID)
		require.NoError(t, err)
		assertSnapshotScheduleEqual(t, mdb.GetBackupSpec().SnapshotSchedule, snapshotSchedule)
	}
}

func testBackupScheduleNotUpdatedIfNotChanged(mdb backup.ConfigReaderUpdater, kubeClient client.Client, reconciler reconcile.Reconciler, clusterID string) func(t *testing.T) {
	return func(t *testing.T) {
		insertDefaultBackupSchedule(t, clusterID)

		snapshotSchedule := &mdbv1.SnapshotSchedule{
			SnapshotIntervalHours:          pointer.Int(11),
			SnapshotRetentionDays:          pointer.Int(12),
			DailySnapshotRetentionDays:     pointer.Int(13),
			WeeklySnapshotRetentionWeeks:   pointer.Int(14),
			MonthlySnapshotRetentionMonths: pointer.Int(15),
			PointInTimeWindowHours:         pointer.Int(16),
			ReferenceHourOfDay:             pointer.Int(17),
			ReferenceMinuteOfHour:          pointer.Int(18),
			FullIncrementalDayOfWeek:       pointer.String("Thursday"),
			ClusterCheckpointIntervalMin:   pointer.Int(19),
		}

		mdb.GetBackupSpec().SnapshotSchedule = snapshotSchedule

		err := kubeClient.Update(context.TODO(), mdb)
		require.NoError(t, err)

		checkReconcile(t, reconciler, mdb)

		omSnapshotSchedule, err := om.CurrMockedConnection.ReadSnapshotSchedule(clusterID)
		require.NoError(t, err)

		assertSnapshotScheduleEqual(t, mdb.GetBackupSpec().SnapshotSchedule, omSnapshotSchedule)

		om.CurrMockedConnection.CleanHistory()
		checkReconcile(t, reconciler, mdb)

		om.CurrMockedConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(om.CurrMockedConnection.UpdateSnapshotSchedule))

		mdb.GetBackupSpec().SnapshotSchedule.FullIncrementalDayOfWeek = pointer.String("Monday")
		err = kubeClient.Update(context.TODO(), mdb)
		require.NoError(t, err)

		checkReconcile(t, reconciler, mdb)

		omSnapshotSchedule, err = om.CurrMockedConnection.ReadSnapshotSchedule(clusterID)
		assert.NoError(t, err)
		require.NotNil(t, omSnapshotSchedule)
		require.NotNil(t, omSnapshotSchedule.FullIncrementalDayOfWeek)
		assert.Equal(t, "Monday", *omSnapshotSchedule.FullIncrementalDayOfWeek)
	}
}

func insertDefaultBackupSchedule(t *testing.T, clusterID string) {
	// insert default backup schedule
	err := om.CurrMockedConnection.UpdateSnapshotSchedule(clusterID, &backup.SnapshotSchedule{
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

func checkReconcile(t *testing.T, reconciler reconcile.Reconciler, resource metav1.Object) {
	result, e := reconciler.Reconcile(context.TODO(), requestFromObject(resource))
	require.NoError(t, e)
	require.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)
}
