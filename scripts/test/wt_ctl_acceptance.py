"""Acceptance scenarios of wt-ctl: create worktrees, run one e2e marker in each, check that the worktrees are
isolated, delete them. Run with `--scenarios L1,S2`; the groups of scenarios run together. The guide, "Acceptance
scenarios", has the prerequisites and run times."""

import argparse
import json
import re
import shlex
import sys
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone

import wt_ctl_integration as it
from wt_ctl_integration import REPO, run, wt_ctl

sys.path.insert(0, str(REPO / "scripts" / "dev"))
from wt_ctl.domains.devcontainer import DevcontainerDomain  # noqa: E402
from wt_ctl.domains.evg import DEAD, EvgDomain, host_address, ssh_argv  # noqa: E402
from wt_ctl.domains.network import NetworkDomain, worktree_prefix  # noqa: E402
from wt_ctl.paths import shared_state_dir  # noqa: E402

HOST_HOURS = 48  # H1 spawns its first host for this long, and the replacement for the default
DEFAULT_HOURS = 24
GRAPHQL = "https://evergreen.corp.mongodb.com/graphql/query"

evg = EvgDomain(it.Runner())
PREFIXES = set()  # the devc<N>- prefixes of this run's worktrees, so that two runs can share a machine
PATHS = set()  # the paths of this run's worktrees

# id: (where, context, marker). where is "local" or the name of an Evergreen host, which scenarios may share.
# H1 has no marker: it ends its host and creates again.
SCENARIOS = {
    "L1": ("local", "e2e_mdb_kind_ubi_cloudqa", "e2e_replica_set_config_map"),
    "V1": ("local", "e2e_mdb_kind_ubi_cloudqa", "e2e_replica_set_config_map"),  # starts from a bare `git worktree add`
    "L2": ("local", "e2e_multi_cluster_kind", "e2e_multi_cluster_replica_set"),
    "R1": ("acc-r1", "e2e_mdb_kind_ubi_cloudqa", "e2e_replica_set_config_map"),
    "R2": ("acc-r2", "e2e_multi_cluster_om_appdb", "e2e_multi_cluster_appdb_cleanup"),
    "H1": ("acc-h1", "e2e_mdb_kind_ubi_cloudqa", ""),
    "S1": ("wt-acc-shared", "e2e_mdb_kind_ubi_cloudqa", "e2e_replica_set"),
    "S2": ("wt-acc-shared", "e2e_multi_cluster_om_appdb", "e2e_multi_cluster_appdb_s3_based_backup_restore"),
    "S3": ("wt-acc-shared", "e2e_multi_cluster_kind", "e2e_multi_cluster_replica_set"),
}


class Worktree:
    def __init__(self, id):
        self.id, (self.where, self.context, self.marker) = id, SCENARIOS[id]
        self.branch, self.remote = f"acc-{id.lower()}", self.where != "local"
        self.path = REPO.parent / self.branch

    prefix = property(lambda self: it.prefix(self.path))  # devc<N>-, readable once the worktree exists

    def kubectl(self, *args):
        return run("kubectl", "--kubeconfig", self.path / ".generated/current.kubeconfig", *args)

    def nodes(self):
        contexts = self.kubectl("config", "get-contexts", "-o", "name").split()
        return {f"{self.prefix}{c.removeprefix('kind-')}-control-plane" for c in contexts}

    def on_host(self, command):
        return run(*ssh_argv(self.path, host_address(self.path), command))


def containers(s=None):
    """Node container name to id, on the host of the remote worktree `s`, else on this machine."""
    cmd = ["docker", "ps", "--filter", "name=^devc[0-9]+-", "--format", "{{.Names}} {{.ID}}"]
    out = s.on_host(shlex.join(cmd)) if s and s.remote else run(*cmd)
    return dict(line.split() for line in out.splitlines())


def check_worktree(s):
    """Checks 1, 3 and 5: the worktree-context file's pools, own clusters only, the container reaches them."""
    remember(s)
    row = 128 + int(s.prefix[4:-1])
    pools = re.findall(rf'="172\.18\.{row}\.\d+-172\.18\.{row}\.\d+"', it.worktree_context(s.path))
    assert len(pools) == 6, f"{s.id}: {len(pools)} pools in row {row}, not 6"
    assert len(s.nodes()) == (5 if s.context.startswith("e2e_multi") else 1), f"{s.id}: {sorted(s.nodes())}"
    want = f"node/{s.prefix}{s.kubectl('config', 'current-context').removeprefix('kind-')}-control-plane"
    assert it.in_container(s.path, "kubectl get nodes -o name") == want, f"{s.id}: the container does not reach {want}"


