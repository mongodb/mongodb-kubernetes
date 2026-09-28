"""Tests for the periodic GCP reaper (scripts/evergreen/periodic_cleanup_gcp.py)."""

import time
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

from google.api_core import exceptions as api_errors
from google.cloud import compute_v1, container_v1, iam_admin_v1, resourcemanager_v3

from scripts.evergreen import periodic_cleanup_gcp as reaper_module
from scripts.evergreen.periodic_cleanup_gcp import Reaper, is_denylisted, parse_time


def iso(hours_ago: float) -> str:
    return (datetime.now(timezone.utc) - timedelta(hours=hours_ago)).isoformat()


def gce_item(name: str, hours_ago: float = 48) -> SimpleNamespace:
    return SimpleNamespace(name=name, creation_timestamp=iso(hours_ago))


def scoped(collection: str, *items) -> SimpleNamespace:
    return SimpleNamespace(**{collection: list(items)})


class FakeComputeClient:
    def __init__(self, items=(), aggregated=(), failing=()):
        self.items = list(items)
        self.aggregated = list(aggregated)
        self.failing = set(failing)
        self.deletes: list[dict] = []

    def list(self, project):
        return list(self.items)

    def aggregated_list(self, project):
        return list(self.aggregated)

    def delete(self, **kwargs):
        self.deletes.append(kwargs)
        name = next(value for key, value in kwargs.items() if key not in ("project", "zone", "region"))
        if name in self.failing:
            raise api_errors.InvalidArgument("resource is in use")


class FakeContainerClient:
    def __init__(self, clusters, operation_statuses=(container_v1.Operation.Status.DONE,)):
        self.clusters = list(clusters)
        self.operation_statuses = iter(operation_statuses)
        self.deleted: list[str] = []

    def list_clusters(self, parent):
        return SimpleNamespace(clusters=self.clusters)

    def delete_cluster(self, name):
        self.deleted.append(name)
        return SimpleNamespace(name="operation-1", status=container_v1.Operation.Status.RUNNING)

    def get_operation(self, name):
        status = next(self.operation_statuses, container_v1.Operation.Status.DONE)
        return SimpleNamespace(name=name, status=status, error=SimpleNamespace(code=0, message=""))


class FakeIAMClient:
    def __init__(self, accounts, keys):
        self.accounts = list(accounts)
        self.keys = keys
        self.deleted: list[str] = []

    def list_service_accounts(self, name):
        return list(self.accounts)

    def list_service_account_keys(self, name, key_types):
        email = name.rsplit("/", 1)[-1]
        return SimpleNamespace(keys=self.keys.get(email, []))

    def delete_service_account(self, name):
        self.deleted.append(name)


class FakeIAMPolicy:
    def __init__(self, bindings):
        self.bindings = list(bindings)


class FakeIAMBinding:
    def __init__(self, role, members):
        self.role = role
        self.members = list(members)


class FakeResourceManagerClient:
    def __init__(self, policy):
        self.policy = policy
        self.written = []

    def get_iam_policy(self, resource):
        return self.policy

    def set_iam_policy(self, resource, policy):
        self.written.append(policy)


class FakeRestClient:
    def __init__(self, responses=None, failing=()):
        self.responses = responses or {}
        self.failing = set(failing)
        self.calls: list[tuple[str, str]] = []

    def get(self, url, key):
        self.calls.append(("GET", url))
        if url in self.failing:
            raise api_errors.InternalServerError("list failed")
        return self.responses.get(url, [])

    def post(self, url, body):
        self.calls.append(("POST", url))
        self.responses.setdefault("posted", []).append(body)
        return {}

    def delete(self, url):
        self.calls.append(("DELETE", url))
        if url in self.failing:
            raise api_errors.InvalidArgument("in use")


class FakeClients:
    def __init__(self, mapping=None, rest=None, default=None):
        self.mapping = mapping or {}
        self.rest = rest
        self.default = default if default is not None else FakeComputeClient()

    def get(self, client_class):
        return self.mapping.get(client_class, self.default)


def make_reaper(clients, dry_run=False):
    return Reaper("test-project", 24, dry_run, clients)


