{{- define "ncli-relay.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "ncli-relay.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "ncli-relay.selector" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ncli-relay.secretName" -}}
{{- default (printf "%s-secrets" (include "ncli-relay.fullname" .)) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "ncli-relay.meiliHost" -}}
{{- printf "http://%s-meilisearch:7700" (include "ncli-relay.fullname" .) -}}
{{- end -}}

{{- define "ncli-relay.minioEndpoint" -}}
{{- printf "%s-minio:9000" (include "ncli-relay.fullname" .) -}}
{{- end -}}
