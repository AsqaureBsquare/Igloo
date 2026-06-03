{{/*
Expand the name of the chart.
*/}}
{{- define "igloo.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "igloo.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- printf "%s" $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "igloo.server.fullname" -}}
{{- printf "%s-server" (include "igloo.fullname" .) }}
{{- end }}

{{- define "igloo.provisioner.fullname" -}}
{{- printf "%s-provisioner" (include "igloo.fullname" .) }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "igloo.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "igloo.server.labels" -}}
{{ include "igloo.labels" . }}
app.kubernetes.io/name: {{ include "igloo.server.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: server
{{- end }}

{{- define "igloo.provisioner.labels" -}}
{{ include "igloo.labels" . }}
app.kubernetes.io/name: {{ include "igloo.provisioner.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: provisioner
{{- end }}

{{- define "igloo.server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "igloo.server.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "igloo.provisioner.selectorLabels" -}}
app.kubernetes.io/name: {{ include "igloo.provisioner.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account names
*/}}
{{- define "igloo.server.serviceAccountName" -}}
{{- if .Values.server.serviceAccount.create }}
{{- default (include "igloo.server.fullname" .) .Values.server.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.server.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "igloo.provisioner.serviceAccountName" -}}
{{- if .Values.provisioner.serviceAccount.create }}
{{- default (include "igloo.provisioner.fullname" .) .Values.provisioner.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.provisioner.serviceAccount.name }}
{{- end }}
{{- end }}
