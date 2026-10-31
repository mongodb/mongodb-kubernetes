# Devcontainer worktree architecture

A worktree here is an ordinary checkout of the repository with a worktree prefix of its own, `devc<N>-`, plus a
devcontainer that mounts the checkout. The prefix marks everything the worktree owns: its kind clusters, its test
namespace, its MetalLB addresses, its compose project and, on an Evergreen host, its directory `/home/ubuntu/devc<N>/`.
It is kept in `.generated/worktree-context`. Every script of the repository runs in the worktree as in a checkout you
made by hand; `wt-ctl` only writes that file and starts things. To use it, follow
[devcontainer-worktrees.md](devcontainer-worktrees.md).

## Where things are

| Path | What |
|---|---|
| `scripts/dev/wt-ctl` | Shim: loads the invoking checkout's env through `scripts/dev/devenv`, then runs `python3 -m wt_ctl` |
| `scripts/dev/wt_ctl/` | `cli.py` has the parser and the dispatch, `create.py`, `delete.py`, `status.py` and `auth_cli.py` one verb each. `runner.py` is the only module that starts processes. `domains/` wraps one outside system each: the worktree index, kind, the devcontainer CLI and compose, kubeconfigs, the kfp daemon, registry credentials, git worktrees |
| `.devcontainer/` | Image (`Dockerfile`, `features/`), `compose.yml`, lifecycle scripts in `scripts/`, the tmux layout `tmuxp/mck.yaml` |
| `scripts/dev/op_run.sh`, `e2e_run.sh` | The operator and one e2e marker, run inside the container |
| `scripts/dev/wt_ctl/tests/` | Unit tests of the pure functions: `python -m pytest scripts/dev/wt_ctl/tests` |

## The worktree

`wt-ctl create <branch>` creates the worktree with master's `scripts/dev/create_worktree.sh`, or adopts the worktree
that has the branch checked out with `init_worktree.sh`. It deletes the state those scripts copy from the invoking
checkout (kubeconfigs, registry store), and writes two files:

- `.devcontainer/.env` pins the worktree index `N` (0 to 126) and the compose network values. Compose cannot call code, so
  it does the arithmetic of the network itself: a `/23` per worktree in `172.16.0.0/16`, the container addresses in its
  lower `/24` and the k8s-proxy virtual addresses in the upper one.
- `.generated/worktree-context` has literal `export` lines: `WORKTREE_PREFIX="devc<N>-"` and the six
  `KIND_METALLB_RANGE_*` pools of `root-context` with the third octet set to `128 + N`. `root-context` sources it, so
  the prefix reaches the kind cluster names, the test namespace and the MetalLB pools of every script. `.generated/` is
  already gitignored, and `evg_host.sh sync` copies this one file to an Evergreen host.

The index is recorded only in the checkout's `.devcontainer/.env`. `create` takes the lowest index that no worktree of
the repository pins, under a lock in the main checkout's `.generated/wt-ctl/`, and removing the worktree frees it.

All worktrees share one docker network, `kind`, and one daemon, so the pools of the worktrees must not overlap: the row
`128 + N` of the `172.18.0.0/16` network belongs to worktree `N`, and `devc<N>-` keeps the cluster names apart.
`scripts/dev/recreate_kind_cluster.sh` and `recreate_kind_clusters.sh` delete only clusters with the worktree's prefix.

## Two sides, one checkout

The checkout is bind-mounted at `/workspace`, so `.generated/` and `logs/` are the same files on the host and in the
container. A file whose content differs per side has `host` or `devc` in its name: `switch_context.sh` writes
`.generated/context.env` (both sides) and `context.<side>.env` (PATH and every path of the checkout, of the Evergreen
workdir or of `$HOME`), and `scripts/dev/devenv` loads the shared file and then the file of the side it runs on. On an
Evergreen task host the shared file also holds the whole environment of the shell that ran `make switch`: the agent's
`TMPDIR` and `GOCACHE`, and the cache directories `wt-ctl` sets. Those name host paths, so a container does not load a
line of the shared file that is also in `context.host.env`: it keeps the value it had, or none, and its own file supplies
the paths of its side. The Go root is the one other value `devenv` puts back (the container has its own). `root-context`
also picks the side's kubeconfig:

