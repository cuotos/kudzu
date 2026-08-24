{{- define "kudzu.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kudzu.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "kudzu.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "kudzu.labels" -}}
app.kubernetes.io/name: {{ include "kudzu.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "kudzu.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kudzu.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kudzu.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/*
Name of the PVC backing the bundled Redis. An existingClaim wins; otherwise the
chart creates one named after the release.
*/}}
{{- define "kudzu.redis.claimName" -}}
{{- if .Values.redis.persistence.existingClaim -}}
{{- .Values.redis.persistence.existingClaim -}}
{{- else -}}
{{- printf "%s-redis-data" (include "kudzu.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
The volume spec for the Redis /data mount, minus the name. The chart is
agnostic to how storage is provided; resolution order is:
  1. persistence.volume   - a raw volume spec, spliced in verbatim
  2. persistence.existingClaim - a PVC you created out of band
  3. a PVC the chart creates (see redis-pvc.yaml)
  4. emptyDir             - persistence disabled (the default)
*/}}
{{- define "kudzu.redis.dataVolume" -}}
{{- if not .Values.redis.persistence.enabled -}}
emptyDir: {}
{{- else if .Values.redis.persistence.volume -}}
{{- toYaml .Values.redis.persistence.volume -}}
{{- else -}}
persistentVolumeClaim:
  claimName: {{ include "kudzu.redis.claimName" . }}
{{- end -}}
{{- end -}}

{{/*
Server args for the bundled Redis. An explicit redis.args wins; otherwise
persistence.enabled decides whether the AOF is on.
*/}}
{{- define "kudzu.redis.args" -}}
{{- if .Values.redis.args -}}
{{- toJson .Values.redis.args -}}
{{- else if .Values.redis.persistence.enabled -}}
["--appendonly", "yes", "--appendfsync", "everysec"]
{{- else -}}
["--save", "", "--appendonly", "no"]
{{- end -}}
{{- end -}}

{{/*
Fail fast when more than one storage source is given, so precedence is never
silently applied to a mistake.
*/}}
{{- define "kudzu.redis.validatePersistence" -}}
{{- with .Values.redis.persistence -}}
{{- if and .enabled .volume .existingClaim -}}
{{- fail "redis.persistence: set at most one of `volume` or `existingClaim`" -}}
{{- end -}}
{{- if and (not .enabled) (or .volume .existingClaim) -}}
{{- fail "redis.persistence: `volume`/`existingClaim` set but persistence.enabled is false" -}}
{{- end -}}
{{- end -}}
{{- end -}}
