{{- define "k8sagnet.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "k8sagnet.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "k8sagnet.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Labels shared by every object; component is passed in by the caller. */}}
{{- define "k8sagnet.labels" -}}
helm.sh/chart: {{ include "k8sagnet.chart" .root }}
{{ include "k8sagnet.selectorLabels" . }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/part-of: {{ include "k8sagnet.name" .root }}
{{- end -}}

{{- define "k8sagnet.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8sagnet.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/* In-cluster DNS name of a component's service. */}}
{{- define "k8sagnet.svcHost" -}}
{{- printf "%s-%s.%s.svc.cluster.local" (include "k8sagnet.fullname" .root) .component .root.Release.Namespace -}}
{{- end -}}
