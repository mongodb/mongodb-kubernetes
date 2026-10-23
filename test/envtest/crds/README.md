# Test CRDs

`cert-manager.io_certificates.yaml` and `cert-manager.io_issuers.yaml` are cert-manager's CRDs for
its `Certificate` and `Issuer` kinds (Apache-2.0), taken from `github.com/cert-manager/cert-manager`
v1.18.5, `deploy/crds/crd-certificates.yaml` and `crd-issuers.yaml`. Only the Helm templating was
stripped: the files there carry template expressions in their labels, which the API server rejects.

They are installed by `env.WithCertManagerCRDs()` so tests can exercise operator-managed
certificates against a real API server. There is no cert-manager controller in envtest, so the
tests satisfy the `Certificate` objects themselves.

When the cert-manager dependency in `go.mod` is bumped, refresh these two files the same way.
