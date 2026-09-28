"""Tests for the periodic GCP reaper (scripts/evergreen/periodic_cleanup_gcp.py)."""

import subprocess
import time

import pytest

from scripts.evergreen.periodic_cleanup_gcp import (
    InventoryError,
    Reaper,
    is_denylisted,
    parse_inventory,
    scope_of,
)


def epoch(hours_ago: float) -> str:
    return f"{time.time() - hours_ago * 3600:.3f}"


def completed(returncode: int = 0, stdout: str = "", stderr: str = "") -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess([], returncode, stdout, stderr)


class FakeGcloud:
    """Records invocations and answers from ordered (predicate, response) rules."""

    def __init__(self, responses=()):
        self.commands: list[list[str]] = []
        self.responses = list(responses)

    def __call__(self, cmd):
        cmd = list(cmd)
        self.commands.append(cmd)
        for predicate, response in self.responses:
            if predicate(cmd):
                return response
        return completed()

    def deletes(self) -> list[list[str]]:
        return [cmd for cmd in self.commands if "delete" in cmd]


def make_reaper(fake: FakeGcloud, **overrides) -> Reaper:
    options = {"project": "test-project", "age_threshold_hours": 24, "dry_run": False, "executor": fake}
    options.update(overrides)
    return Reaper(**options)


def runs(cmd, *prefix: str) -> bool:
    """True when the gcloud call starts with the given subcommand path."""
    return cmd[: len(prefix) + 1] == ["gcloud", *prefix]


def contains(cmd, fragment: str) -> bool:
    return any(fragment in arg for arg in cmd)


def test_parse_inventory_rejects_malformed_rows():
    with pytest.raises(InventoryError):
        parse_inventory("k8s-fw-abc\tnot-a-timestamp", columns=2, timestamp_index=1)
    with pytest.raises(InventoryError):
        parse_inventory("only-one-column", columns=2, timestamp_index=1)


def test_scope_of_extracts_basename():
    assert (
        scope_of("", "https://www.googleapis.com/compute/v1/projects/p/zones/europe-central2-a")
        == "zone:europe-central2-a"
    )
    assert (
        scope_of("https://www.googleapis.com/compute/v1/projects/p/regions/europe-central2", "")
        == "region:europe-central2"
    )
    assert scope_of("", "") == "global"


def test_denylists():
    assert is_denylisted("firewall-rules", "default-allow-ssh")
    assert not is_denylisted("firewall-rules", "k8s-fw-abc")
    assert is_denylisted("service-accounts", "k8s-operator-e2e-tests@test-project.iam.gserviceaccount.com")
    assert is_denylisted("service-accounts", "123456-compute@developer.gserviceaccount.com")
    assert not is_denylisted("service-accounts", "ext-dns-sa-abc@test-project.iam.gserviceaccount.com")
    assert not is_denylisted("disks", "default-disk")


def test_firewall_rules_skip_denylist_and_young_rules():
    old, young = epoch(48), epoch(1)
    listing = f"default-allow-ssh\t{old}\nk8s-fw-abc\t{old}\nk8s-fw-new\t{young}\n"
    fake = FakeGcloud([(lambda cmd: runs(cmd, "compute", "firewall-rules", "list"), completed(stdout=listing))])
    reaper = make_reaper(fake)

    reaper.reap_firewall_rules()

    assert len(fake.deletes()) == 1
    delete = fake.deletes()[0]
    assert "k8s-fw-abc" in delete
    assert "default-allow-ssh" not in delete
    assert "k8s-fw-new" not in delete
    assert not reaper.failed


def test_malformed_inventory_aborts_class(capsys):
    fake = FakeGcloud(
        [(lambda cmd: runs(cmd, "compute", "firewall-rules", "list"), completed(stdout="k8s-fw-abc\tgarbage\n"))]
    )
    reaper = make_reaper(fake)

    reaper.reap_firewall_rules()

    assert not fake.deletes()
    assert reaper.failed
    assert "malformed firewall-rules inventory" in capsys.readouterr().out


def test_dry_run_logs_but_never_deletes(capsys):
    fake = FakeGcloud(
        [(lambda cmd: runs(cmd, "compute", "firewall-rules", "list"), completed(stdout=f"k8s-fw-abc\t{epoch(48)}\n"))]
    )
    reaper = make_reaper(fake, dry_run=True)

    reaper.reap_firewall_rules()

    assert not fake.deletes()
    out = capsys.readouterr().out
    assert "dry-run: gcloud compute firewall-rules delete k8s-fw-abc" in out
    assert "1 would-delete, 0 failed" in out


