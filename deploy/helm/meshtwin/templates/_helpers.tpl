{{/* Standard labels; pass (dict "root" $ "component" "api"). */}}
{{- define "meshtwin.labels" -}}
app.kubernetes.io/name: meshtwin
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "meshtwin.selector" -}}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/* Pod-level restricted security context; pass the image's non-root uid. */}}
{{- define "meshtwin.podSecurity" -}}
runAsNonRoot: true
runAsUser: {{ . }}
runAsGroup: {{ . }}
fsGroup: {{ . }}
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/* Container-level restricted security context (same for every container). */}}
{{- define "meshtwin.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end }}

{{- define "meshtwin.s3Secret" -}}
{{ .Values.s3.existingSecret | default (printf "%s-s3" .Release.Name) }}
{{- end }}

{{/* S3 client settings shared by ingester, api and analytics (plain S3, path-style). */}}
{{- define "meshtwin.s3Env" -}}
- name: S3_ENDPOINT
  value: {{ .Release.Name }}-seaweedfs:8333
- name: S3_TLS
  value: "false"
- name: S3_BUCKET
  value: {{ .Values.s3.bucket }}
- name: S3_ACCESS_KEY
  valueFrom: {secretKeyRef: {name: {{ include "meshtwin.s3Secret" . }}, key: access-key}}
- name: S3_SECRET_KEY
  valueFrom: {secretKeyRef: {name: {{ include "meshtwin.s3Secret" . }}, key: secret-key}}
{{- end }}
