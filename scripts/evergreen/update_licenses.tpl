{{ range . }}
{{.Name}},{{.Version}},{{.LicenseName}}
{{- end }}