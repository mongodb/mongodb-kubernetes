package mdb

import (
	"fmt"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"sort"
	"strings"
)

type AgentConfig struct {
	// +optional
	BackupAgent BackupAgent `json:"backupAgent,omitempty"`
	// +optional
	MonitoringAgent MonitoringAgent `json:"monitoringAgent,omitempty"`
	// +optional
	Mongod AgentLoggingMongodConfig `json:"mongod,omitempty"`
	// +optional
	ReadinessProbe ReadinessProbe `json:"readinessProbe,omitempty"`
	// +optional
	StartupParameters StartupParameters `json:"startupOptions"`
	// +optional
	LogLevel LogLevel `json:"logLevel"`
	// +optional
	MaxLogFileDurationHours int `json:"maxLogFileDurationHours"`
	// DEPRECATED please use mongod.logRotate
	// +optional
	LogRotate *automationconfig.CrdLogRotate `json:"logRotate,omitempty"`
	// DEPRECATED please use mongod.systemLog
	// +optional
	SystemLog *automationconfig.SystemLog `json:"systemLog,omitempty"`
}

// AgentLoggingMongodConfig contain settings for the mongodb processes configured by the agent
type AgentLoggingMongodConfig struct {
	// +optional
	// LogRotate configures log rotation for the mongodb processes
	LogRotate *automationconfig.CrdLogRotate `json:"logRotate,omitempty"`

	// LogRotate configures audit log rotation for the mongodb processes
	AuditLogRotate *automationconfig.CrdLogRotate `json:"auditlogRotate,omitempty"`

	// +optional
	// SystemLog configures system log of mongod
	SystemLog *automationconfig.SystemLog `json:"systemLog,omitempty"`
}

type BackupAgent struct {
	// +optional
	// LogRotate configures log rotation for the BackupAgent processes
	LogRotate *LogRotateForBackupAndMonitoring `json:"logRotate,omitempty"`
}

type MonitoringAgent struct {
	// +optional
	// LogRotate configures log rotation for the BackupAgent processes
	LogRotate *LogRotateForBackupAndMonitoring `json:"logRotate,omitempty"`
}

type LogRotateForBackupAndMonitoring struct {
	// Maximum size for an individual log file before rotation.
	// OM only supports ints
	SizeThresholdMB int `json:"sizeThresholdMB,omitempty"`
	// Number of hours after which this MongoDB Agent rotates the log file.
	TimeThresholdHrs int `json:"timeThresholdHrs,omitempty"`
}

// StartupParameters can be used to configure the startup parameters with which the agent starts. That also contains
// log rotation settings as defined here:
type StartupParameters map[string]string

type MonitoringAgentConfig struct {
	StartupParameters StartupParameters `json:"startupOptions"`
}

type EnvironmentVariables map[string]string

type ReadinessProbe struct {
	EnvironmentVariables `json:"environmentVariables,omitempty"`
}

func (a *AgentLoggingMongodConfig) HasLoggingConfigured() bool {
	if a.LogRotate != nil || a.AuditLogRotate != nil || a.SystemLog != nil {
		return true
	}
	return false
}

func (s StartupParameters) ToCommandLineArgs() string {
	var keys []string
	for k := range s {
		keys = append(keys, k)
	}

	// order must be preserved to ensure the same set of command line arguments
	// results in the same StatefulSet template spec.
	sort.SliceStable(keys, func(i, j int) bool {
		return keys[i] < keys[j]
	})

	sb := strings.Builder{}
	for _, key := range keys {
		if value := s[key]; value != "" {
			sb.Write([]byte(fmt.Sprintf(" -%s=%s", key, value)))
		} else {
			sb.Write([]byte(fmt.Sprintf(" -%s", key)))
		}
	}
	return sb.String()
}
