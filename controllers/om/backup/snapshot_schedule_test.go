package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

func TestMergeExistingScheduleWithSpec(t *testing.T) {
	existingSchedule := SnapshotSchedule{
		GroupID:                        "a",
		ClusterID:                      "b",
		DailySnapshotRetentionDays:     ptr.To(2),
		FullIncrementalDayOfWeek:       ptr.To("c"),
		MonthlySnapshotRetentionMonths: ptr.To(3),
		PointInTimeWindowHours:         ptr.To(4),
		ReferenceHourOfDay:             ptr.To(5),
		ReferenceMinuteOfHour:          ptr.To(6),
		SnapshotIntervalHours:          ptr.To(8),
		SnapshotRetentionDays:          ptr.To(9),
		WeeklySnapshotRetentionWeeks:   ptr.To(10),
		ClusterCheckpointIntervalMin:   ptr.To(11),
	}

	specSchedule := mdb.SnapshotSchedule{
		SnapshotIntervalHours:          ptr.To(11),
		SnapshotRetentionDays:          ptr.To(12),
		DailySnapshotRetentionDays:     ptr.To(13),
		WeeklySnapshotRetentionWeeks:   ptr.To(14),
		MonthlySnapshotRetentionMonths: ptr.To(15),
		PointInTimeWindowHours:         ptr.To(16),
		ReferenceHourOfDay:             ptr.To(17),
		ReferenceMinuteOfHour:          ptr.To(18),
		FullIncrementalDayOfWeek:       ptr.To("cc"),
		ClusterCheckpointIntervalMin:   ptr.To(11),
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
