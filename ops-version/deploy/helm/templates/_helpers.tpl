{{- define "opsversion.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "opsversion.fullname" -}}
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

{{- define "opsversion.frontend.fullname" -}}
{{- printf "%s-frontend" (include "opsversion.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "opsversion.backend.fullname" -}}
{{- printf "%s-backend" (include "opsversion.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "opsversion.backendAddr" -}}
{{ include "opsversion.backend.fullname" . }}:{{ .Values.backend.service.port }}
{{- end }}

{{- define "opsversion.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "opsversion.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: opsplane
{{- end }}

{{- define "opsversion.frontend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opsversion.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: frontend
{{- end }}

{{- define "opsversion.backend.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opsversion.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: backend
{{- end }}

{{- define "opsversion.antiAffinity" -}}
{{- $ctx := index . 0 -}}
{{- $selector := index . 1 -}}
{{- if $ctx.Values.podAntiAffinity.enabled }}
affinity:
  podAntiAffinity:
    {{- if $ctx.Values.podAntiAffinity.required }}
    requiredDuringSchedulingIgnoredDuringExecution:
      - topologyKey: kubernetes.io/hostname
        labelSelector:
          matchLabels:
{{ $selector | indent 12 }}
    {{- else }}
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
{{ $selector | indent 14 }}
    {{- end }}
{{- end }}
{{- end }}

{{- define "opsversion.tag" -}}
{{- $g := (.ctx.Values.global | default dict).tag | default "" -}}
{{- .img.tag | default $g | default .ctx.Chart.AppVersion -}}
{{- end -}}

{{- define "opsversion.requireSameTag" -}}
{{- $g := (.Values.global | default dict).tag | default "" -}}
{{- $b := .Values.backend.image.tag | default $g | default .Chart.AppVersion -}}
{{- $f := .Values.frontend.image.tag | default $g | default .Chart.AppVersion -}}
{{- if ne $b $f -}}
{{- fail (printf "前后端 tag 不一致：backend=%s frontend=%s。同一个 chart 必须一次发布、一次回滚 —— 版本错配不会报错，只会表现为某个接口 404 或字段缺失，最难排查。请用 global.tag 统一设置，或把两个组件的 image.tag 设成同一个值。" $b $f) -}}
{{- end -}}
{{- end -}}

{{- define "opsversion.image" -}}
{{- $reg := .ctx.Values.global.imageRegistry | default "" -}}
{{- $repo := .img.repository -}}
{{- $tag := include "opsversion.tag" . -}}
{{- if $reg -}}
{{- printf "%s/%s:%s" (trimSuffix "/" $reg) $repo $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{- define "opsversion.securityContext" -}}
runAsNonRoot: true
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "opsversion.requireEnv" -}}
{{- include "opsversion.requireSameTag" . -}}
{{- if .Values.requireEnvValues -}}
{{- fail "必须指定环境 values 文件：生产用 -f values-prod.yaml，本地用 -f values-local.yaml。chart 自带的 values.yaml 只是默认结构（镜像仓库为空、入口全关），装出来跑不起来。" -}}
{{- end -}}
{{- end -}}
