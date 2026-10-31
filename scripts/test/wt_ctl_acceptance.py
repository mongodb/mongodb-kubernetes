"""Acceptance scenarios of wt-ctl: create worktrees, run one e2e marker in each, check that the worktrees are
isolated, delete them. Run with `--scenarios L1,S2`; the groups of scenarios run together. The guide, "Acceptance
scenarios", has the prerequisites and run times."""

import argparse
import re
import shlex
import sys
import time
from concurrent.futures import ThreadPoolExecutor

import wt_ctl_integration as it
from wt_ctl_integration import REPO, run, wt_ctl

sys.path.insert(0, str(REPO / "scripts" / "dev"))
from wt_ctl.domains.evg import host_address, parse_hosts, pick_host, ssh_argv  # noqa: E402

# id: (where, context, marker). where is "local" or the name of an Evergreen host, which scenarios may share.
SCENARIOS = {
    "L1": ("local", "e2e_mdb_kind_ubi_cloudqa", "e2e_replica_set_config_map"),
    "V1": ("local", "e2e_mdb_kind_ubi_cloudqa", "e2e_replica_set_config_map"),  # starts from a bare `git worktree add`
    "L2": ("local", "e2e_multi_cluster_kind", "e2e_multi_cluster_replica_set"),
    "R1": ("acc-r1", "e2e_mdb_kind_ubi_cloudqa", "e2e_replica_set_config_map"),
    "R2": ("acc-r2", "e2e_multi_cluster_om_appdb", "e2e_multi_cluster_appdb_cleanup"),
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


def leak(before, home):
    """Check 4 and the home check, once all groups are done: this machine is as it was before the run."""
    now = containers()
    if now != before:
        return f"node containers on this machine differ from the start: {sorted(now.items() ^ before.items())}"
    if it.home_state() != home:
        return "the home directory or ~/.kube/config changed"


def report(who, message):
    sys.stderr.write(f"{who}: {message}\n")  # one write, so that the lines of parallel groups do not mix


def delete(s, live):
    """Check 7: the worktree goes, the node containers of the other worktrees on its host keep their ids."""
    others, gone = [p for p in live if p is not s], s.nodes()
    seen = containers(others[0]) if others else {}
    wt_ctl(REPO, "delete", "--all", s.branch)
    live.remove(s)
    assert not s.path.exists() and it.project_containers(s.path, "-a") == "", f"{s.id}: worktree or containers are left"
    if others:
        assert containers(others[0]) == {n: i for n, i in seen.items() if n not in gone}


def check_host_ended(host):
    """Check 10: the host of the last worktree is terminated within two minutes."""
    for _ in range(12):
        if not pick_host(parse_hosts(run("evergreen", "host", "list", "--mine", "--json")), host):
            return
        time.sleep(10)
    raise AssertionError(f"host {host} still runs after its last worktree was deleted")


def run_group(group):
    """Run a group together; a failed check ends it, a failed marker only its scenario. id -> (failure, seconds)."""
    start, live, failed, problem = time.monotonic(), [], {}, None
    try:
        for s in group:
            if s.id == "V1":  # nothing copied: no private-context, venv, .generated or hooks, as after a clone
                run("git", "-C", REPO, "worktree", "add", "-b", s.branch, s.path)
            live.append(s)
            where = ["--evg-host-name", s.where] if s.remote else ["--local-kind"]
            wt_ctl(REPO, "create", *where, "--context", s.context, s.branch)
        for s in group:
            check_worktree(s)
        with ThreadPoolExecutor() as pool:  # the markers of a shared host run together
            runs = {s: pool.submit(it.in_container, s.path, f"scripts/dev/e2e_run.sh {s.marker}") for s in group}
        for s, r in runs.items():
            if r.exception():
                report(s.id, r.exception())
                failed[s.id] = f"marker {s.marker} failed, output above"
        if group[0].remote:
            check_host(live)
        for s in group:
            delete(s, live)
        if group[0].remote:
            check_host_ended(group[0].where)
    except Exception as e:
        report("+".join(s.id for s in group), e)
        problem = str(e).splitlines()[0]
    finally:
        for s in live:
            wt_ctl(REPO, "delete", "--all", "--force", s.branch, check=False)
        for s in group:  # delete never removes a branch, and a leftover acc-* branch stops the next run
            run("git", "-C", REPO, "branch", "-D", s.branch, check=False)
    return {s.id: (failed.get(s.id) or problem, time.monotonic() - start) for s in group}


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--scenarios", required=True, help=f"comma separated, from {','.join(SCENARIOS)}")
    ids = list(dict.fromkeys(parser.parse_args().scenarios.split(",")))
    if not set(ids) <= SCENARIOS.keys():
        parser.error(f"unknown scenario, valid ones are {','.join(SCENARIOS)}")
    if run("git", "-C", REPO, "branch", "--list", "acc-*"):
        sys.exit("a branch acc-* exists from an earlier run: delete its worktree and the branch first")
    private = REPO / "scripts/dev/contexts/private-context"
    if not private.is_file() or not re.search(r"^[^#\n]*LOCAL_OPERATOR=true", private.read_text(), re.M):
        sys.exit("private-context must set LOCAL_OPERATOR=true in the container (see the guide)")
    groups = {}  # the scenarios of one host form a group, each local one is a group
    for i in ids:
        s = Worktree(i)
        groups.setdefault(s.where if s.remote else i, []).append(s)
    before, home = containers(), it.home_state()
    with ThreadPoolExecutor(len(groups)) as pool:  # on Ctrl-C the with waits for the finally of every group
        results = {i: r for done in pool.map(run_group, groups.values()) for i, r in done.items()}
    leaked = leak(before, home)
    results = {i: (error or leaked, seconds) for i, (error, seconds) in results.items()}
    for i, (error, seconds) in results.items():
        print(f"{i} {'FAIL ' + error if error else 'PASS'} {int(seconds // 60)}m{int(seconds % 60):02d}s")
    sys.exit(any(error for error, _ in results.values()))


if __name__ == "__main__":
    main()
