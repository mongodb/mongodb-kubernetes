# envtest-based Go tests

[envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) boots a local
Kubernetes control plane (etcd + kube-apiserver) for Go tests, letting them run against
a real API server — e.g. to verify CRD CEL validation rules — without a cluster.

## Running

The envtest binaries are provisioned automatically by all unit test entry points
(`make golang-tests`, `make test`, CI's `unit_tests_golang` task); they are downloaded
once into `bin/envtest/` and exposed to `go test` via `KUBEBUILDER_ASSETS`.

To run a test package directly (or from an IDE), provision the binaries once —
the `env.Start` helper locates them in `bin/envtest/` on its own:

```shell
make envtest-assets
go test -v ./api/mongodb/v1/search/...   # or any other package with envtest tests
```

`ENVTEST_K8S_VERSION` (Makefile) tracks the minimum supported Kubernetes version from
`kubernetes-versions.json`; override with `make envtest-assets ENVTEST_K8S_VERSION=1.35.x`.

## Writing a new envtest test

Use the shared helper in `test/envtest/env` — it starts the control plane, installs the
CRDs from `config/crd/bases` and returns a client with the `mongodb.com/v1` scheme
registered. It works from any package — co-locate CEL tests next to the API types that
define the rules (see below), or use it from controller tests:

```go
func TestSomething(t *testing.T) {
    testEnv := env.Start(t, env.WithCRDs("mongodb.com_mongodbsearch.yaml"))
    k8sClient := testEnv.Client
    // ... create/update objects, assert API server behaviour
}
```

Guidelines:

- Each test *package* boots its own control plane (`go test` compiles every package
  into a separate binary, so environments cannot be shared across packages). Call
  `env.Start` once per top-level test and share it across subtests to keep boot cost low.
- Pass `env.WithCRDs(...)` to install only the CRDs the test needs (faster boot);
  omit it to install all of `config/crd/bases`.
- Missing binaries or CRD paths fail the test immediately — this is deliberate, so CI
  can never silently skip envtest coverage.
- envtest runs no controllers: objects are stored and validated, but nothing reconciles
  them, and namespaces cannot be deleted.

See `api/mongodb/v1/search/mongodbsearch_cel_envtest_test.go` for a complete example:
it verifies the CEL rules declared on the `MongoDBSearch` type in the very same package.
