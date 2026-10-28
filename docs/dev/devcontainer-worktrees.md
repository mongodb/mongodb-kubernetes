# Devcontainer worktrees

`scripts/dev/wt-ctl` gives each branch its own worktree: a sibling git worktree, a devcontainer that mounts it, kind
clusters on an Evergreen host or on your laptop and a local operator. Two branches run side by side on one docker
daemon, or on one Evergreen host.
[devcontainer-architecture.md](devcontainer-architecture.md) explains how the pieces fit together.

## The worktree prefix

Each worktree has a prefix, `devc<N>-`, where `N` is a number from 0 to 126 that `create` picks (the lowest one no
worktree of the repository has). The prefix, kept in `.generated/worktree-context` as `WORKTREE_PREFIX`, marks what
the worktree owns:

- its kind clusters (`devc<N>-kind`, `devc<N>-e2e-operator`, and so on) and their kubeconfig,
- its test namespace, and its row of MetalLB addresses (the third octet `128 + N`),
- its devcontainer and compose project, and
- on an Evergreen host, its directory `/home/ubuntu/devc<N>/`.

An Evergreen host is either exclusive to one worktree or shared by several worktrees, which then have different
prefixes. A checkout without the file, like one you made by hand, has no prefix and owns the whole docker daemon or host.

## Prerequisites

- Docker, the `devcontainer` CLI (`npm i -g @devcontainers/cli`), `kind`, `python3` and `tmux`.
- For a worktree on an Evergreen host: the `evergreen` CLI, logged in, `ssh`, `rsync`, `jq`, VPN, and an ssh agent that
  holds the key Evergreen puts on its hosts (`ssh-add ~/.ssh/evg-host`).
- A checkout to run `wt-ctl` from that has been through `make switch context=<context>`, so that `.generated/` and
  `scripts/dev/contexts/private-context` exist (the `private-context-template` shows what to fill in). Build its venv
  with `scripts/dev/recreate_python_venv.sh`: `wt-ctl create` needs PyYAML from it to rewrite kubeconfigs.
- `private-context` must set `LOCAL_OPERATOR=true` inside the container, for example with
  `if [ -f /.dockerenv ]; then export LOCAL_OPERATOR=true; fi`. The template sets `false`, and nothing in `wt-ctl` or
  `.devcontainer/` overrides it: with `false`, `make prepare-local-e2e` and the tests skip the local operator setup.
- A `kind` release newer than v0.30 when `kubernetes-versions.json` names a 1.37 node image, which v0.30 cannot
  create. With an older `kind`, `create` stops before it deletes anything and prints a `KIND_NODE_IMAGE=<image>:v1.36.1`
  override to run it with.

## Commands

Run `create` from the checkout you branch from, or inside a worktree you made by hand. The other verbs act on the
worktree of the current directory, except `delete`, which names the branch.

- `wt-ctl create [--local-kind] [--single-cluster|--multi-cluster] [--context <name>] [<branch>]` creates the worktree
  `../<branch with / as _>`, or adopts the worktree that already has the branch checked out (one you made with
  `git worktree add`, at any path). Without `<branch>` it adopts the checkout you run it in. Adopting copies the
  `private-context`, the `bin/` and a context into the worktree and builds its venv, which a bare worktree lacks: from
  the checkout you run `create` in, or from the main checkout when you run it inside the worktree. A bare worktree has
  no env of its own, so its `wt-ctl` runs the one of the main checkout, which must have the tooling. A worktree that has a `private-context` but no `.generated/.current_context` is refused: run `make switch` in it first. Then it gives the worktree the next free index and its prefix, switches its context, creates its kind clusters (on an Evergreen
  host, or on this machine with `--local-kind`), builds and starts the devcontainer, runs `make prepare-local-e2e` in
  it and starts the operator. Multi-cluster is the default.
  `--context` takes a file from `scripts/dev/contexts`; without it a worktree keeps the context of the checkout you ran
  `create` in, or switches to `e2e_multi_cluster_kind` (`root-context` with `--single-cluster`) when that one is of the
  other kind. A worktree starts with the `private-context` and the `.devcontainer/compose.user.yml` of that checkout. Every
  step is idempotent: if `create` fails or you interrupt it, run the same command again. A rerun recreates the kind
  clusters, which takes minutes, unless you add `--skip-recreate`: add it when the clusters came up and the failure
  was later. `--skip-prepare-e2e` leaves out `make prepare-local-e2e`, which resets the test namespace and deletes
  what a running operator watches. `--skip-evg` is for a worktree that already has
  its host and clusters: it makes no Evergreen call and uses the kubeconfig in `.generated/` (see "Evergreen hosts").
  A rerun keeps the kind of the worktree: a worktree without a host stays local.
