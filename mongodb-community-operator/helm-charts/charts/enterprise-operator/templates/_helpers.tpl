{{/* vim: set filetype=mustache: */}}

{{/*
    Define the namespace in which the operator deployment will be running.
    This can be used to override the installation of the operator in the same namespace as the helm release
*/}}
{{- define "mongodb-enterprise-operator.namespace" -}}
{{- if .Values.operator.namespace -}}
{{- .Values.operator.namespace -}}
{{- else -}}
{{- .Release.Namespace -}}
{{- end -}}
{{- end -}}
