{{/*
Expand the chart name, honoring nameOverride.
*/}}
{{- define "sorovista.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified release name.
*/}}
{{- define "sorovista.fullname" -}}
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

{{/*
Common labels.
*/}}
{{- define "sorovista.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "sorovista.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. These must stay stable across upgrades.
*/}}
{{- define "sorovista.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sorovista.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "sorovista.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "sorovista.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Validate sourceMode once, with a message naming the accepted values.
*/}}
{{- define "sorovista.validateMode" -}}
{{- if not (has .Values.sourceMode (list "rpc" "sorotrail")) }}
{{- fail (printf "sourceMode %q is invalid (want rpc or sorotrail)" .Values.sourceMode) }}
{{- end }}
{{- end }}
