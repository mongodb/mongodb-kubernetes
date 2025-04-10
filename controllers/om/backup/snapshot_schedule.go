package backup

import (
	"fmt"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
)

// SnapshotSchedule object represents request and response body object of get and update snapshot schedule request
// https://www.mongodb.com/docs/ops-manager/master/reference/api/backup/update-one-snapshot-schedule-by-cluster-id/#request-body-parameters
type SnapshotSchedule struct {
	GroupID                        string  `json:"groupId,omitempty"`
	ClusterID                      string  `json:"clusterId,omitempty"`
	ClusterCheckpointIntervalMin   *int    `json:"clusterCheckpointIntervalMin,omitempty"`
	DailySnapshotRetentionDays     *int    `json:"dailySnapshotRetentionDays,omitempty"`
	FullIncrementalDayOfWeek       *string `json:"fullIncrementalDayOfWeek,omitempty"`
	MonthlySnapshotRetentionMonths *int    `json:"monthlySnapshotRetentionMonths,omitempty"`
	PointInTimeWindowHours         *int    `json:"pointInTimeWindowHours,omitempty"`
	ReferenceHourOfDay             *int    `json:"referenceHourOfDay,omitempty"`
	ReferenceMinuteOfHour          *int    `json:"referenceMinuteOfHour,omitempty"`
	SnapshotIntervalHours          *int    `json:"snapshotIntervalHours,omitempty"`
	SnapshotRetentionDays          *int    `json:"snapshotRetentionDays,omitempty"`
	WeeklySnapshotRetentionWeeks   *int    `json:"weeklySnapshotRetentionWeeks,omitempty"`
	// ReferenceTimeZoneOffset is not handled deliberately, because OM converts ReferenceHourOfDay to UTC and saves always +0000 offset,
	// and this would cause constant updates, due to always different timezone offset.
	// ReferenceTimeZoneOffset        *string `json:"referenceTimeZoneOffset,omitempty"`
}

func ptrToStr[T any](val *T) string {
	if val != nil {
		return fmt.Sprintf("%v", *val)
	}
	return "nil"
}

func (s SnapshotSchedule) String() string {
	str := strings.Builder{}
	_, _ = str.WriteString("GroupID: " + s.GroupID)
	_, _ = str.WriteString(", ClusterID: " + s.ClusterID)
	_, _ = str.WriteString(", ClusterCheckpointIntervalMin: " + ptrToStr(s.ClusterCheckpointIntervalMin))
	_, _ = str.WriteString(", DailySnapshotRetentionDays: " + ptrToStr(s.DailySnapshotRetentionDays))
	_, _ = str.WriteString(", FullIncrementalDayOfWeek: " + ptrToStr(s.FullIncrementalDayOfWeek))
	_, _ = str.WriteString(", MonthlySnapshotRetentionMonths: " + ptrToStr(s.MonthlySnapshotRetentionMonths))
	_, _ = str.WriteString(", PointInTimeWindowHours: " + ptrToStr(s.PointInTimeWindowHours))
	_, _ = str.WriteString(", ReferenceHourOfDay: " + ptrToStr(s.ReferenceHourOfDay))
	_, _ = str.WriteString(", ReferenceMinuteOfHour: " + ptrToStr(s.ReferenceMinuteOfHour))
	_, _ = str.WriteString(", SnapshotIntervalHours: " + ptrToStr(s.SnapshotIntervalHours))
	_, _ = str.WriteString(", SnapshotRetentionDays: " + ptrToStr(s.SnapshotRetentionDays))
	_, _ = str.WriteString(", WeeklySnapshotRetentionWeeks: " + ptrToStr(s.WeeklySnapshotRetentionWeeks))

	return str.String()
}

func mergeExistingScheduleWithSpec(existingSnapshotSchedule SnapshotSchedule, specSnapshotSchedule mdb.SnapshotSchedule) SnapshotSchedule {
	snapshotSchedule := SnapshotSchedule{}
	snapshotSchedule.ClusterID = existingSnapshotSchedule.ClusterID
	snapshotSchedule.GroupID = existingSnapshotSchedule.GroupID
	snapshotSchedule.ClusterCheckpointIntervalMin = pickFirstIfNotNil(specSnapshotSchedule.ClusterCheckpointIntervalMin, existingSnapshotSchedule.ClusterCheckpointIntervalMin)
	snapshotSchedule.DailySnapshotRetentionDays = pickFirstIfNotNil(specSnapshotSchedule.DailySnapshotRetentionDays, existingSnapshotSchedule.DailySnapshotRetentionDays)
	snapshotSchedule.FullIncrementalDayOfWeek = pickFirstIfNotNil(specSnapshotSchedule.FullIncrementalDayOfWeek, existingSnapshotSchedule.FullIncrementalDayOfWeek)
	snapshotSchedule.MonthlySnapshotRetentionMonths = pickFirstIfNotNil(specSnapshotSchedule.MonthlySnapshotRetentionMonths, existingSnapshotSchedule.MonthlySnapshotRetentionMonths)
	snapshotSchedule.PointInTimeWindowHours = pickFirstIfNotNil(specSnapshotSchedule.PointInTimeWindowHours, existingSnapshotSchedule.PointInTimeWindowHours)
	snapshotSchedule.ReferenceHourOfDay = pickFirstIfNotNil(specSnapshotSchedule.ReferenceHourOfDay, existingSnapshotSchedule.ReferenceHourOfDay)
	snapshotSchedule.ReferenceMinuteOfHour = pickFirstIfNotNil(specSnapshotSchedule.ReferenceMinuteOfHour, existingSnapshotSchedule.ReferenceMinuteOfHour)
	snapshotSchedule.SnapshotIntervalHours = pickFirstIfNotNil(specSnapshotSchedule.SnapshotIntervalHours, existingSnapshotSchedule.SnapshotIntervalHours)
	snapshotSchedule.SnapshotRetentionDays = pickFirstIfNotNil(specSnapshotSchedule.SnapshotRetentionDays, existingSnapshotSchedule.SnapshotRetentionDays)
	snapshotSchedule.WeeklySnapshotRetentionWeeks = pickFirstIfNotNil(specSnapshotSchedule.WeeklySnapshotRetentionWeeks, existingSnapshotSchedule.WeeklySnapshotRetentionWeeks)

	return snapshotSchedule
}

func pickFirstIfNotNil[T any](first *T, second *T) *T {
	if first != nil {
		return first
	} else {
		return second
	}
}
