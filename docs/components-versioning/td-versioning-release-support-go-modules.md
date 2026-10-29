# Single go.mod or one per component

Subtab of [Engineering Proposal: Versioning and release of MCK components](td-versioning-release-support.md).

Decision: keep the root `go.mod`. Revisit if the triggers below happen.

What a separate module would give us: knowing exactly when a component's dependencies changed, and control over them independent of the operator.

- The proxy prototype (branch `KUBE-530/mongodb-proxy`) uses the standard library plus `go.uber.org/zap`. With the single `go.mod`, `go list -deps ./cmd/mongodb-proxy` gives its exact module closure: `zap v1.28.0` and `multierr v1.11.0`. The image is `FROM scratch`, so there is no base image to track beyond the Go toolchain.
- The reporter needs a Kubernetes client (Pod get and patch), so its closure will overlap heavily with the operator's. A separate module would gain little there.
- The existing components, the readiness probe, the version upgrade hook and `init-database`, build from packages shared with the community operator, so a separate module would need those packages moved or published as their own module.
- The repository was deliberately consolidated into one module (CLOUDP-332943, #288) so that `go test ./...` covers all Go code.

| | Single `go.mod` | One `go.mod` per component |
|---|---|---|
| Knowing when dependencies changed | Compare the `go list -deps` closure (module and version) of the component | Diff of the component's `go.mod`; Dependabot PRs scoped to its directory |
| Dependency control | Versions shared with the operator; one Dependabot config. Weekly grouped PRs include modules the proxy does not use | Independent versions; duplicated Kubernetes dependencies for the reporter |
| Tooling coverage | `./...` covers everything: `govulncheck` workflow, `go vet ./...`, `golangci-lint-full`, Dependabot directory `/` | `./...` stops at the module boundary. Each module needs separate wiring in `govulncheck`, `go vet` and tests, `golangci-lint`, Dependabot and `update_go_version.sh`, or it is silently skipped |
| Maintenance | Lowest | Go version and toolchain bumps per module; cross-module imports need versions or `replace` |

Path-based detection of code changes already works inside one module: the component changelog check and depguard use the component's directories. What stays unsolved in either layout is a dependency or toolchain update: it moves the minimum only when someone writes an entry, as the vulnerability review rule in the main proposal requires.

Revisit if: the proxy's dependency graph grows or diverges from the operator's, the reporter ends up with a small closure, or we want dependency updates to move the minimum automatically.
