package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
)

func TestMergeExistingScheduleWithSpec(t *testing.T) {
	existingSchedule := SnapshotSchedule{
		GroupID:                        "a",
		ClusterID:                      "b",
		DailySnapshotRetentionDays:     new(2),
		FullIncrementalDayOfWeek:       new("c"),
		MonthlySnapshotRetentionMonths: new(3),
		PointInTimeWindowHours:         new(4),
		ReferenceHourOfDay:             new(5),
		ReferenceMinuteOfHour:          new(6),
		SnapshotIntervalHours:          new(8),
		SnapshotRetentionDays:          new(9),
		WeeklySnapshotRetentionWeeks:   new(10),
		ClusterCheckpointIntervalMin:   new(11),
	}

	specSchedule := mdb.SnapshotSchedule{
		SnapshotIntervalHours:          new(11),
		SnapshotRetentionDays:          new(12),
		DailySnapshotRetentionDays:     new(13),
		WeeklySnapshotRetentionWeeks:   new(14),
		MonthlySnapshotRetentionMonths: new(15),
		PointInTimeWindowHours:         new(16),
		ReferenceHourOfDay:             new(17),
		ReferenceMinuteOfHour:          new(18),
		FullIncrementalDayOfWeek:       new("cc"),
		ClusterCheckpointIntervalMin:   new(11),
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
