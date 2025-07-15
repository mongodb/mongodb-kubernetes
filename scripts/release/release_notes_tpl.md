# MCK {{ version }} Release Notes
{% if preludes %}
{% for prelude in preludes -%}
{{- prelude }}
{%- endfor -%}
{%- endif -%}
{% if breaking_changes %}
## Breaking Changes

{% for change in breaking_changes -%}
{{- change -}}
{%- endfor -%}
{%- endif -%}
{% if features %}
## New Features

{% for feature in features -%}
{{- feature -}}
{%- endfor -%}
{%- endif -%}
{% if fixes %}
## Bug Fixes

{% for fix in fixes -%}
{{- fix -}}
{%- endfor -%}
{%- endif -%}
{% if others %}
## Other Changes

{% for other in others -%}
{{- other -}}
{%- endfor -%}
{%- endif -%}