| File | Used by | Servers |
|---|---|---|
| `.generated/current.kubeconfig` | the host | `127.0.0.1:<port>` of the kind nodes |
| `.generated/current.devc.kubeconfig` | the container | `host.docker.internal:<port>` on macOS, `<node ip>:6443` on Linux, no TLS verification |
| `.generated/multicluster_kubeconfig`, `multicluster.devc.kubeconfig` | the operator and the tests of a multi-cluster worktree | the same split, but the container's file keeps each cluster's CA and TLS verification: the operator's member cluster health check verifies the certificate, which names `host.docker.internal` and the node address |

On Linux the devcontainer and its k8s-proxy join the `kind` network, because the apiserver ports are bound to the
host's loopback. `wt-ctl` writes the devc files from the host file in `domains/kubeconfig.py` and pins the current
context of the host file to `CLUSTER_NAME`.

## What `create` runs

Each step logs to `logs/setup_worktree/<step>.log` in the new worktree.

1. `worktree`: `create_worktree.sh <branch>`; `init_worktree.sh` for a worktree of the branch that has no
   `private-context` yet; nothing for one that has.
2. `prefix`: the index, `.devcontainer/.env` and `.generated/worktree-context`.
3. `context`: `switch_context.sh` for the requested context, then for the default one of the other topology when the
   requested topology does not match. The result is recorded in `.generated/.current_topology`.
4. `registry credentials`: the worktree's store `.generated/registry-auth/config.json` gets ECR tokens with time left.
5. `evergreen host` and `kind clusters`: with an Evergreen host (the default), the host is found or spawned, its
   address and key are recorded in `.generated/`, and `evg_host.sh` syncs, recreates the clusters there and copies the
   kubeconfig back, as the guide describes. Otherwise `recreate_kind_cluster.sh kind` or `recreate_kind_clusters.sh`,
   after a check that the kind CLI can create the node image. `MCK_KIND_REGISTRY_AUTH_DIR` points `setup_kind_cluster.sh` at the store's directory, which
   the nodes mount as `/.docker`. `--skip-recreate` leaves the clusters alone, except local ones that fail the health
   check of `KindDomain.unhealthy_reason` (node containers running, API server answering, nodes `Ready`, no crashing
   pod in `kube-system`), which are recreated. `wt-ctl status` runs the same check. `--skip-evg` leaves out every
   Evergreen call.
6. `kubeconfig`: the container's copies of the kubeconfig, and the host one registered with the kfp daemon on a Mac.
7. `devcontainer`: `.devcontainer/scripts/initialize.sh` (compose override files, the main `.git` mount), then
   `devcontainer build` and `up`, then the store as the container's `~/.docker/config.json`, then the `kind` network
   join on Linux, or the check that the `evg-host-proxy` sidecar reaches the Evergreen host.
8. `prepare-local-e2e`: in the container, stop the operator, wait for the apiserver, `make prepare-local-e2e`, then
   refresh the kubeconfigs (an Evergreen worktree's `multicluster.devc.kubeconfig` gets its proxy-url). Left out with
   `--skip-prepare-e2e`.
9. `operator`: `op_run.sh --detach`, which runs `go run ./main.go` in the tmux session `mck-operator`.

The compose project is `<branch_dir>_devcontainer`, lower case, with the services `devcontainer` and `k8s-proxy` (the
kube-forwarding-proxy: the container's DNS server, which answers `*.svc.cluster.local` with virtual addresses and
forwards them into the cluster) and the proxy sidecars `evg-host-proxy` and `gost-proxy` that worktrees on an Evergreen host use.
`delete` takes the compose project down with its images and its `venv`, `bin` and `ssh-auth-sock` volumes. The cache
volumes `mck-devcontainer-{gomodcache,gocache,uvcache,helmcache}` are shared and stay.

## What is not isolated

The docker daemon and its `kind` network, the cache volumes, the macOS kube-forwarding-proxy daemon, and the Cloud QA
organization: a worktree's Ops Manager projects are named after its namespace, so `delete --om` removes only the projects of
its prefix. The guide lists the host files a worktree reads and the mount of the main `.git`.