- `wt-ctl status` lists the worktrees of the repository with their kind clusters, their health (local worktrees only,
  see "Cluster health") and devcontainer state.
- `wt-ctl attach` opens the tmux session of the container (k9s, the operator log, the test log and a shell) and needs a
  terminal. Everything after `attach --` runs in the container instead, without one and without a login shell: wrap a
  command that needs `KUBECONFIG` or the venv, as in `attach -- bash -lc 'kubectl get nodes'`.
- `wt-ctl kubeconfig refresh` rewrites the container's copies of the host kubeconfig. Run it on the host, not in the
  container, after a hand-run `make prepare-local-e2e` and after a kind cluster is recreated: until then the container
  has the old servers.
- `wt-ctl kfp [status|register]` shows the state of the kube-forwarding-proxy daemon on a Mac and the contexts it holds,
  or registers this worktree's host kubeconfig with it (see "Host DNS on a Mac").
- `wt-ctl auth status` shows the time left on the registry credentials of the worktree, and `wt-ctl auth refresh
  [--force]` renews and rolls them (see "Registry credentials").
- `wt-ctl network allocate [<branch>]` prints the `.devcontainer/.env` and `.generated/worktree-context` lines that `create` would
  write for the next free index, and reserves nothing. `--quiet`, before the verb, drops the `[wt-ctl] worktree=...`
  banner on stderr.
