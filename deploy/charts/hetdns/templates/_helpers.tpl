{{/* Expand the chart name. */}}
{{- define "hetdns.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Create a release-specific, DNS-safe resource name. */}}
{{- define "hetdns.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := include "hetdns.name" . }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "hetdns.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "hetdns.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "hetdns.selectorLabels" -}}
app.kubernetes.io/name: {{ include "hetdns.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "hetdns.replicaGuard" -}}
{{- if gt (int .Values.replicaCount) 1 }}
{{- fail "hetdns is a singleton: replicaCount must not be greater than 1" }}
{{- end }}
{{- end }}

{{- define "hetdns.configMapName" -}}
{{- default (include "hetdns.fullname" .) .Values.config.existingConfigMap }}
{{- end }}

{{- define "hetdns.tokenSecretName" -}}
{{- required "tokenSecret.name is required" .Values.tokenSecret.name }}
{{- end }}

{{/* Keep the declared container port aligned with the application's listen address. */}}
{{- define "hetdns.listenPort" -}}
{{- $listen := required "runtime.listen is required" .Values.runtime.listen -}}
{{- if not (regexMatch "^.*:[0-9]+$" $listen) -}}
{{- fail "runtime.listen must end with a TCP port (for example :8080)" -}}
{{- end -}}
{{- $port := atoi (regexFind "[0-9]+$" $listen) -}}
{{- if or (lt $port 1) (gt $port 65535) -}}
{{- fail "runtime.listen port must be between 1 and 65535" -}}
{{- end -}}
{{- $port -}}
{{- end }}

{{- define "hetdns.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end }}
