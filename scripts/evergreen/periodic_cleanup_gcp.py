"""Age-based garbage collector for the GKE code-snippet test project (KUBE-268).

Deletes every resource older than AGE_THRESHOLD_HOURS, except the auditable
DENYLISTS below. Age is the only ownership filter: the project is used
exclusively by ephemeral e2e tests, and an in-flight run's resources are
younger than the threshold (deleting them is what broke the KUBE-268 run).
Clusters go first, in parallel; delete failures are tolerated and retried on
the next run; deletes are batched by scope with a per-name fallback; malformed
inventories abort their class instead of deleting from a partial view.

Run through the project venv (created by the periodic_teardown setup):
  scripts/dev/run_python.sh scripts/evergreen/periodic_cleanup_gcp.py

Env: MDB_GKE_PROJECT (required), AGE_THRESHOLD_HOURS (24), DRY_RUN (false),
BATCH_SIZE (50).
"""

import os
import re
import shlex
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from itertools import groupby
from typing import Callable, Optional, Sequence

COMPUTE_LIST_FORMAT = "value(name,region,zone,creationTimestamp.date('%s'))"
CLUSTER_LIST_FORMAT = "value(name,location,createTime.date('%s'))"
FIREWALL_LIST_FORMAT = "value(name,creationTimestamp.date('%s'))"
DNS_ZONES_LIST_FORMAT = "value(name,creationTime.date('%s'))"
NOT_FOUND_ERROR = re.compile(r"not found|NOT_FOUND|notFound|does not exist|not present", re.IGNORECASE)

# The reaper never deletes a name matching one of these regexes. Everything not
# listed is deleted once older than the threshold, so keep the lists small and
# auditable.
DENYLISTS: dict[str, tuple[str, ...]] = {
    "firewall-rules": (r"^default-",),  # shared VPC networking rules
    "service-accounts": (
        r"^k8s-operator-e2e-tests@",  # CI infrastructure account
        r".*@developer\.gserviceaccount\.com$",  # GCE default compute account
    ),
}

Executor = Callable[[Sequence[str]], "subprocess.CompletedProcess[str]"]


class GcloudError(RuntimeError):
    """A gcloud invocation failed in a way the caller cannot ignore."""


class InventoryError(ValueError):
    """A gcloud list output does not match the expected shape."""


def execute(cmd: Sequence[str]) -> "subprocess.CompletedProcess[str]":
    return subprocess.run(list(cmd), capture_output=True, text=True)


def parse_epoch(value: str) -> Optional[float]:
    try:
        return float(value)
    except ValueError:
        return None


def parse_inventory(output: str, columns: int, timestamp_index: int) -> list[list[str]]:
    """Parse a gcloud ``value()`` TSV output, failing closed on any odd row.

    Python's split() keeps empty fields, so no placeholder normalization is
    needed for global resources (unlike the previous bash implementation).
    """
    rows = []
    for line in output.splitlines():
        row = line.split("\t")
        if len(row) != columns or parse_epoch(row[timestamp_index]) is None:
            raise InventoryError(f"unexpected row {line!r}")
        rows.append(row)
    return rows


def scope_of(region: str, zone: str) -> str:
    """Return the deletion scope of a compute row: zone:<name>, region:<name> or global."""
    if zone:
        return "zone:" + zone.rsplit("/", 1)[-1]
    if region:
        return "region:" + region.rsplit("/", 1)[-1]
    return "global"


def scope_args(scope: str, global_flag: bool) -> list[str]:
    if scope.startswith("zone:"):
        return [f"--zone={scope.removeprefix('zone:')}"]
    if scope.startswith("region:"):
        return [f"--region={scope.removeprefix('region:')}"]
    return ["--global"] if global_flag else []


def is_denylisted(resource: str, name: str) -> bool:
    return any(re.search(pattern, name) for pattern in DENYLISTS.get(resource, ()))


def positive_int_env(name: str, default: int) -> int:
    raw = os.environ.get(name, str(default))
    if not raw.isdigit() or int(raw) < 1:
        raise ValueError(f"{name} must be a positive integer, got {raw!r}")
    return int(raw)