def check_host(live):
    """Checks 8 and 9: the node containers and the directories on the host are those of the live worktrees."""
    assert set(containers(live[0])) == set().union(*(s.nodes() for s in live)), "node containers on the host"
    dirs = set(live[0].on_host("ls -d /home/ubuntu/devc*/").split())
    assert dirs == {f"/home/ubuntu/{s.prefix[:-1]}/" for s in live}, f"directories on the host: {dirs}"


def remember(s):
    """Note the path and, once its worktree has one, the prefix of `s`. Another run's worktrees carry other prefixes."""
    PATHS.add(s.path.resolve())
    try:
        PREFIXES.add(s.prefix)
    except (OSError, AttributeError):
        pass


def leak(home):
    """Check 4 and the home check, once all groups are done: no node container of this run is left, and the home
    directory and ~/.kube/config are as they were."""
    # The lowest free index goes to the next create, so a worktree of another run may now hold a prefix that a worktree
    # of this run held. Its nodes are not a leak. A worktree of this run that is still there does not excuse its nodes.
    pins = NetworkDomain(it.Runner(), REPO, lock_dir=shared_state_dir(REPO)).pins()
    taken = {worktree_prefix(pin.index) for path, pin in pins.items() if path not in PATHS}
    left = sorted(n for n in containers() if any(n.startswith(p) for p in PREFIXES - taken))
    if left:
        return f"node containers of this run are left: {left}"
    if it.home_state() != home:
        return "the home directory or ~/.kube/config changed"


def report(who, message):
    sys.stderr.write(f"{who}: {message}\n")  # one write, so that the lines of parallel groups do not mix


def delete(s, live):
    """Check 7: the worktree goes, the node containers of the other worktrees on its host keep their ids."""
    remember(s)
    others, gone = [p for p in live if p is not s], s.nodes()
    seen = containers(others[0]) if others else {}
    wt_ctl(REPO, "delete", "--all", s.branch)
    assert not s.path.exists() and it.project_containers(s.path, "-a") == "", f"{s.id}: worktree or containers are left"
    live.remove(s)
    if others:
        assert containers(others[0]) == {n: i for n, i in seen.items() if n not in gone}


def delete_together(batch, live):
    """The worktrees of `batch` go at the same time, as the last ones of a host do, so their leaves race to terminate
    it. Check 7 compares the containers of the worktrees that stay, and none stays, so it is left to `delete`."""
    for s in batch:
        remember(s)
    with ThreadPoolExecutor(len(batch)) as pool:
        deletes = [pool.submit(wt_ctl, REPO, "delete", "--all", s.branch) for s in batch]
    for d in deletes:
        d.result()  # the with block has waited for all of them; this raises the first failure
    for s in batch:
        assert (
            not s.path.exists() and it.project_containers(s.path, "-a") == ""
        ), f"{s.id}: worktree or containers are left"
        live.remove(s)


def create(s, *extra):
    where = ["--evg-host-name", s.where] if s.remote else ["--local-kind"]
    wt_ctl(REPO, "create", *where, *extra, "--context", s.context, s.branch)


def host_field(host_id, fields):
    """The fields of the host that the CLI does not print (expiration, tags), from the GraphQL API."""
    query = json.dumps({"query": f'{{ host(hostId: "{host_id}") {{ {fields} }} }}'}).encode()
    headers = {
        "Authorization": f"Bearer {evg.evergreen('client', 'get-oauth-token').strip()}",
        "Content-Type": "application/json",
    }
    with urllib.request.urlopen(urllib.request.Request(GRAPHQL, query, headers), timeout=30) as response:
        return json.load(response)["data"]["host"]


def hours_left(host_id):
    """Hours until Evergreen expires the host. The CLI does not print it, so this asks the GraphQL API."""
    expiry = datetime.fromisoformat(host_field(host_id, "expiration")["expiration"].replace("Z", "+00:00"))
    return (expiry - datetime.now(timezone.utc)).total_seconds() / 3600


