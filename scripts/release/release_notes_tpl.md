# MCK {{ version }} Release Notes

{% if breaking_changes -%}
## Breaking Changes

{% for change in breaking_changes -%}
{{- change -}}
{%- endfor -%}
{%- endif -%}
