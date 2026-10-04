{{- define "zea.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "zea.fullname" -}}
{{- if contains .Chart.Name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "zea.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "zea.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "zea.selectorLabels" -}}
app.kubernetes.io/name: {{ include "zea.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: backend
{{- end }}

{{- define "zea.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "zea.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "zea.anchorApplication" -}}
{{- $anchor := .Values.anchorApplication | default (printf "%s:%s" .Release.Namespace .Release.Name) }}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-.a-z0-9]*[a-z0-9])?$" $anchor) }}
{{- fail (printf "anchorApplication must be <namespace>:<name>, got %q" $anchor) }}
{{- end }}
{{- $anchor }}
{{- end }}

{{- define "zea.proxyTokenSource" -}}
{{- $src := .Values.proxyToken.source | default "generate" }}
{{- if not (has $src (list "generate" "value" "existing")) }}
{{- fail (printf "proxyToken.source must be generate, value or existing, got %q" $src) }}
{{- end }}
{{- $src }}
{{- end }}

{{- define "zea.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}