def check_tagged(s, host):
    """The host that this run's create spawned carries the `wt-ctl` tag, with its name as the value: `delete` goes by it."""
    tags = {t["key"]: t["value"] for t in host_field(host["id"], "instanceTags { key value }")["instanceTags"]}
    assert tags.get("wt-ctl") == s.where, f"{s.id}: host {host['id']} has the tags {tags}, not wt-ctl={s.where}"


def check_expiry(s, hours):
    """The live host of `s` expires in about `hours` hours; returns the host and the hours left."""
    host = evg.find(s.where)
    left = hours_left(host["id"])
    assert hours - 2 < left <= hours, f"{s.id}: the host expires in {left:.1f} h, not about {hours}"
    return host, left


def sidecar(s):
    """The evg-host-proxy container of the worktree and when it last started."""
    cid = DevcontainerDomain(it.Runner(), s.path).container_id("evg-host-proxy")
    return cid, run("docker", "inspect", "-f", "{{.State.StartedAt}}", cid)


def check_host_lifecycle(s):
    """Check 11: --host-hours extends the host; a create with the same address leaves the sidecar running; once the
    host is terminated, status says GONE and exits 0, and create ends on a new host of the default lifetime with a
    tunnel that points at it, without a manual step. Both hosts that wt-ctl spawned carry its `wt-ctl` tag."""
    host, left = check_expiry(s, HOST_HOURS)
    check_tagged(s, host)
    old_address, (cid, started) = host_address(s.path), sidecar(s)
    create(s, "--skip-recreate")
    assert sidecar(s) == (cid, started), f"{s.id}: create restarted the sidecar of a live host"
    evg.terminate(host["id"])
    check_host_ended(s.where)
    line = next(line for line in wt_ctl(REPO, "status").splitlines() if str(s.path) in line)
    assert "(GONE)" in line, f"{s.id}: status does not report the host as gone: {line}"
    create(s)
    new_address, (new_cid, _) = host_address(s.path), sidecar(s)
    assert new_address != old_address, f"{s.id}: create reused the terminated host"
    assert new_address in run("docker", "top", new_cid), f"{s.id}: the tunnel does not point at the new host"
    new_host, new_left = check_expiry(s, DEFAULT_HOURS)
    check_tagged(s, new_host)
    check_worktree(s)
    shown = re.search(r"evergreen host: .*?\)", line).group()
    report(
        s.id,
        f"expiry {left:.1f} h; same-address rerun kept the sidecar ({started}); after terminate: {shown}; "
        f"replacement expiry {new_left:.1f} h, tunnel to {new_address}",
    )


def check_host_ended(host):
    """Check 10: the host of the last worktree is terminated within two minutes."""
    for _ in range(12):
        if not evg.find(host):
            return
        time.sleep(10)
    raise AssertionError(f"host {host} still runs after its last worktree was deleted")


def end_hosts(name):
    """Terminate by id every live host called `name`, whatever `delete` did, then check that none is left. main
    refused a host that existed before the run, so these are the run's own. The Evergreen CLI can fail for a minute
    (network, token file), so the whole step is tried three times. A failure is reported and does not hide the run's
    own failure."""
    for attempt in range(3):
        try:
            for host in [
                h for h in evg.hosts() if h.get("name") == name and (h.get("status") or "").lower() not in DEAD
            ]:
                report(name, f"terminating leftover host {host['id']}")
                evg.terminate(host["id"])
            for _ in range(3):  # Evergreen may take a moment to list a terminated host as terminated
                left = evg.find(name)
                if left is None:
                    return
                time.sleep(10)
            report(
                name, f"host {left['id']} is still live after the cleanup: evergreen host terminate --host {left['id']}"
            )
            return
        except Exception as e:
            if attempt == 2:
                report(name, f"cleanup failed, run `evergreen host list --mine` and terminate the host {name}: {e}")
            else:
                time.sleep(20)