def test_batch_failure_falls_back_to_per_name(capsys):
    old = epoch(48)
    listing = (
        f"pvc-1\thttps://x/regions/r\thttps://x/zones/z-a\t{old}\n"
        f"pvc-2\thttps://x/regions/r\thttps://x/zones/z-a\t{old}\n"
    )
    batch = lambda cmd: runs(cmd, "compute", "disks", "delete") and "pvc-1" in cmd and "pvc-2" in cmd  # noqa: E731
    fake = FakeGcloud(
        [
            (lambda cmd: runs(cmd, "compute", "disks", "list"), completed(stdout=listing)),
            (batch, completed(returncode=1, stderr="batch failed")),
            (lambda cmd: runs(cmd, "compute", "disks", "delete") and "pvc-1" in cmd, completed()),
            (
                lambda cmd: runs(cmd, "compute", "disks", "delete") and "pvc-2" in cmd,
                completed(returncode=1, stderr="resource in use"),
            ),
        ]
    )
    reaper = make_reaper(fake)

    reaper.reap_compute("disks")

    assert reaper.failed
    deletes = fake.deletes()
    assert len(deletes) == 3  # one batch + two individual retries
    assert "--zone=z-a" in deletes[0]
    assert "1 deleted, 1 failed" in capsys.readouterr().out


def test_every_inventory_passes_a_format_flag():
    # A bare `gcloud ... list value(...)` is silently interpreted as a
    # deprecated NAME filter (0 results, exit 0), so every list must carry the
    # --format= flag.
    fake = FakeGcloud()
    reaper = make_reaper(fake)

    reaper.reap_clusters()
    reaper.reap_compute("disks")
    reaper.reap_firewall_rules()
    reaper.reap_dns_zones()
    reaper.reap_service_accounts()

    lists = [cmd for cmd in fake.commands if "list" in cmd]
    assert lists
    for cmd in lists:
        assert any(arg.startswith("--format=") for arg in cmd), cmd


def test_network_endpoint_groups_delete_one_name_per_call():
    old = epoch(48)
    listing = (
        f"neg-1\thttps://x/regions/r\thttps://x/zones/z-a\t{old}\n"
        f"neg-2\thttps://x/regions/r\thttps://x/zones/z-a\t{old}\n"
    )
    fake = FakeGcloud(
        [(lambda cmd: runs(cmd, "compute", "network-endpoint-groups", "list"), completed(stdout=listing))]
    )
    reaper = make_reaper(fake)

    reaper.reap_compute("network-endpoint-groups", single_name=True)

    deletes = fake.deletes()
    assert len(deletes) == 2
    for cmd in deletes:
        assert len([arg for arg in cmd if arg.startswith("neg-")]) == 1


def test_service_accounts_delete_only_stale_keyed_accounts(capsys):
    denylisted = "k8s-operator-e2e-tests@test-project.iam.gserviceaccount.com"
    stale = "ext-dns-sa-old@test-project.iam.gserviceaccount.com"
    young = "ext-dns-sa-young@test-project.iam.gserviceaccount.com"
    listing = f"{denylisted}\n{stale}\n{young}\n"
    fake = FakeGcloud(
        [
            (lambda cmd: runs(cmd, "iam", "service-accounts", "list"), completed(stdout=listing)),
            (
                lambda cmd: runs(cmd, "iam", "service-accounts", "keys", "list") and contains(cmd, stale),
                completed(stdout=f"{epoch(48)}\n"),
            ),
            (
                lambda cmd: runs(cmd, "iam", "service-accounts", "keys", "list") and contains(cmd, young),
                completed(stdout=f"{epoch(1)}\n"),
            ),
        ]
    )
    reaper = make_reaper(fake)

    reaper.reap_service_accounts()

    key_lists = [cmd for cmd in fake.commands if "keys" in cmd]
    assert len(key_lists) == 2  # the denylisted account is never inspected
    assert not any(denylisted in cmd for cmd in key_lists)
    assert any(
        runs(cmd, "projects", "remove-iam-policy-binding") and contains(cmd, f"serviceAccount:{stale}")
        for cmd in fake.commands
    )
    assert any(runs(cmd, "iam", "service-accounts", "delete") and stale in cmd for cmd in fake.commands)
    assert not any(young in cmd for cmd in fake.deletes())
    assert "1 deleted, 0 failed" in capsys.readouterr().out
