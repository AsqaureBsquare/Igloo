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

{{/*
Bundled PostgreSQL. Used only when enabled and no external database is configured
and the server is not in --local (in-memory) mode.
*/}}
{{- define "igloo.postgres.fullname" -}}
{{- printf "%s-postgres" (include "igloo.fullname" .) }}
{{- end }}

{{- define "igloo.postgres.selectorLabels" -}}
app.kubernetes.io/name: {{ include "igloo.postgres.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "igloo.postgres.labels" -}}
{{ include "igloo.labels" . }}
{{ include "igloo.postgres.selectorLabels" . }}
app.kubernetes.io/component: postgres
{{- end }}

{{- define "igloo.postgres.bundled" -}}
{{- if and .Values.postgresql.enabled (not .Values.server.localMode) (not .Values.server.database.postgresURL) (not .Values.server.database.mongoURL) -}}true{{- end -}}
{{- end }}

{{/*
Password: explicit value, else the one already stored in the cluster (so upgrades
don't rotate it out from under the data volume), else a fresh random one.
*/}}
{{- define "igloo.postgres.password" -}}
{{- if .Values.postgresql.password -}}
{{- .Values.postgresql.password -}}
{{- else -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "igloo.postgres.fullname" .) -}}
{{- if and $existing $existing.data (index $existing.data "password") -}}
{{- index $existing.data "password" | b64dec -}}
{{- else -}}
{{- randAlphaNum 32 -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Image reference with the registry host swapped for global.imageRegistry, so one value
points every image at an internal registry or mirror. Only a leading host (contains "."
or ":" or is "localhost") is replaced; names without one, like postgres:16-alpine or
igloo-server:dev, are prefixed. With no override the image is returned unchanged.
Usage: include "igloo.image" (dict "root" . "image" "ghcr.io/org/app:tag")
*/}}
{{- define "igloo.image" -}}
{{- $reg := trimSuffix "/" (default "" .root.Values.global.imageRegistry) -}}
{{- if not $reg -}}
{{- .image -}}
{{- else -}}
{{- $parts := splitList "/" .image -}}
{{- $first := first $parts -}}
{{- if and (gt (len $parts) 1) (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- $parts = rest $parts -}}
{{- end -}}
{{- printf "%s/%s" $reg (join "/" $parts) -}}
{{- end -}}
{{- end }}

{{/* imagePullSecrets block for a pod spec; empty when none are configured. */}}
{{- define "igloo.imagePullSecrets" -}}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
{{- range . }}
  - name: {{ . | quote }}
{{- end }}
{{- end }}
{{- end }}

{{/* Egress gateway (igloo-guard) */}}
{{- define "igloo.guard.fullname" -}}
{{- printf "%s-guard" (include "igloo.fullname" .) }}
{{- end }}

{{- define "igloo.guard.selectorLabels" -}}
app.kubernetes.io/name: {{ include "igloo.guard.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "igloo.guard.labels" -}}
{{ include "igloo.labels" . }}
{{ include "igloo.guard.selectorLabels" . }}
app.kubernetes.io/component: guard
{{- end }}

{{/* Where workspaces reach the gateway (they live in another namespace, so use the full name). */}}
{{- define "igloo.guard.proxyURL" -}}
{{- printf "http://%s.%s.svc.cluster.local:%v" (include "igloo.guard.fullname" .) .Release.Namespace .Values.guard.port }}
{{- end }}

{{/* Shared secret between the server and the gateway: set explicitly, else kept from the cluster, else generated. */}}
{{- define "igloo.guard.key" -}}
{{- if .Values.guard.key -}}
{{- .Values.guard.key -}}
{{- else -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "igloo.guard.fullname" .) -}}
{{- if and $existing $existing.data (index $existing.data "key") -}}
{{- index $existing.data "key" | b64dec -}}
{{- else -}}
{{- randAlphaNum 40 -}}
{{- end -}}
{{- end -}}
{{- end }}