def run_group(group):
    """Run a group together; a failed check ends it, a failed marker only its scenario. id -> (failure, seconds)."""
    start, live, failed, problem = time.monotonic(), [], {}, None
    try:
        for s in group:
            if s.id == "V1":  # nothing copied: no private-context, venv, .generated or hooks, as after a clone
                run("git", "-C", REPO, "worktree", "add", "-b", s.branch, s.path)
            live.append(s)  # before its create, so that the cleanup covers a create that fails halfway
        with ThreadPoolExecutor(len(group)) as pool:  # the worktrees of one host join it together
            creates = [
                pool.submit(create, s, *(["--host-hours", str(HOST_HOURS)] if s.id == "H1" else [])) for s in group
            ]
        for c in creates:
            c.result()  # the with block has waited for all of them; this raises the first failure
        if group[0].remote:
            live_hosts = [
                h
                for h in evg.hosts()
                if h.get("name") == group[0].where and (h.get("status") or "").lower() not in DEAD
            ]
            assert len(live_hosts) == 1, f"{len(live_hosts)} live hosts named {group[0].where}, not 1"
        for s in group:
            check_worktree(s)
        with ThreadPoolExecutor() as pool:  # the markers of a shared host run together
            runs = {
                s: pool.submit(it.in_container, s.path, f"scripts/dev/e2e_run.sh {s.marker}") for s in group if s.marker
            }
        for s, r in runs.items():
            if r.exception():
                report(s.id, r.exception())
                failed[s.id] = f"marker {s.marker} failed, output above"
        for s in group:
            if s.id == "H1":
                check_host_lifecycle(s)
        if group[0].remote:
            check_host(live)
        # The first worktree of a shared host goes alone, with check 7. The others go together: their leaves race.
        alone = 1 if group[0].remote and len(group) > 2 else len(group)
        for s in group[:alone]:
            delete(s, live)
        if group[alone:]:
            delete_together(group[alone:], live)
        if group[0].remote:
            check_host_ended(group[0].where)
    except Exception as e:
        report("+".join(s.id for s in group), e)
        problem = str(e).splitlines()[0]
    finally:
        for s in live:
            remember(s)
            wt_ctl(REPO, "delete", "--all", "--force", s.branch, check=False)
        if group[0].remote:  # delete leaves a host it cannot reach (ssh refused, say), and the run paid for it
            end_hosts(group[0].where)
        for s in group:  # delete never removes a branch, and a leftover acc-* branch stops the next run
            run("git", "-C", REPO, "branch", "-D", s.branch, check=False)
    return {s.id: (failed.get(s.id) or problem, time.monotonic() - start) for s in group}


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--scenarios", required=True, help=f"comma separated, from {','.join(SCENARIOS)}")
    ids = list(dict.fromkeys(parser.parse_args().scenarios.split(",")))
    if not set(ids) <= SCENARIOS.keys():
        parser.error(f"unknown scenario, valid ones are {','.join(SCENARIOS)}")
    taken = [f"acc-{i.lower()}" for i in ids if run("git", "-C", REPO, "branch", "--list", f"acc-{i.lower()}")]
    if taken:
        sys.exit(
            f"{', '.join(taken)} exists from an earlier or a running run: delete its worktree and the branch first"
        )
    for name in dict.fromkeys(Worktree(i).where for i in ids if Worktree(i).remote):
        if evg.find(name):
            sys.exit(
                f"a live Evergreen host named {name} exists: terminate it first, the run ends every host of that name"
            )
    private = REPO / "scripts/dev/contexts/private-context"
    if not private.is_file() or not re.search(r"^[^#\n]*LOCAL_OPERATOR=true", private.read_text(), re.M):
        sys.exit("private-context must set LOCAL_OPERATOR=true in the container (see the guide)")
    groups = {}  # the scenarios of one host form a group, each local one is a group
    for i in ids:
        s = Worktree(i)
        groups.setdefault(s.where if s.remote else i, []).append(s)
    home = it.home_state()
    with ThreadPoolExecutor(len(groups)) as pool:  # on Ctrl-C the with waits for the finally of every group
        results = {i: r for done in pool.map(run_group, groups.values()) for i, r in done.items()}
    leaked = leak(home)
    results = {i: (error or leaked, seconds) for i, (error, seconds) in results.items()}
    for i, (error, seconds) in results.items():
        print(f"{i} {'FAIL ' + error if error else 'PASS'} {int(seconds // 60)}m{int(seconds % 60):02d}s")
    sys.exit(any(error for error, _ in results.values()))


if __name__ == "__main__":
    main()
