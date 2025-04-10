{{ range . }}
{{.Name}},{{.Version}},{{.LicenseURL}},{{.LicenseName}}
{{- end }}