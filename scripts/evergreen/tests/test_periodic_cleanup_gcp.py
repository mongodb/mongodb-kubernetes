"""Tests for the periodic GCP reaper (scripts/evergreen/periodic_cleanup_gcp.py).

The GCP clients are autospecced from the real classes so a wrong call signature
fails the test; payloads use the real proto types.
"""

from datetime import datetime, timedelta, timezone
from unittest.mock import create_autospec

import pytest
from google.api_core import exceptions as api_errors
from google.cloud import asset_v1, iam_admin_v1, resourcemanager_v3
from google.iam.v1 import policy_pb2

from scripts.evergreen import periodic_cleanup_gcp as gc
from scripts.evergreen.periodic_cleanup_gcp import Api, InventoryError, Reaper, parse_asset

PROJECT = "test-project"
COMPUTE = f"https://compute.googleapis.com/compute/v1/projects/{PROJECT}/"
FIREWALL = "compute.googleapis.com/Firewall"


@pytest.fixture(autouse=True)
def no_sleep(monkeypatch):
    monkeypatch.setattr(gc, "POLL_SECONDS", 0)


def ago(hours: float) -> datetime:
    return datetime.now(timezone.utc) - timedelta(hours=hours)


def asset(asset_type: str, path: str, hours: float = 48, project: str = PROJECT) -> asset_v1.ResourceSearchResult:
    service = asset_type.split("/")[0]
    return asset_v1.ResourceSearchResult(
        name=f"//{service}/projects/{project}/{path}", asset_type=asset_type, create_time=ago(hours)
    )


class FakeApi(Api):
    """Serves scripted responses per (method, url); the last one repeats."""

    def __init__(self, **scripted):
        super().__init__(session=None)
        self.responses = {tuple(key.split(" ", 1)): list(values) for key, values in scripted.items()}
        self.calls, self.bodies = [], []

    def script(self, method: str, url: str, *responses) -> None:
        self.responses[(method, url)] = list(responses)

    def _call(self, method, url, **kwargs):
        self.calls.append((method, url))
        self.bodies.append(kwargs.get("json"))
        queue = self.responses.get((method, url))
        assert queue, f"unexpected {method} {url}"
        response = queue.pop(0) if len(queue) > 1 else queue[0]
        if isinstance(response, Exception):
            raise response
        return response


def compute_delete(api: FakeApi, path: str, *final_ops) -> str:
    """Script a compute DELETE whose operation is RUNNING, then final_ops."""
    url, op = COMPUTE + path, f"{COMPUTE}global/operations/op-{path.rsplit('/', 1)[-1]}"
    api.script("DELETE", url, {"status": "RUNNING", "selfLink": op})
    api.script("GET", op, *(final_ops or ({"status": "DONE"},)))
    return url


def make_reaper(api=None, results=(), dry_run=False, accounts=(), keys=None, policy=None):
    assets = create_autospec(asset_v1.AssetServiceClient, instance=True)
    assets.search_all_resources.return_value = list(results)
    iam = create_autospec(iam_admin_v1.IAMClient, instance=True)
    iam.list_service_accounts.return_value = list(accounts)
    iam.list_service_account_keys.side_effect = lambda name, key_types: iam_admin_v1.ListServiceAccountKeysResponse(
        keys=[iam_admin_v1.ServiceAccountKey(valid_after_time=ago(hours)) for hours in (keys or {}).get(name, [])]
    )
    projects = create_autospec(resourcemanager_v3.ProjectsClient, instance=True)
    projects.get_iam_policy.return_value = policy or policy_pb2.Policy()
    return Reaper(PROJECT, 24, dry_run, api=api or FakeApi(), assets=assets, iam=iam, projects=projects)


def test_parse_asset_maps_names_to_rest_urls():
    cluster = parse_asset(asset("container.googleapis.com/Cluster", "zones/z-a/clusters/k8s-1"), PROJECT)
    assert cluster.url == f"https://container.googleapis.com/v1/projects/{PROJECT}/zones/z-a/clusters/k8s-1"
    assert (cluster.name, cluster.label) == ("k8s-1", "zones/z-a/clusters/k8s-1")
    zone = parse_asset(asset("dns.googleapis.com/ManagedZone", "managedZones/246523"), PROJECT)
    assert zone.url == f"https://dns.googleapis.com/dns/v1/projects/{PROJECT}/managedZones/246523"


@pytest.mark.parametrize(
    "result",
    [
        asset_v1.ResourceSearchResult(name=f"//compute.googleapis.com/projects/{PROJECT}/global/firewalls/x"),
        asset(FIREWALL, "global/firewalls/x", project="other-project"),
        asset("storage.googleapis.com/Bucket", "buckets/b"),
        asset_v1.ResourceSearchResult(name=f"compute.googleapis.com/projects/{PROJECT}/x", create_time=ago(48)),
    ],
    ids=["no-create-time", "foreign-project", "unknown-service", "not-an-asset-name"],
)
def test_parse_asset_fails_closed(result):
    with pytest.raises(InventoryError):
        parse_asset(result, PROJECT)


