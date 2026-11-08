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

{{/*
The digests the chart pins its default images to, by "repository:tag".
Chainguard tags MinIO only `latest`, so the tag alone pins nothing. To bump
one, take the Digest line from `docker buildx imagetools inspect <ref>`.
*/}}
{{- define "ncli-relay.imageDigests" -}}
"cgr.dev/chainguard/minio:latest": sha256:a1ac4589a5f8136b616e667b48a0bea9719bb10bd45799ea946f24bbedfafde4
"cgr.dev/chainguard/minio-client:latest": sha256:10388d580a724f356b94c9b2631b5a179039cabe8c76e9b37d86ed007d569b05
{{- end -}}

{{/*
An image reference: the repository, ":tag" (".default" when the tag is
empty) and "@digest". The digest is the image's own, else the pinned one
for exactly that repository and tag, so another repository or tag never
gets a default image's digest. Call with
(dict "image" .Values.x.image "default" "").
*/}}
{{- define "ncli-relay.image" -}}
{{- $ref := .image.repository -}}
{{- with (default .default .image.tag) }}{{ $ref = printf "%s:%s" $ref . }}{{ end -}}
{{- $pinned := include "ncli-relay.imageDigests" . | fromYaml -}}
{{- $ref -}}
{{- with (default (get $pinned $ref) .image.digest) }}@{{ . }}{{ end -}}
{{- end -}}