def test_parse_time_accepts_rfc3339_with_offset_and_zulu():
    assert parse_time("2026-09-28T08:11:35+00:00") is not None
    assert parse_time("2026-09-28T08:11:35Z") is not None
    assert parse_time("not-a-timestamp") is None
    assert parse_time("") is None


def test_denylists():
    assert is_denylisted("firewall-rules", "default-allow-ssh")
    assert not is_denylisted("firewall-rules", "k8s-fw-abc")
    assert is_denylisted("service-accounts", "k8s-operator-e2e-tests@test-project.iam.gserviceaccount.com")
    assert is_denylisted("service-accounts", "123456-compute@developer.gserviceaccount.com")
    assert not is_denylisted("service-accounts", "ext-dns-sa-abc@test-project.iam.gserviceaccount.com")


def test_firewall_rules_skip_denylist_and_young(capsys):
    client = FakeComputeClient(
        items=[
            gce_item("default-allow-ssh", hours_ago=48),
            gce_item("k8s-fw-abc", hours_ago=48),
            gce_item("k8s-fw-new", hours_ago=1),
        ]
    )
    reaper = make_reaper(FakeClients({compute_v1.FirewallsClient: client}))

    reaper.reap_firewall_rules()

    assert [call["firewall"] for call in client.deletes] == ["k8s-fw-abc"]
    out = capsys.readouterr().out
    assert "skipped 1 denylisted rule(s)" in out
    assert "3 listed, 2 older than 24h, 1 deleted, 0 failed" in out
    assert not reaper.failed


def test_zonal_deletes_include_zone_from_aggregated_scope():
    client = FakeComputeClient(aggregated=[("zones/europe-central2-a", scoped("disks", gce_item("pvc-1")))])
    reaper = make_reaper(FakeClients({compute_v1.DisksClient: client}))

    reaper.reap_compute("disks")

    assert client.deletes == [{"project": "test-project", "disk": "pvc-1", "zone": "europe-central2-a"}]


def test_regional_forwarding_rules_include_region():
    client = FakeComputeClient(aggregated=[("regions/europe-central2", scoped("forwarding_rules", gce_item("fw-1")))])
    reaper = make_reaper(FakeClients({compute_v1.ForwardingRulesClient: client}))

    reaper.reap_compute("forwarding-rules")

    assert client.deletes == [{"project": "test-project", "forwarding_rule": "fw-1", "region": "europe-central2"}]


def test_malformed_timestamp_aborts_class(capsys):
    bad = SimpleNamespace(name="pvc-1", creation_timestamp="garbage")
    client = FakeComputeClient(aggregated=[("zones/z-a", scoped("disks", bad))])
    reaper = make_reaper(FakeClients({compute_v1.DisksClient: client}))

    reaper.reap_compute("disks")

    assert client.deletes == []
    assert reaper.failed
    out = capsys.readouterr().out
    assert "ERROR: failed to list disks" in out
    assert "bad creation timestamp" in out
    assert "0 deleted" in out


def test_dry_run_never_calls_delete(capsys):
    client = FakeComputeClient(items=[gce_item("k8s-fw-abc")])
    reaper = make_reaper(FakeClients({compute_v1.FirewallsClient: client}), dry_run=True)

    reaper.reap_firewall_rules()

    assert client.deletes == []
    assert "dry-run: delete firewall-rules/k8s-fw-abc" in capsys.readouterr().out


def test_delete_failure_marks_overall_failed_and_continues(capsys):
    client = FakeComputeClient(items=[gce_item("k8s-fw-abc"), gce_item("k8s-fw-def")], failing={"k8s-fw-abc"})
    reaper = make_reaper(FakeClients({compute_v1.FirewallsClient: client}))

    reaper.reap_firewall_rules()

    assert [call["firewall"] for call in client.deletes] == ["k8s-fw-abc", "k8s-fw-def"]
    assert reaper.failed
    assert "1 deleted, 1 failed" in capsys.readouterr().out


def test_cluster_delete_waits_for_operation(monkeypatch):
    monkeypatch.setattr(reaper_module, "CLUSTER_POLL_SECONDS", 0)
    cluster = SimpleNamespace(name="k8s-mdb-0-abc", location="europe-central2-a", create_time=iso(48))
    client = FakeContainerClient(
        [cluster], operation_statuses=(container_v1.Operation.Status.RUNNING, container_v1.Operation.Status.DONE)
    )
    reaper = make_reaper(FakeClients({container_v1.ClusterManagerClient: client}))

    reaper.reap_clusters()

    assert client.deleted == ["projects/test-project/locations/europe-central2-a/clusters/k8s-mdb-0-abc"]
    assert not reaper.failed