def test_old_resources_deleted_after_their_operation_is_done(capsys):
    api = FakeApi()
    old = compute_delete(api, "global/firewalls/k8s-old", {"status": "RUNNING"}, {"status": "DONE"})
    reaper = make_reaper(api, [asset(FIREWALL, "global/firewalls/k8s-old"), asset(FIREWALL, "global/firewalls/new", 1)])

    reaper.reap_assets()

    assert api.calls == [("DELETE", old)] + [("GET", f"{COMPUTE}global/operations/op-k8s-old")] * 2
    assert "summary: 2 listed, 0 denylisted, 1 older than 24h, 1 deleted, 0 failed" in capsys.readouterr().out
    assert not reaper.failed


def test_denylisted_firewall_is_kept(capsys):
    api = FakeApi()
    kept = compute_delete(api, "global/firewalls/k8s-fw")
    reaper = make_reaper(
        api, [asset(FIREWALL, "global/firewalls/default-allow-ssh"), asset(FIREWALL, "global/firewalls/k8s-fw")]
    )

    reaper.reap_assets()

    assert [url for method, url in api.calls if method == "DELETE"] == [kept]
    assert "summary: 2 listed, 1 denylisted, 1 older than 24h, 1 deleted, 0 failed" in capsys.readouterr().out


def test_types_are_deleted_in_dependency_order():
    api = FakeApi()
    rule = compute_delete(api, "regions/r/forwardingRules/fr")
    pool = compute_delete(api, "regions/r/targetPools/tp")
    reaper = make_reaper(
        api,
        [
            asset("compute.googleapis.com/TargetPool", "regions/r/targetPools/tp"),
            asset("compute.googleapis.com/ForwardingRule", "regions/r/forwardingRules/fr"),
        ],
    )

    reaper.reap_assets()

    # The forwarding rule's operation completes before the target pool is touched.
    assert [call[1] for call in api.calls] == [
        rule,
        f"{COMPUTE}global/operations/op-fr",
        pool,
        f"{COMPUTE}global/operations/op-tp",
    ]


def test_failed_operation_fails_only_its_resource(capsys):
    api = FakeApi()
    compute_delete(api, "global/firewalls/bad", {"status": "DONE", "error": {"errors": [{"message": "in use"}]}})
    compute_delete(api, "global/firewalls/good")
    reaper = make_reaper(api, [asset(FIREWALL, "global/firewalls/bad"), asset(FIREWALL, "global/firewalls/good")])

    reaper.reap_assets()

    out = capsys.readouterr().out
    assert "error: global/firewalls/bad: in use" in out
    assert "1 deleted, 1 failed" in out
    assert reaper.failed


def test_operation_timeout_is_a_failure(monkeypatch, capsys):
    monkeypatch.setattr(gc, "OPERATION_TIMEOUT_SECONDS", -1)
    api = FakeApi()
    compute_delete(api, "global/firewalls/stuck", {"status": "RUNNING"})
    reaper = make_reaper(api, [asset(FIREWALL, "global/firewalls/stuck")])

    reaper.reap_assets()

    assert "not done after" in capsys.readouterr().out
    assert reaper.failed


def test_malformed_entry_aborts_only_its_type(capsys):
    api = FakeApi()
    disk = compute_delete(api, "zones/z/disks/pvc-1")
    broken = asset_v1.ResourceSearchResult(
        name=f"//compute.googleapis.com/projects/{PROJECT}/global/firewalls/x", asset_type=FIREWALL
    )
    reaper = make_reaper(
        api,
        [broken, asset(FIREWALL, "global/firewalls/y"), asset("compute.googleapis.com/Disk", "zones/z/disks/pvc-1")],
    )

    reaper.reap_assets()

    assert [url for method, url in api.calls if method == "DELETE"] == [disk]
    assert f"ERROR: failed to list {FIREWALL}" in capsys.readouterr().out
    assert reaper.failed


def test_search_failure_deletes_nothing(capsys):
    api = FakeApi()
    reaper = make_reaper(api)
    reaper.assets.search_all_resources.side_effect = api_errors.InternalServerError("boom")

    reaper.reap_assets()

    assert api.calls == []
    assert "asset search failed, nothing deleted" in capsys.readouterr().out
    assert reaper.failed


def test_dry_run_makes_no_calls(capsys):
    api = FakeApi()
    reaper = make_reaper(api, [asset(FIREWALL, "global/firewalls/k8s-fw")], dry_run=True)

    reaper.reap_assets()

    assert api.calls == []
    out = capsys.readouterr().out
    assert "dry-run: delete global/firewalls/k8s-fw" in out
    assert "1 would-delete, 0 failed" in out


