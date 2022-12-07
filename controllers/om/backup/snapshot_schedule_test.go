package backup

import (
	"k8s.io/utils/pointer"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/stretchr/testify/assert"
)

func TestMergeExistingScheduleWithSpec(t *testing.T) {
	existingSchedule := SnapshotSchedule{
		GroupID:                        "a",
		ClusterID:                      "b",
		DailySnapshotRetentionDays:     pointer.Int(2),
		FullIncrementalDayOfWeek:       pointer.String("c"),
		MonthlySnapshotRetentionMonths: pointer.Int(3),
		PointInTimeWindowHours:         pointer.Int(4),
		ReferenceHourOfDay:             pointer.Int(5),
		ReferenceMinuteOfHour:          pointer.Int(6),
		SnapshotIntervalHours:          pointer.Int(8),
		SnapshotRetentionDays:          pointer.Int(9),
		WeeklySnapshotRetentionWeeks:   pointer.Int(10),
		ClusterCheckpointIntervalMin:   pointer.Int(11),
	}

	specSchedule := mdb.SnapshotSchedule{
		SnapshotIntervalHours:          pointer.Int(11),
		SnapshotRetentionDays:          pointer.Int(12),
		DailySnapshotRetentionDays:     pointer.Int(13),
		WeeklySnapshotRetentionWeeks:   pointer.Int(14),
		MonthlySnapshotRetentionMonths: pointer.Int(15),
		PointInTimeWindowHours:         pointer.Int(16),
		ReferenceHourOfDay:             pointer.Int(17),
		ReferenceMinuteOfHour:          pointer.Int(18),
		FullIncrementalDayOfWeek:       pointer.String("cc"),
		ClusterCheckpointIntervalMin:   pointer.Int(11),
	}

	merged := mergeExistingScheduleWithSpec(existingSchedule, specSchedule)
	assert.Equal(t, specSchedule.SnapshotIntervalHours, merged.SnapshotIntervalHours)
	assert.Equal(t, specSchedule.SnapshotRetentionDays, merged.SnapshotRetentionDays)
	assert.Equal(t, specSchedule.DailySnapshotRetentionDays, merged.DailySnapshotRetentionDays)
	assert.Equal(t, specSchedule.WeeklySnapshotRetentionWeeks, merged.WeeklySnapshotRetentionWeeks)
	assert.Equal(t, specSchedule.MonthlySnapshotRetentionMonths, merged.MonthlySnapshotRetentionMonths)
	assert.Equal(t, specSchedule.PointInTimeWindowHours, merged.PointInTimeWindowHours)
	assert.Equal(t, specSchedule.ReferenceHourOfDay, merged.ReferenceHourOfDay)
	assert.Equal(t, specSchedule.ReferenceMinuteOfHour, merged.ReferenceMinuteOfHour)
	assert.Equal(t, specSchedule.FullIncrementalDayOfWeek, merged.FullIncrementalDayOfWeek)
	assert.Equal(t, specSchedule.ClusterCheckpointIntervalMin, merged.ClusterCheckpointIntervalMin)

	emptySpecSchedule := mdb.SnapshotSchedule{}
	merged = mergeExistingScheduleWithSpec(existingSchedule, emptySpecSchedule)
	assert.Equal(t, existingSchedule.SnapshotIntervalHours, merged.SnapshotIntervalHours)
	assert.Equal(t, existingSchedule.SnapshotRetentionDays, merged.SnapshotRetentionDays)
	assert.Equal(t, existingSchedule.DailySnapshotRetentionDays, merged.DailySnapshotRetentionDays)
	assert.Equal(t, existingSchedule.WeeklySnapshotRetentionWeeks, merged.WeeklySnapshotRetentionWeeks)
	assert.Equal(t, existingSchedule.MonthlySnapshotRetentionMonths, merged.MonthlySnapshotRetentionMonths)
	assert.Equal(t, existingSchedule.PointInTimeWindowHours, merged.PointInTimeWindowHours)
	assert.Equal(t, existingSchedule.ReferenceHourOfDay, merged.ReferenceHourOfDay)
	assert.Equal(t, existingSchedule.ReferenceMinuteOfHour, merged.ReferenceMinuteOfHour)
	assert.Equal(t, existingSchedule.FullIncrementalDayOfWeek, merged.FullIncrementalDayOfWeek)
	assert.Equal(t, existingSchedule.ClusterCheckpointIntervalMin, merged.ClusterCheckpointIntervalMin)
}
