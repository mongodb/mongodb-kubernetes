{{/*
Expand the name of the chart.
*/}}
{{- define "atlas-basic.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "atlas-basic.fullname" -}}
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

{{- define "atlas-basic.projectfullname" -}}
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
{{- define "atlas-basic.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "atlas-basic.labels" -}}
helm.sh/chart: {{ include "atlas-basic.chart" . }}
{{ include "atlas-basic.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "atlas-basic.selectorLabels" -}}
app.kubernetes.io/name: {{ include "atlas-basic.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "generateRandomString" -}}
{{ randAlphaNum 5 }}
{{- end }}

{{- define "getInstanceSizeOrFail" -}}
{{ $instances := list "M0" "M2" "M5"}}
{{- if not (has .Values.deployment.instanceSize $instances)}}
{{- fail "Instance size can only be one of M0, M2, M5" }}
{{- end }}
{{- .Values.deployment.instanceSize }}
{{- end }}

{{- define "getProviderNameOrFail" -}}
{{ $providers := list "AWS" "GCP" "AZURE" }}
{{- if not (has .Values.deployment.providerName $providers) }}
{{- fail "Provider name can only be one of AWS, GCP, AZURE" }}
{{- end}}
{{- .Values.deployment.providerName }}
{{- end}}