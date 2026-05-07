# MCK Dev Container

A dev container is a Docker-based development environment defined in `devcontainer.json` that VS Code (and other compatible editors) can build and connect to automatically. Dev containers can also be used with the [Dev Container CLI](https://github.com/devcontainers/cli) directly. Every contributor gets the same OS, tools, and configuration without manual setup: open the repository, accept the "Reopen in Container" prompt, and the environment is ready.

## Features

The following [dev container features](https://containers.dev/features) are installed into the image at build time:

| Feature | Purpose |
|---|---|
| `go:1` | Go 1.25 toolchain |
| `uv:1` | Fast Python package and venv manager |
| `kubectl-helm-minikube:1` | kubectl and Helm (minikube is disabled; kind is used instead) |
| `./features/helm-unittest` | Local custom feature that installs the [helm-unittest](https://github.com/helm-unittest/helm-unittest) plugin; depends on Helm from the feature above |
| `docker-in-docker:2` | Full Docker daemon inside the container |
| `aws-cli:1` | AWS CLI for logging in to ECR |
| `jira-cli:1` | Jira CLI |
| `shellcheck:1` | ShellCheck shell-script linter |
| `jq-likes:2` | jq and yq (both latest) |
| `fzf:1` | fzf fuzzy finder, used by [`scripts/dev/switch_context.sh`](../scripts/dev/switch_context.sh) |
| `kind:1` | Local Kubernetes clusters via kind |
| `helm-chart-testing:1` | Helm chart testing (`ct`) |
| `pre-commit:2` | Git pre-commit hook manager |

Each feature is a separate layer in the underlying Docker image built for the dev container, taking advantage of Docker's caching on repeated builds - like when creating another dev container instance for a new git worktree.

## Lifecycle scripts

Three lifecycle hooks run scripts at different stages. Each hook delegates to numbered sub-scripts in a dedicated directory, executed in lexicographic order. This makes it easy to add, remove, or reorder steps without touching the main script.

### `initializeCommand` — [`scripts/initialize.sh`](scripts/initialize.sh)

Runs **on the host machine** before the container is created or started. It:

1. Ensures `compose.generated.yml` and `compose.user.yml` exist (creates empty files on first run).
2. Runs all `scripts/initialize/*.sh` scripts:
   - [`evergreen-cli.sh`](scripts/initialize/evergreen-cli.sh) — reads the host `evergreen` binary version and writes the matching Linux binary URL into `compose.generated.yml` as a build argument, so the container installs an Evergreen CLI that matches the host.
   - [`git-worktree.sh`](scripts/initialize/git-worktree.sh) — if the workspace is a [git worktree](https://git-scm.com/docs/git-worktree), injects the common `.git` directory as a volume mount into `compose.generated.yml` so git commands work correctly inside the container.

### `onCreateCommand` — [`scripts/on-create.sh`](scripts/on-create.sh)

Runs **inside the container**, once at creation time. It:

1. Fixes ownership on all mounted volumes (Docker mounts them as root by default).
2. Runs all `scripts/on-create/*.sh` scripts:
   - [`10-go.sh`](scripts/on-create/10-go.sh) — runs `go mod download`.
   - [`20-python-venv.sh`](scripts/on-create/20-python-venv.sh) — creates the Python virtualenv via [`scripts/dev/recreate_python_venv.sh`](../scripts/dev/recreate_python_venv.sh).

### `postStartCommand` — [`scripts/post-start.sh`](scripts/post-start.sh)

VS Code automatically mounts the host's ssh-agent socket into the dev container, but it's only available in the main container - sidecars don't have access to it.

This hook runs **inside the container** on every start (including restarts). It starts a background `socat` process that projects the mounted host ssh-agent socket into a shared volume accessible by other services in the stack. This is required by `evg-host-proxy` to authenticate its SSH tunnel (see below).

You can also mount your ssh-agent socket into the dev container explicitly and set `$SSH_AUTH_SOCK` in `compose.user.yml` if you don't create the dev container with VS Code.

### Local `.user.sh` scripts

Any file matching `*.user.sh` inside `scripts/initialize/` or `scripts/on-create/` is executed by the respective loop but is gitignored. Use this to add personal setup steps — installing personal tools, configuring dotfiles, etc. — without touching shared history.

## Compose stack

The dev container runs as a [Docker Compose](https://docs.docker.com/compose/) stack defined in [`compose.yml`](compose.yml), with overrides from [`compose.generated.yml`](compose.generated.yml) and [`compose.user.yml`](compose.user.yml). All services share a private `172.28.0.0/16` bridge network.

The **`devcontainer`** service is the main container, built from [`Dockerfile`](Dockerfile). It mounts:
- The workspace (bind mount).
- Named volumes for Go module cache, Go build cache, uv cache, and Helm cache — shared across all dev container instances on the same host to avoid redundant downloads.
- Evergreen auth files (`~/.evergreen.yml`, `~/.kanopy`) from the host.
- A shared SSH agent socket volume used by `evg-host-proxy`.

The remaining three services — `k8s-proxy`, `evg-host-proxy`, and `gost-proxy` — form the proxy layer described below.

## k8s-proxy: automatic kubefwd replacement

Running `kubefwd` requires a manually-started process, and it stops working whenever you recreate a cluster. `k8s-proxy` eliminates this entirely. Once the dev container is running, the proxy intercepts DNS queries for `*.svc.cluster.local` addresses and dynamically allocates a network alias for each matching service or pod that port-forwards the exposed ports on the service endpoints.

`k8s-proxy` is built from the [`fealebenpae/kube-forwarding-proxy`](https://github.com/fealebenpae/kube-forwarding-proxy) repository. It runs with a fixed IP (`172.28.0.10`), which the `devcontainer` service uses as its DNS resolver.

Clusters are registered automatically by `PATCH`-ing their kubeconfig to the proxy:

```sh
curl -X PATCH --data-binary @"${kubeconfig_path}" "http://${K8S_FWD_PROXY}/kubeconfig"
```

Two scripts call this automatically:
- [`scripts/dev/setup_kind_cluster.sh`](../scripts/dev/setup_kind_cluster.sh) — after creating a local kind cluster, feeds the new kubeconfig to `k8s-proxy`.
- [`scripts/dev/evg_host.sh get-kubeconfig`](../scripts/dev/evg_host.sh) — after downloading a remote Evergreen host's kubeconfig, feeds it to `k8s-proxy`, making that cluster's services reachable with standard DNS names too.

## evg-host-proxy and gost-proxy: persistent Evergreen SSH tunnel

`evg_host.sh tunnel` required the user to manually start and keep alive an SSH session with explicit `-L` port-forward flags for each API server port. Any cluster change, reconnect, or container restart meant re-running it. `evg-host-proxy` and `gost-proxy` replace that with a persistent, zero-maintenance tunnel. **You never need to run `evg_host.sh tunnel` or `kubefwd` manually.**

### evg-host-proxy

Built from [`autossh.Dockerfile`](autossh.Dockerfile), this service uses `autossh` instead of plain `ssh` so the tunnel reconnects automatically on drop — no intervention required. It opens a SOCKS5 proxy on port 1080 that tunnels all TCP traffic to the Evergreen host; no per-port configuration is needed. Authentication uses the SSH agent socket forwarded by `post-start.sh`. The Evergreen host address is read from `scripts/dev/contexts/private-context` (gitignored).

### gost-proxy

`gost-proxy` wraps `evg-host-proxy`'s SOCKS5 tunnel in a standard HTTP CONNECT proxy on port 8080. This is necessary because `client-go` (the Go Kubernetes client) does not fully support SOCKS5 proxies directly, but does support HTTP CONNECT proxies via the kubeconfig `proxy-url` field.

### End-to-end wiring

The entire chain is wired up automatically:

1. **Kubeconfig patching** — `evg_host.sh get-kubeconfig` downloads the remote kubeconfig and patches every cluster entry with `proxy-url: http://gost-proxy:8080`:
   ```sh
   yq -i ".clusters[].cluster.proxy-url |= \"${EVG_HOST_PROXY}\"" "${kubeconfig_path}"
   ```

2. **Client-side proxy pickup** — `load_proxy_config()` in [`kubetester/kubetester.py`](../docker/mongodb-kubernetes-tests/kubetester/kubetester.py) reads `proxy-url` from the kubeconfig at client-creation time and applies it to the kubernetes client configuration. [`tests/conftest.py`](../docker/mongodb-kubernetes-tests/tests/conftest.py) calls the same function for multi-cluster client setup.

3. **Result** — all Kubernetes API calls to remote Evergreen clusters route through gost-proxy → autossh SOCKS5 tunnel → Evergreen host, with no developer action beyond running `evg_host.sh get-kubeconfig` once.

## Generated and user compose files

### [`compose.generated.yml`](compose.generated.yml)

Auto-generated by `initialize.sh` and its sub-scripts on the host before each container build. Do not edit it manually — it is overwritten on every initialize run. Create an initialize sub-script that idempotently applies changes. It currently records:

- The git worktree `.git` volume mount (written by `git-worktree.sh` when applicable).
- The `EVERGREEN_CLI_URL` build argument that installs the correct Evergreen CLI binary into the image (written by `evergreen-cli.sh`).

### [`compose.user.yml`](compose.user.yml)

User-local overrides. `initialize.sh` creates it as an empty file on first run so Docker Compose always finds it. It is gitignored, so every developer can add personal customizations — extra volumes, environment variables, port mappings — without affecting others.