def bool_env(name: str, default: bool) -> bool:
    raw = os.environ.get(name, str(default)).lower()
    if raw not in ("true", "false"):
        raise ValueError(f"{name} must be 'true' or 'false', got {raw!r}")
    return raw == "true"


@dataclass
class Summary:
    """Per-class counters printed as one summary line after each class."""

    resource: str
    listed: int = 0
    old: int = 0
    deleted: int = 0
    failed: int = 0
    skipped: int = 0

    def footer(self, threshold_hours: int, dry_run: bool) -> str:
        action = "would-delete" if dry_run else "deleted"
        return (
            f"summary: {self.listed} listed, {self.old} older than {threshold_hours}h, "
            f"{self.deleted} {action}, {self.failed} failed"
        )


class Reaper:
    def __init__(
        self,
        project: str,
        age_threshold_hours: int,
        dry_run: bool,
        batch_size: int = 50,
        executor: Executor = execute,
    ):
        self.project = project
        self.age_threshold_hours = age_threshold_hours
        self.dry_run = dry_run
        self.batch_size = batch_size
        self.__execute = executor
        self.cutoff = time.time() - age_threshold_hours * 3600
        self.failed = False

    def _gcloud(self, *args: str) -> str:
        cmd = ["gcloud", *args, f"--project={self.project}"]
        proc = self.__execute(cmd)
        if proc.returncode != 0:
            raise GcloudError(proc.stderr.strip() or f"exit code {proc.returncode}")
        return proc.stdout.strip()

    def _delete(self, *args: str) -> bool:
        """Run one mutating command; True on success or when it is already gone."""
        cmd = ["gcloud", *args, f"--project={self.project}"]
        if self.dry_run:
            print(f"  dry-run: {shlex.join(cmd)}")
            return True
        print(f"  running: {shlex.join(cmd)}")
        proc = self.__execute(cmd)
        if proc.returncode == 0 or NOT_FOUND_ERROR.search(proc.stderr):
            return True
        print(f"  stderr: {proc.stderr.strip()}")
        return False

    def _start(self, summary: Summary) -> None:
        print(f"\n=== {summary.resource} (threshold: {self.age_threshold_hours}h) ===")

    def _finish(self, summary: Summary) -> None:
        print(summary.footer(self.age_threshold_hours, self.dry_run))

    def _inventory(self, summary: Summary, *args: str, columns: int, timestamp_index: int) -> Optional[list[list[str]]]:
        try:
            output = self._gcloud(*args)
        except GcloudError as exc:
            print(f"ERROR: failed to list {summary.resource}: {exc}")
            self.failed = True
            return None
        try:
            return parse_inventory(output, columns=columns, timestamp_index=timestamp_index)
        except InventoryError as exc:
            print(f"ERROR: malformed {summary.resource} inventory: {exc}; no deletions for this class")
            self.failed = True
            return None

    def _stale(self, rows: list[list[str]], timestamp_index: int) -> list[list[str]]:
        return [row for row in rows if float(row[timestamp_index]) <= self.cutoff]

    def _delete_one(self, resource: str, name: str, args: Sequence[str], summary: Summary) -> bool:
        if self._delete("compute", resource, "delete", name, *args, "-q"):
            summary.deleted += 1
            return True
        summary.failed += 1
        self.failed = True
        return False

    def _delete_batch(self, resource: str, args: Sequence[str], names: Sequence[str], summary: Summary) -> None:
        for start in range(0, len(names), self.batch_size):
            chunk = names[start : start + self.batch_size]
            if self._delete("compute", resource, "delete", *chunk, *args, "-q"):
                summary.deleted += len(chunk)
                continue
            print(f"  batch delete of {len(chunk)} {resource} failed; retrying individually")
            for name in chunk:
                self._delete_one(resource, name, args, summary)

    def reap_clusters(self) -> None:
        summary = Summary("clusters")
        self._start(summary)
        rows = self._inventory(
            summary, "container", "clusters", "list", f"--format={CLUSTER_LIST_FORMAT}", columns=3, timestamp_index=2
        )
        if rows is not None:
            summary.listed = len(rows)
            stale = [(name, location) for name, location, timestamp in self._stale(rows, 2)]
            summary.old = len(stale)
            # Cluster deletes take minutes each; run them in parallel so a large
            # backlog cannot push the task past its timeout before the dependent
            # load-balancer resources are reached.
            with ThreadPoolExecutor() as pool:
                futures = {
                    pool.submit(
                        self._delete, "container", "clusters", "delete", name, f"--location={location}", "-q"
                    ): name
                    for name, location in stale
                }
                for future in as_completed(futures):
                    if future.result():
                        summary.deleted += 1
                    else:
                        summary.failed += 1
                        self.failed = True
        self._finish(summary)

    def reap_compute(self, resource: str, *, global_flag: bool = False, single_name: bool = False) -> None:
        summary = Summary(resource)
        self._start(summary)
        rows = self._inventory(
            summary, "compute", resource, "list", f"--format={COMPUTE_LIST_FORMAT}", columns=4, timestamp_index=3
        )
        if rows is not None:
            summary.listed = len(rows)
            stale = self._stale(rows, 3)
            summary.old = len(stale)

            def scope_key(row: list[str]) -> str:
                return scope_of(row[1], row[2])

            for scope, group in groupby(sorted(stale, key=scope_key), key=scope_key):
                names = [row[0] for row in group]
                args = scope_args(scope, global_flag)
                if single_name:
                    # gcloud compute network-endpoint-groups delete accepts one name per call.
                    for name in names:
                        self._delete_one(resource, name, args, summary)
                else:
                    self._delete_batch(resource, args, names, summary)
        self._finish(summary)

    def reap_firewall_rules(self) -> None:
        summary = Summary("firewall-rules")
        self._start(summary)
        rows = self._inventory(
            summary,
            "compute",
            "firewall-rules",
            "list",
            f"--format={FIREWALL_LIST_FORMAT}",
            columns=2,
            timestamp_index=1,
        )
        if rows is not None:
            summary.listed = len(rows)
            # summary.old includes denylisted rules; skipped reports the difference.
            stale = [name for name, timestamp in self._stale(rows, 1)]
            summary.old = len(stale)
            names = [name for name in stale if not is_denylisted("firewall-rules", name)]
            summary.skipped = len(stale) - len(names)
            if summary.skipped:
                print(f"  skipped {summary.skipped} denylisted rule(s)")
            if names:
                self._delete_batch("firewall-rules", [], names, summary)
        self._finish(summary)

    def reap_dns_zones(self) -> None:
        summary = Summary("dns-managed-zones")
        self._start(summary)
        rows = self._inventory(
            summary, "dns", "managed-zones", "list", f"--format={DNS_ZONES_LIST_FORMAT}", columns=2, timestamp_index=1
        )
        if rows is not None:
            summary.listed = len(rows)
            for zone, timestamp in self._stale(rows, 1):
                summary.old += 1
                if self._delete_zone(zone):
                    summary.deleted += 1
                else:
                    summary.failed += 1
                    self.failed = True
        self._finish(summary)

    def _delete_zone(self, zone: str) -> bool:
        """Delete a managed zone and its records. Record deletes are not counted separately."""
        try:
            output = self._gcloud("dns", "record-sets", "list", f"--zone={zone}", "--format=value(name,type)")
        except GcloudError as exc:
            print(f"ERROR: failed to list records of {zone}: {exc}")
            return False
        records = []
        for line in output.splitlines():
            name, separator, record_type = line.partition("\t")
            if not separator or not name or not record_type:
                print(f"ERROR: malformed record list for {zone}: {line!r}")
                return False
            if record_type not in ("NS", "SOA"):  # system-managed records cannot be deleted
                records.append((name, record_type))
        failed = False
        for name, record_type in records:
            failed |= not self._delete(
                "dns", "record-sets", "delete", name, f"--zone={zone}", f"--type={record_type}", "-q"
            )
        if failed:
            return False
        return self._delete("dns", "managed-zones", "delete", zone, "-q")

    def reap_service_accounts(self) -> None:
        summary = Summary("service-accounts")
        self._start(summary)
        try:
            output = self._gcloud("iam", "service-accounts", "list", "--format=value(email)")
        except GcloudError as exc:
            print(f"ERROR: failed to list service-accounts: {exc}")
            self.failed = True
            self._finish(summary)
            return
        emails = [line for line in output.splitlines() if line.strip()]
        summary.listed = len(emails)
        for email in emails:
            if is_denylisted("service-accounts", email):
                summary.skipped += 1
                continue
            # Service accounts have no creation timestamp: age is established by
            # requiring at least one user-managed key and all keys being old.
            # Known gap: an account without user-managed keys has no age signal
            # and is skipped (an interrupted ra-09 run can leave one behind, but
            # deleting it blindly could hit a test mid-setup).
            keys = self._user_managed_keys(email)
            if keys is None or not keys or any(key > self.cutoff for key in keys):
                continue
            summary.old += 1
            if not self._delete(
                "projects",
                "remove-iam-policy-binding",
                self.project,
                f"--member=serviceAccount:{email}",
                "--role=roles/dns.admin",
                "-q",
            ):
                summary.failed += 1
                self.failed = True
                continue
            if self._delete("iam", "service-accounts", "delete", email, "-q"):
                summary.deleted += 1
            else:
                summary.failed += 1
                self.failed = True
        if summary.skipped:
            print(f"  skipped {summary.skipped} denylisted account(s)")
        self._finish(summary)

    def _user_managed_keys(self, email: str) -> Optional[list[float]]:
        """Return the validAfterTime epochs of the account's user-managed keys, or None on error."""
        try:
            output = self._gcloud(
                "iam",
                "service-accounts",
                "keys",
                "list",
                f"--iam-account={email}",
                "--managed-by=user",
                "--format=value(validAfterTime.date('%s'))",
            )
        except GcloudError as exc:
            print(f"ERROR: failed to list keys for {email}: {exc}")
            return None
        keys = []
        for line in output.splitlines():
            timestamp = parse_epoch(line) if line.strip() else None
            if timestamp is None:
                print(f"ERROR: malformed key timestamp for {email}: {line!r}")
                return None
            keys.append(timestamp)
        return keys

    def run(self) -> None:
        if self.dry_run:
            print("=== DRY RUN: no delete or IAM mutation commands will execute ===")
        self.reap_clusters()
        self.reap_compute("forwarding-rules", global_flag=True)
        self.reap_compute("target-pools", global_flag=True)
        self.reap_compute("backend-services", global_flag=True)
        # Health checks are listed directly: after the pools and backend
        # services above are gone, nothing references them.
        self.reap_compute("http-health-checks")
        self.reap_compute("health-checks", global_flag=True)
        self.reap_compute("target-https-proxies", global_flag=True)
        self.reap_compute("url-maps", global_flag=True)
        self.reap_compute("ssl-certificates", global_flag=True)
        self.reap_compute("addresses", global_flag=True)
        self.reap_compute("network-endpoint-groups", single_name=True)
        self.reap_firewall_rules()
        self.reap_compute("disks")
        self.reap_dns_zones()
        self.reap_service_accounts()


def main() -> int:
    project = os.environ.get("MDB_GKE_PROJECT", "")
    if not project:
        print("ERROR: MDB_GKE_PROJECT is required", file=sys.stderr)
        return 1
    try:
        age_threshold_hours = positive_int_env("AGE_THRESHOLD_HOURS", 24)
        batch_size = positive_int_env("BATCH_SIZE", 50)
        dry_run = bool_env("DRY_RUN", False)
    except ValueError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    reaper = Reaper(project, age_threshold_hours, dry_run, batch_size)
    reaper.run()
    return 1 if reaper.failed else 0


if __name__ == "__main__":
    sys.exit(main())
