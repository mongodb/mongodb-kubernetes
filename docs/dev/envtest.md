# envtest-based Go tests

[envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) boots a local
Kubernetes control plane (etcd + kube-apiserver) for Go tests, letting them run against
a real API server — e.g. to verify CRD CEL validation rules — without a cluster.

## Running

The envtest binaries are provisioned automatically by all unit test entry points
(`make golang-tests`, `make test`, CI's `unit_tests_golang` task); they are downloaded
once into `bin/envtest/` and exposed to `go test` via `KUBEBUILDER_ASSETS`.

To run a test package directly (or from an IDE), provision the binaries once —
the `env` helper locates them in `bin/envtest/` on its own:

```shell
make envtest-assets
go test -v ./api/mongodb/v1/search/...   # or any other package with envtest tests
```

`ENVTEST_K8S_VERSION` (Makefile) is derived from the minimum supported Kubernetes version
in `kubernetes-versions.json`; override with `make envtest-assets ENVTEST_K8S_VERSION=1.35.x`.

## Writing a new envtest test

Use the shared helper in `test/envtest/env` — it starts the control plane, installs the
CRDs from `config/crd/bases` and returns a client with the `mongodb.com/v1` and
`operator.mongodb.com/v1` schemes registered. It works from any package — co-locate
CEL tests next to the API types that define the rules (see below), or use it from
controller tests:

```go
func TestMain(m *testing.M) {
    os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbsearch.yaml")))
}

func TestSomething(t *testing.T) {
    k8sClient := env.Shared(t).Client
    // ... create/update objects, assert API server behaviour
}
```

Guidelines:

- Each test *package* boots its own control plane (`go test` compiles every package
  into a separate binary, so environments cannot be shared across packages). One boot
  takes a few seconds, so **boot exactly once per package** from `TestMain` with
  `env.RunShared` and access the environment from every test via `env.Shared(t)`:
  ```go
  func TestMain(m *testing.M) {
      os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbsearch.yaml")))
  }
  ```
- Pass `env.WithCRDs(...)` to install only the CRDs the test needs (faster boot);
  omit it to install all of `config/crd/bases`.
- Missing binaries or CRD paths fail the test immediately — this is deliberate, so CI
  can never silently skip envtest coverage.
- envtest runs no controllers: objects are stored and validated, but nothing reconciles
  them, and namespaces cannot be deleted.

See `api/mongodb/v1/search/mongodbsearch_cel_envtest_test.go` for a complete example:
it verifies the CEL rules declared on the `MongoDBSearch` type in the very same package.
`api/operator/v1` shows the multi-file variant: `main_test.go` boots the shared
environment and each `*_envtest_test.go` covers one API type's validation rules.