- `wt-ctl delete <branch>` removes only what you ask for: `--om` (the worktree's Cloud QA projects), `--devcontainer` (the
  compose project with its images and its own volumes, and the worktree's contexts in the kfp daemon), `--evg` (the worktree's
  clusters and directory on its Evergreen host, see below), `--clusters` (the local kind clusters whose name starts
  with the worktree's prefix), `--worktree` (the branch stays), or `--all`. A worktree with uncommitted changes stops
  `delete --worktree` and `--all` before the first step, with nothing removed; `--force` removes it anyway. Run
  `delete` from another checkout than the one it deletes, then check `wt-ctl status`: the worktree's index is free
  again once its worktree is gone.

A worktree's kind clusters and its test namespace carry its prefix, as "The worktree prefix" describes.

### Cluster health

For a worktree on local kind, `create` and `status` check every kind cluster of its topology: all node containers
are running, the API server answers `kubectl get nodes` within 10 seconds and every node is `Ready`, and no pod in
`kube-system` is outside the phases `Running` and `Succeeded` or has a container waiting as `CrashLoopBackOff` or
`Error`. A stopped node cannot be started again, so an unhealthy cluster has to be recreated: `create` does that even
with `--skip-recreate`, after it logs the cluster and the reason. The state in the clusters is lost, and the
namespace and the operator are set up again. `wt-ctl status` shows `health: ok` or the first reason. Worktrees on an
Evergreen host are not checked.

## Working in a worktree

On the host, `source scripts/dev/devenv` (bash or zsh) loads the worktree's context, activates its venv and puts its
`bin/` on `PATH`; inside the container (`wt-ctl attach`, or `wt-ctl attach -- bash -lc 'cd /workspace && source
scripts/dev/devenv && <command>'`) it does the same with the container's own venv and `bin/` volume. After that
`make precommit`, `go test ./pkg/...` and `scripts/evergreen/unit-tests-python.sh` (the Python unit tests, with those of
`wt-ctl` and of the e2e code) work on both sides. `make precommit` downloads `prek` into `bin/` on first use and
stages what the hooks change. `validate-evergreen-config` fails on both sides with `field required_labels not found` when
your `evergreen` CLI is older than the project config. Run `evergreen get-update --install` on the host (the container
downloads the build of the host's CLI when it is created) or skip the hook with `SKIP=validate-evergreen-config make
precommit`, set in the shell that runs it. `golangci-lint-fmt` can fail with `lstat ....isorted: no such file` when it
scans the tree while `isort` rewrites its files: run `make precommit` again. Commits in the container use your host's
`user.name` and `user.email`, and git over ssh trusts GitHub's host key.

## Running a test

```shell
scripts/dev/wt-ctl attach -- /workspace/scripts/dev/e2e_run.sh e2e_replica_set_readiness_probe
```

`e2e_run.sh` takes a pytest marker or a path under `tests/`. The output goes to `logs/test.log` in the worktree, the
JUnit report to `logs/myreport.xml`, and the exit code is pytest's. The operator runs in the container and logs to
`logs/operator.log`. To start a marker from a clean namespace, run
`wt-ctl create --skip-recreate <branch>` (with `--local-kind` for a worktree on your laptop): it resets the test
namespace and restarts the operator, and `--skip-prepare-e2e` leaves the namespace alone.

## Evergreen hosts

Without `--local-kind`, `create` spawns an Evergreen host named after the branch directory, or reuses a live one of
that name (`--evg-host-name <name>` picks another; `--distro` and `--region` apply to a spawn). It then syncs the
checkout to `/home/ubuntu/devc<N>/mongodb-kubernetes` on the host, recreates the worktree's clusters there with
`evg_host.sh` and copies the kubeconfig back to `.generated/current.kubeconfig`. A second worktree joins the same host
with the same `--evg-host-name`: each worktree has its own directory, and its clusters carry its `devc<N>-` prefix. The
sync also copies `.generated/worktree-context` (the rest of `.generated/` stays out), so that the scripts on the host see
the same prefix.

The clusters are reached through ssh. The `evg-host-proxy` sidecar of the worktree's compose project opens a tunnel to
the host with your ssh agent, because the container has no `~/.ssh`, and `gost-proxy` turns it into an HTTP proxy.
`kubeconfig refresh` sets it as `proxy-url` on the container's kubeconfigs, and on the host one through the port
`gost-proxy` publishes on `127.0.0.1` (`MCK_DEVC_PROXY_PORT` in `.devcontainer/.env`), so host `kubectl` works while
the worktree is up. `create` records the host key in `.generated/ssh/known_hosts` and in `~/.ssh/known_hosts` (the one
`evg_host.sh` reads, which would otherwise ask about the new host and fail without a terminal), trusting it on first
use, and stops early when no key of the agent is accepted.

`.generated/.current-evg-host` and `.current-evg-host-address` mark a worktree as one with an Evergreen host. `create --skip-evg`
makes no Evergreen call and uses the host address and kubeconfig that are there; `--skip-recreate` still syncs nothing
and only copies the kubeconfig again.

`delete --evg` removes the worktree's clusters and its directory from the host. The last worktree of a host that `create`
spawned (it marks it with `~/.wt-ctl-host`) terminates the host. A host you spawned yourself and a host that does not
answer are left running, with a message. `auth refresh` puts the worktree's registry credentials on the host too, because
the nodes there mount `~/.docker/config.json`, and `auth status` shows its time left.

The container has the `evergreen` CLI of the host's build, installed when the container is created, and a copy of
`~/.evergreen.yml` and of the access token in `~/.kanopy`, without the refresh token. The token lasts as long as the
host's did when the container started. When it expires `evergreen` asks for a login: do not complete it, because that
logs you out on the host. Run `evergreen client get-oauth-token` on the host and restart the container.

## Registry credentials

The images come from ECR, and an ECR token lives 12 hours. Each worktree keeps its own copy in
`.generated/registry-auth/config.json`, which no other worktree uses. The kind nodes mount its
directory, the container gets the file as `~/.docker/config.json`, and `image-registries-secret` in the worktree's
namespaces is built from that copy. Your own `~/.docker/config.json` is read as a source of tokens and never written.

`create` fills the store before it creates the clusters (and puts it on the Evergreen host after the sync): it takes the token with the most time left from the store and
your docker config, and runs `aws ecr get-login-password` for a new one when that has under 4 hours. That needs the AWS
keys in `private-context` or an SSO `AWS_PROFILE` (`aws sso login --profile <name>` when the session ended). `attach` does
the same before it opens the container, with a warning instead of an error when it fails, `wt-ctl status` shows the
time left, and `e2e_run.sh` refuses to start with an expired token, because the first pull would fail minutes in.
Through `attach` you only see that refusal when the renewal failed.

`wt-ctl auth status` exits 0 when every copy is fine, 1 when one is expiring and 3 when one is expired or missing.
`wt-ctl auth refresh` renews the token and rolls it to the container and the pull secrets of every cluster; run it
on the host; it refuses in the container, so that the host writes the store and every copy. `--force` mints a new token even when the current one has
time left. Both verbs act on the current worktree only.

## Host DNS on a Mac

The kube-forwarding-proxy daemon is a launchd service you install from its macOS pkg. It resolves
`*.svc.cluster.local` for the kubeconfigs registered with it, so `kubectl` and a browser on the host reach services
by their cluster names. `wt-ctl` never installs, starts or stops it: when it is down, `create` and `status` warn and
carry on, and `sudo launchctl kickstart -k system/io.github.fealebenpae.k8s-proxy` starts it.

`create` and `kubeconfig refresh` send the worktree's host kubeconfig to the daemon, and the container's `post-start.sh`
sends it again on every start, because the daemon forgets registrations when it restarts. One daemon serves every
worktree, so the registered names get the worktree's proxy port as a suffix (`kind-kind-8005`); the file on disk keeps
`kind-kind`. `delete --devcontainer` removes the worktree's contexts and leaves the others. On a Linux host there is no
daemon and all of this does nothing.

## Inner loop

On the host, `KUBECONFIG=<worktree>/.generated/current.kubeconfig kubectl get pods -n devc<N>-<namespace>` and `k9s`
with the same variable work. A multi-cluster worktree has the contexts `kind-e2e-operator`, `kind-e2e-cluster-1`, `-2` and
`-3`; the MongoDB pods run in the member clusters. In the container the kubeconfig is
`.generated/current.devc.kubeconfig`: its servers are `host.docker.internal` on macOS and the kind node address on
Linux, with TLS verification off for kubectl. The apiserver certificate of a cluster that `setup_kind_cluster.sh`
creates does name `host.docker.internal` (and the node address, as kind's does), because the operator verifies it.

`kubectl` runs by hand on the host keep their discovery cache under `~/.kube/cache` unless you also set
`KUBECACHEDIR=<worktree>/.generated/cache/kube`, as `wt-ctl` does.

After you edit Go code, restart the operator with `wt-ctl attach -- /workspace/scripts/dev/op_run.sh --detach`. Add
`--stop` to stop it.

## Git in the container

The container mounts the checkout at `/workspace` and the main repository's `.git` at its host path, so git works in
a linked worktree on both sides, and you can commit and push from either. The mount is read-write and shared by every
worktree, which has consequences:

- A `git branch -D`, `git stash drop` or `git gc` in the container acts on the branches and stashes of every worktree.
- `.git/config` is shared, so a token in a remote URL is visible in the container. Credential helpers and
  `~/.git-credentials` are not mounted.
- `.git/hooks` is shared. `prek install` on the host writes hooks with host paths that fail in the container, and
  one installed in the container breaks host commits. Never run it there.
- The container sees the metadata of the other worktrees but not their directories. Never run `git worktree prune`,
  `git worktree repair`, `git worktree remove` or `git gc --prune` there: they would drop the entries of the others.
  `post-start.sh` sets `gc.worktreePruneExpire never` in the container's own git config so that an automatic gc does not.

If git in the container says "dubious ownership", the container user does not own the mounted files, which can happen
on a Linux host whose uid is not 1000. In the container, `git config --global --add safe.directory /workspace` and the
same for the main repository's path fixes it.

## Tests

The unit tests of the pure logic (worktree index, kubeconfig rewrite, registry token expiry, host data) run with
`scripts/dev/run_python.sh -m pytest scripts/dev/wt_ctl/tests` and are part of `unit_tests_python`.

Two integration tests run real kind clusters, and neither is collected by `unit_tests_python`. They need docker, `kind`,
`kubectl`, a `private-context`, about 14 GB of free memory, a git checkout (the worktrees are worktrees of `HEAD`, so
commit what you want tested) and an ECR login, or `KIND_NODE_IMAGE` set to an image you have pulled. The second also
needs the `devcontainer` CLI. Both refuse to start when clusters from an earlier run are left over, and they delete
only what they created.

- `scripts/test/kind_prefix_integration.py` checks the prefixes without `wt-ctl`: two worktrees with different prefixes
  on one docker daemon, one of them multi-cluster, with their own pools and kubeconfigs.
- `scripts/test/wt_ctl_integration.py` creates two worktrees with `wt-ctl create --local-kind`, checks their clusters,
  kubeconfigs and git in the containers, deletes one with `delete --all` and checks that the other keeps running.

```bash
scripts/dev/run_python.sh -m pytest -v scripts/test/kind_prefix_integration.py scripts/test/wt_ctl_integration.py
```

Evergreen runs them on demand, never on a commit or a PR by default. The variant `e2e_mck_devcontainer_local_kind`
starts with `evergreen patch -a devcontainer` or the PR label `evergreen:devcontainer`, and has three tasks, each on
its own host:

- `e2e_devc_worktrees` runs the `wt-ctl` unit tests and the two integration tests above.
- `e2e_devc_replica_set` and `e2e_devc_multi_cluster_replica_set` are the scenarios L1 and L2 below: each creates one
  worktree on the host with `wt-ctl create --local-kind` and runs one marker in its container, with the operator started
  by `go run` there.

All three go through `scripts/evergreen/e2e/devcontainer_local_kind_e2e.sh`. It commits the patched working tree first,
because `wt-ctl` and the tests add worktrees of `HEAD`, and it writes the Evergreen expansions into `private-context`
and `.devcontainer/compose.user.yml`, which every worktree copies from the checkout it is created in. No images are
built: the tasks use the `:latest` tags unless `OVERRIDE_VERSION_ID` pins a build.

## Acceptance scenarios

`scripts/test/wt_ctl_acceptance.py` runs real worktrees end to end. A scenario creates a worktree on the branch
`acc-<id>` with `wt-ctl create` (V1 first makes the worktree with a plain `git worktree add`, so `create` adopts it), runs one marker in its container with `e2e_run.sh`, and deletes the worktree. It also
checks, for the worktrees of one run: the prefix and load balancer pools of `.generated/worktree-context`, the
kubeconfig and the node the container reaches, that the node containers and the `/home/ubuntu/devc<N>/` directories on a
host are exactly those of its live worktrees, that deleting one worktree leaves the node containers of the others on its
host unchanged, that after all groups the node containers on the machine, the home directory and `~/.kube/config` are as
they were, and that a host the run spawned is gone within two minutes of its last worktree. It touches only worktrees it
created, refuses to start when an `acc-*` branch exists, and at the end removes its worktrees (`delete --all --force`)
and their branches, also if it fails or you interrupt it.

| ID | Where | Context | Marker | Needs | Time |
|---|---|---|---|---|---|
| L1 | local kind | `e2e_mdb_kind_ubi_cloudqa` | `e2e_replica_set_config_map` | 3 GB in the Docker VM | 8 min |
| V1 | local kind, from a bare worktree | `e2e_mdb_kind_ubi_cloudqa` | `e2e_replica_set_config_map` | 3 GB in the Docker VM | 8 min |
| L2 | local kind, 5 clusters | `e2e_multi_cluster_kind` | `e2e_multi_cluster_replica_set` | 12 GB in the VM, nothing else heavy | 20 min |
| R1 | host of its own | as L1 | as L1 | host 2 GB, laptop 1 GB | 10 min |
| R2 | host of its own | `e2e_multi_cluster_om_appdb` | `e2e_multi_cluster_appdb_cleanup` | AWS keys with S3 rights | 25 min |
| S1 | shared host | as L1 | `e2e_replica_set` | the shared host | 60 to 90 min together |
| S2 | shared host | `e2e_multi_cluster_om_appdb` | `e2e_multi_cluster_appdb_s3_based_backup_restore` | AWS keys with S3 rights | 60 to 90 min together |
| S3 | shared host | `e2e_multi_cluster_kind` | `e2e_multi_cluster_replica_set` | the shared host | 60 to 90 min together |

The scenarios of a run start together and S1 to S3 share one host, `wt-acc-shared`, so the run takes as long as its
slowest group and needs their resources at once: L1, L2 and V1 about 19 GB in the Docker VM, and three Evergreen hosts for
R1, R2 and the shared one. Two single-cluster worktrees and one multi-cluster worktree used 14 GB of the shared
host's 64; S2 adds Ops Manager 9 and has not been measured. R2 and S2 need the Ops Manager 9 image of the context
(`mongodb-enterprise-ops-manager-ubi:<CUSTOM_OM_VERSION>` in your `REGISTRY`). If S2 is too heavy or the AWS keys cannot
write to S3, `e2e_multi_cluster_backup_restore` is the fallback marker (single-cluster Ops Manager 8).
`e2e_multi_cluster_om_appdb_no_mesh` does not run in a worktree: it points the operator at the bare service name
`nginx-ext-svc-interconnected`, which only kubefwd resolves, so the operator in the container cannot reach Ops Manager.
R2 does not use `e2e_multi_cluster_appdb` (48 minutes on CI): its `test_create_om` needs about 1000 s against a
1000 s timeout, and it failed in all three runs on a host of its own (twice on that timeout).

```bash
scripts/dev/run_python.sh scripts/test/wt_ctl_acceptance.py --scenarios L1,L2,V1   # together, they share the VM
scripts/dev/run_python.sh scripts/test/wt_ctl_acceptance.py --scenarios R1,R2   # together, one host each
scripts/dev/run_python.sh scripts/test/wt_ctl_acceptance.py --scenarios S1,S2,S3   # together on one host
```

Use a terminal per line to run the lines side by side. The output is one line per scenario, `L1 PASS 7m42s` or
`S2 FAIL <first failed check>`, and the exit code is 1 if one failed. The end of a failed marker's output goes to
stderr; the worktree's `logs/` are deleted with the worktree.

Commit what you want tested first (the worktrees are worktrees of `HEAD`). The prerequisites are the ones of the
integration tests, plus Cloud QA values (`OM_HOST`, `OM_USER`, `OM_API_KEY`) in `private-context` and the
`LOCAL_OPERATOR` line above. R and S also need the `evergreen` CLI login, VPN and the ssh agent key. L1 and L2 are
Evergreen tasks. R and S are not: `create` spawns hosts as the logged-in user and reaches them through your ssh agent,
which a task host does not have. A run that was killed leaves worktrees and branches behind: `wt-ctl status`,
`wt-ctl delete --all <branch>` and `git branch -D <branch>` for each `acc-*`, and `evergreen host list --mine` for a host.

## Outside the checkout

A worktree keeps its state in its checkout and writes almost nothing to your home directory: a worktree on an Evergreen
host appends the host's key to `~/.ssh/known_hosts`, and a `kubectl` you run by hand on the host writes its own
discovery cache under `~/.kube/cache` (see "Inner loop"). Three more things reach beyond the checkout:

- Credentials are read, not written: `~/.docker/config.json` and `~/.aws` (through the `aws` CLI) for the ECR token of
  the worktree's store, `~/.evergreen.yml` and `~/.kanopy` for the Evergreen CLI, `~/.ssh` and the ssh agent socket for
  git and the Evergreen host proxy.
- The kube-forwarding-proxy daemon on macOS (a launchd service). `wt-ctl` registers kubeconfigs with it and never
  starts or stops it.
- The main repository's `.git`, mounted read-write at its own path into the container of a linked worktree
  (see above).

The container can read more of your credentials than that list: `~/.evergreen.yml` (it holds the API key) is mounted
read-only, and so is the whole of `~/.kanopy`, refresh token included, at `/host-kanopy`. `post-start.sh` copies the
access token without the refresh token for the Evergreen CLI. The docker-in-docker feature runs the container
privileged, and it holds your ssh agent. Run in it only what you would run on the host.

Container-internal state is not host state: the cache volumes `mck-devcontainer-{gomodcache,gocache,uvcache,helmcache}`
are shared by all worktrees, and `venv`, `bin` and `ssh-auth-sock` belong to one worktree and go with `delete --devcontainer`.

## Troubleshooting

`create` writes one log per step to `logs/setup_worktree/` in the new worktree (`worktree-<branch_dir>.log` in the
checkout you ran it from).

- `cannot ssh to <address> with the keys of the ssh agent`: `ssh-add` the key that Evergreen put on the host, then run
  `create` again. `create` reuses the host.
- `Pool overlaps with other one on this address space`: another docker network holds the worktree's range, usually the
  compose network of an old worktree. Delete the old worktree with `delete --devcontainer`, or remove the network with
  `docker network rm`.
- `no context in .generated/`: run `make switch context=<context>` in the checkout you run `create` from.
- Pulls fail with `403 Forbidden` or `authorization token has expired`: run `wt-ctl auth status`, then
  `wt-ctl auth refresh --force` on the host.
- A recreate of the kind clusters of a worktree leaves a running operator with a stale kubeconfig. Run
  `wt-ctl kubeconfig refresh`, then restart the operator.
- After a Docker restart or a reboot the containers and the kind nodes are stopped, and the operator is gone. The nodes
  fail the cluster health check (see "Cluster health"), so `wt-ctl create [--local-kind] --skip-recreate <branch>`
  recreates the clusters of a local worktree and sets the namespace and the operator up again. A worktree on an
  Evergreen host needs `wt-ctl create <branch>`, which recreates the clusters on the host.
- `make switch` prints `no context exists with the name: "kind-kind"` before the first cluster exists, and the
  devcontainer step prints `[httpOci] 403: Credentials for 'ghcr.io' may be expired`. Both are harmless.
- `create` stopped at `onCreateCommand ... failed` (no venv, for example): the devcontainer CLI runs `on-create.sh` once
  per container, so running `create` again skips it. Fix the cause, then run
  `wt-ctl attach -- bash -lc 'cd /workspace && bash .devcontainer/scripts/on-create.sh'`.
- To stop a `create` that waits on a container command, press Ctrl-C and run it again. The command in the container,
  for example `make prepare-local-e2e`, keeps running; stop it with `wt-ctl attach -- bash -lc 'pkill -f make'` first.
