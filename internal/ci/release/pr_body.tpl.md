## Summary

Release PR for MCK {{.Version}}.

Opened via `mck-ci open-release-pr`.

## Included changes

- Bump `mongodbOperator` to {{.Version}}
- Copy release Dockerfiles to `public/dockerfiles/*/{{.Version}}/ubi/`
- Regenerate release artifacts (Helm chart, manifests, CSV, licenses, RBAC)

## Checklist

- [ ] CI green
- [ ] `release.json` `mongodbOperator` = {{.Version}}
- [ ] Expected Dockerfiles present under `public/dockerfiles/*/{{.Version}}/`