def test_already_deleted_counts_as_deleted(capsys):
    api = FakeApi(**{f"DELETE {COMPUTE}global/firewalls/gone": [api_errors.NotFound("gone")]})
    reaper = make_reaper(api, [asset(FIREWALL, "global/firewalls/gone")])

    reaper.reap_assets()

    assert "1 deleted, 0 failed" in capsys.readouterr().out
    assert not reaper.failed


def test_dns_zone_is_emptied_then_deleted():
    zone = f"https://dns.googleapis.com/dns/v1/projects/{PROJECT}/managedZones/246523"
    a_record = {"name": "om.example.", "type": "A", "ttl": 60, "rrdatas": ["1.2.3.4"]}
    api = FakeApi()
    api.script(
        "GET",
        f"{zone}/rrsets",
        {"rrsets": [{"name": "example.", "type": "NS"}, {"name": "example.", "type": "SOA"}], "nextPageToken": "p2"},
        {"rrsets": [a_record]},
    )
    api.script("POST", f"{zone}/changes", {"id": "7", "status": "pending"})
    api.script("GET", f"{zone}/changes/7", {"id": "7", "status": "done"})
    api.script("DELETE", zone, {})
    reaper = make_reaper(api, [asset("dns.googleapis.com/ManagedZone", "managedZones/246523")])

    reaper.reap_assets()

    assert api.bodies[2] == {"deletions": [a_record]}
    assert api.calls[-2:] == [("GET", f"{zone}/changes/7"), ("DELETE", zone)]
    assert not reaper.failed


def sa(email: str) -> iam_admin_v1.ServiceAccount:
    return iam_admin_v1.ServiceAccount(email=email, name=f"projects/{PROJECT}/serviceAccounts/{email}")


def test_service_accounts_are_unbound_then_deleted(capsys):
    old, young, keyless = (sa(f"{prefix}@{PROJECT}.iam.gserviceaccount.com") for prefix in ("old", "young", "keyless"))
    ci = sa(f"k8s-operator-e2e-tests@{PROJECT}.iam.gserviceaccount.com")
    member = f"serviceAccount:{old.email}"
    policy = policy_pb2.Policy(
        bindings=[
            policy_pb2.Binding(role="roles/dns.admin", members=[member, "user:x@y.com"]),
            policy_pb2.Binding(role="roles/viewer", members=[member]),
        ]
    )
    reaper = make_reaper(
        accounts=[ci, old, young, keyless], keys={ci.name: [48], old.name: [48, 30], young.name: [48, 1]}, policy=policy
    )

    reaper.reap_service_accounts()

    reaper.projects.set_iam_policy.assert_called_once()
    written = reaper.projects.set_iam_policy.call_args.kwargs["request"]["policy"]
    assert [(b.role, list(b.members)) for b in written.bindings] == [("roles/dns.admin", ["user:x@y.com"])]
    reaper.iam.delete_service_account.assert_called_once_with(name=old.name)
    assert "summary: 4 listed, 1 denylisted, 1 older than 24h, 1 deleted, 0 failed" in capsys.readouterr().out


def test_service_account_unbind_failure_deletes_nothing():
    old = sa(f"old@{PROJECT}.iam.gserviceaccount.com")
    policy = policy_pb2.Policy(
        bindings=[policy_pb2.Binding(role="roles/viewer", members=[f"serviceAccount:{old.email}"])]
    )
    reaper = make_reaper(accounts=[old], keys={old.name: [48]}, policy=policy)
    reaper.projects.set_iam_policy.side_effect = api_errors.Aborted("etag mismatch")

    reaper.reap_service_accounts()

    reaper.iam.delete_service_account.assert_not_called()
    assert reaper.failed


def test_service_accounts_dry_run_mutates_nothing():
    old = sa(f"old@{PROJECT}.iam.gserviceaccount.com")
    reaper = make_reaper(accounts=[old], keys={old.name: [48]}, dry_run=True)

    reaper.reap_service_accounts()

    reaper.projects.get_iam_policy.assert_not_called()
    reaper.iam.delete_service_account.assert_not_called()


@pytest.mark.parametrize(
    "env",
    [{}, {"MDB_GKE_PROJECT": "p", "AGE_THRESHOLD_HOURS": "0"}, {"MDB_GKE_PROJECT": "p", "DRY_RUN": "yes"}],
    ids=["no-project", "bad-threshold", "bad-dry-run"],
)
def test_main_rejects_bad_env(monkeypatch, env):
    for name in ("MDB_GKE_PROJECT", "AGE_THRESHOLD_HOURS", "DRY_RUN"):
        monkeypatch.delenv(name, raising=False)
    for name, value in env.items():
        monkeypatch.setenv(name, value)
    assert gc.main() == 1
