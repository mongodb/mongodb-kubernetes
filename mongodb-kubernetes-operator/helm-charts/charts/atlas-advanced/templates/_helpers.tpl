{{/*
Expand the name of the chart.
*/}}
{{- define "atlas-advanced.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "atlas-advanced.fullname" -}}
{{- if .Values.deployment.name }}
{{- .Values.deployment.name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.deployment.name }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "atlas-advanced.projectfullname" -}}
{{- if .Values.project.name }}
{{- .Values.project.name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.project.name }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "atlas-advanced.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "atlas-advanced.labels" -}}
helm.sh/chart: {{ include "atlas-advanced.chart" . }}
{{ include "atlas-advanced.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "atlas-advanced.selectorLabels" -}}
app.kubernetes.io/name: {{ include "atlas-advanced.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "generateRandomString" -}}
{{ randAlphaNum 5 }}
{{- end }}

{{- define "getInstanceSizeOrFail" -}}
{{- $arg := . -}}
{{ $instances := list "M10" "M20" "M30" "M40" "M50" "M60" "M80" "M100" "M140" "M200" "M300" "R40" "R50" "R60" "R80" "R200" "R300" "R400" "R700" "M40_NVME" "M50_NVME" "M60_NVME" "M80_NVME" "M200_NVME" "M400_NVME" }}
{{- if not (has (toString $arg) $instances)}}
{{- fail (printf "Instance size can only be one of: %s " (join "," $instances)) }}
{{- end }}
{{- $arg -}}
{{- end }}

{{- define "getProviderNameOrFail" -}}
{{- $arg := . -}}
{{ $providers := list "AWS" "GCP" "AZURE" }}
{{- if not (has (toString $arg) $providers) }}
{{- fail (printf "Provider name can only be one of: %s. Got %s" (join "," $providers) $arg) }}
{{- end }}
{{- $arg -}}
{{- end }}