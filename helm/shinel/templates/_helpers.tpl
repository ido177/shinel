{{/*
Expand the name of the chart.
*/}}
{{- define "shinel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shinel.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "shinel.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "shinel.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "shinel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "shinel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* In-cluster Redis or an external URL in redis.url. */}}
{{- define "shinel.redisVault" -}}
{{- $r := .Values.redis | default dict -}}
{{- if or $r.enabled $r.url -}}true{{- end -}}
{{- end }}