def test_service_accounts_delete_only_stale_keyed_accounts(capsys):
    def account(email):
        return SimpleNamespace(email=email, name=f"projects/test-project/serviceAccounts/{email}")

    def key(hours_ago):
        return SimpleNamespace(valid_after_time=SimpleNamespace(timestamp=lambda: time.time() - hours_ago * 3600))

    denylisted = "k8s-operator-e2e-tests@test-project.iam.gserviceaccount.com"
    stale = "ext-dns-sa-old@test-project.iam.gserviceaccount.com"
    young = "ext-dns-sa-young@test-project.iam.gserviceaccount.com"
    iam = FakeIAMClient([account(denylisted), account(stale), account(young)], {stale: [key(48)], young: [key(1)]})
    policy = FakeIAMPolicy(
        [
            FakeIAMBinding("roles/dns.admin", [f"serviceAccount:{stale}"]),
            FakeIAMBinding("roles/viewer", ["user:x@y.com"]),
        ]
    )
    rm = FakeResourceManagerClient(policy)
    reaper = make_reaper(FakeClients({iam_admin_v1.IAMClient: iam, resourcemanager_v3.ProjectsClient: rm}))

    reaper.reap_service_accounts()

    assert iam.deleted == [f"projects/test-project/serviceAccounts/{stale}"]
    assert rm.written
    assert [binding.role for binding in rm.written[0].bindings] == ["roles/viewer"]
    out = capsys.readouterr().out
    assert "skipped 1 denylisted account(s)" in out
    assert "3 listed, 1 older than 24h, 1 deleted, 0 failed" in out
    assert not reaper.failed


def test_http_health_checks_are_deleted_via_rest(capsys):
    url = "https://compute.googleapis.com/compute/v1/projects/test-project/global/httpHealthChecks"
    rest = FakeRestClient(
        {
            url: [
                {"name": "k8s-abc-node", "creationTimestamp": iso(48)},
                {"name": "k8s-new-node", "creationTimestamp": iso(1)},
            ]
        }
    )
    reaper = make_reaper(FakeClients(rest=rest))

    reaper.reap_http_health_checks()

    assert ("DELETE", f"{url}/k8s-abc-node") in rest.calls
    assert ("DELETE", f"{url}/k8s-new-node") not in rest.calls
    assert "2 listed, 1 older than 24h, 1 deleted, 0 failed" in capsys.readouterr().out


def test_dns_zone_deletes_records_then_zone(capsys):
    zones_url = "https://dns.googleapis.com/dns/v1/projects/test-project/managedZones"
    zone = "mongodb-abc"
    records_url = f"{zones_url}/{zone}/rrsets"
    records = [
        {"name": f"{zone}.", "type": "NS"},
        {"name": f"{zone}.", "type": "SOA"},
        {"name": f"om.{zone}.", "type": "A", "ttl": 60, "rrdatas": ["1.2.3.4"]},
    ]
    rest = FakeRestClient({zones_url: [{"name": zone, "creationTime": iso(48)}], records_url: records})
    reaper = make_reaper(FakeClients(rest=rest))

    reaper.reap_dns_zones()

    posted = rest.responses["posted"][0]
    assert [record["name"] for record in posted["deletions"]] == [f"om.{zone}."]
    assert ("DELETE", f"{zones_url}/{zone}") in rest.calls
    assert "1 listed, 1 older than 24h, 1 deleted, 0 failed" in capsys.readouterr().out
    assert not reaper.failed


def test_rest_list_failure_aborts_class():
    url = "https://dns.googleapis.com/dns/v1/projects/test-project/managedZones"
    rest = FakeRestClient(failing={url})
    reaper = make_reaper(FakeClients(rest=rest))

    reaper.reap_dns_zones()

    assert rest.calls == [("GET", url)]
    assert reaper.failed


def test_main_requires_project(monkeypatch):
    monkeypatch.delenv("MDB_GKE_PROJECT", raising=False)
    assert reaper_module.main() == 1
