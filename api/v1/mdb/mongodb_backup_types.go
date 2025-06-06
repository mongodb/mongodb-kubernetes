package mdb

import v1 "github.com/mongodb/mongodb-kubernetes/api/v1"

type BackupMode string

// Backup contains configuration options for configuring
// backup for this MongoDB resource
type Backup struct {
	// +kubebuilder:validation:Enum=enabled;disabled;terminated
	// +optional
	Mode BackupMode `json:"mode"`

	// AutoTerminateOnDeletion indicates if the Operator should stop and terminate the Backup before the cleanup,
	// when the MongoDB CR is deleted
	// +optional
	AutoTerminateOnDeletion bool `json:"autoTerminateOnDeletion,omitempty"`

	// +optional
	SnapshotSchedule *SnapshotSchedule `json:"snapshotSchedule,omitempty"`

	// Encryption settings
	// +optional
	Encryption *Encryption `json:"encryption,omitempty"`

	// Assignment Labels set in the Ops Manager
	// +optional
	AssignmentLabels []string `json:"assignmentLabels,omitempty"`
}

type SnapshotSchedule struct {
	// Number of hours between snapshots.
	// +kubebuilder:validation:Enum=6;8;12;24
	// +optional
	SnapshotIntervalHours *int `json:"snapshotIntervalHours,omitempty"`

	// Number of days to keep recent snapshots.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=365
	// +optional
	SnapshotRetentionDays *int `json:"snapshotRetentionDays,omitempty"`

	// Number of days to retain daily snapshots. Setting 0 will disable this rule.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=365
	// +optional
	DailySnapshotRetentionDays *int `json:"dailySnapshotRetentionDays"`

	// Number of weeks to retain weekly snapshots. Setting 0 will disable this rule
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=365
	// +optional
	WeeklySnapshotRetentionWeeks *int `json:"weeklySnapshotRetentionWeeks,omitempty"`
	// Number of months to retain weekly snapshots. Setting 0 will disable this rule.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=36
	// +optional
	MonthlySnapshotRetentionMonths *int `json:"monthlySnapshotRetentionMonths,omitempty"`
	// Number of hours in the past for which a point-in-time snapshot can be created.
	// +kubebuilder:validation:Enum=1;2;3;4;5;6;7;15;30;60;90;120;180;360
	// +optional
	PointInTimeWindowHours *int `json:"pointInTimeWindowHours,omitempty"`

	// Hour of the day to schedule snapshots using a 24-hour clock, in UTC.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=23
	// +optional
	ReferenceHourOfDay *int `json:"referenceHourOfDay,omitempty"`

	// Minute of the hour to schedule snapshots, in UTC.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=59
	// +optional
	ReferenceMinuteOfHour *int `json:"referenceMinuteOfHour,omitempty"`

	// Day of the week when Ops Manager takes a full snapshot. This ensures a recent complete backup. Ops Manager sets the default value to SUNDAY.
	// +kubebuilder:validation:Enum=SUNDAY;MONDAY;TUESDAY;WEDNESDAY;THURSDAY;FRIDAY;SATURDAY
	// +optional
	FullIncrementalDayOfWeek *string `json:"fullIncrementalDayOfWeek,omitempty"`

	// +kubebuilder:validation:Enum=15;30;60
	ClusterCheckpointIntervalMin *int `json:"clusterCheckpointIntervalMin,omitempty"`
}

type BackupStatus struct {
	StatusName string `json:"statusName"`
}

// Encryption contains encryption settings
type Encryption struct {
	// Kmip corresponds to the KMIP configuration assigned to the Ops Manager Project's configuration.
	// +optional
	Kmip *KmipConfig `json:"kmip,omitempty"`
}

// KmipConfig contains Project-level KMIP configuration
type KmipConfig struct {
	// KMIP Client configuration
	Client v1.KmipClientConfig `json:"client"`
}

func (b *Backup) IsKmipEnabled() bool {
	if b.Encryption == nil || b.Encryption.Kmip == nil {
		return false
	}
	return true
}

func (b *Backup) GetKmip() *KmipConfig {
	if !b.IsKmipEnabled() {
		return nil
	}
	return b.Encryption.Kmip
}
