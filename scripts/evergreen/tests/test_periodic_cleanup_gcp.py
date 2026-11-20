"""Tests for the periodic GCP reaper (scripts/evergreen/periodic_cleanup_gcp.py).

The periodic build exercises the real deletion paths; these tests only pin the
safety-critical behaviour: dry-run makes no calls, denylisted and young
resources are kept, deletes wait for their operation in dependency order,
malformed inventory fails closed, and one failure does not hide the others.
The GCP clients are autospecced so a wrong call signature fails the test.
"""

from datetime import datetime, timedelta, timezone
from unittest.mock import create_autospec

import pytest
from google.cloud import asset_v1, iam_admin_v1, resourcemanager_v3

from scripts.evergreen import periodic_cleanup_gcp as gc
from scripts.evergreen.periodic_cleanup_gcp import Api, Reaper

PROJECT = "test-project"
COMPUTE = f"https://compute.googleapis.com/compute/v1/projects/{PROJECT}/"
FIREWALL = "compute.googleapis.com/Firewall"


@pytest.fixture(autouse=True)
def no_sleep(monkeypatch):
    monkeypatch.setattr(gc, "POLL_SECONDS", 0)


def ago(hours: float) -> datetime:
    return datetime.now(timezone.utc) - timedelta(hours=hours)


def asset(asset_type: str, path: str, hours: float = 48) -> asset_v1.ResourceSearchResult:
    return asset_v1.ResourceSearchResult(
        name=f"//{asset_type.split('/')[0]}/projects/{PROJECT}/{path}", asset_type=asset_type, create_time=ago(hours)
    )


class FakeApi(Api):
    """Serves scripted responses per (method, url); the last one repeats."""

    def __init__(self, **scripted):
        super().__init__(session=None)
        self.responses = {tuple(key.split(" ", 1)): list(values) for key, values in scripted.items()}
        self.calls = []

    def script(self, method: str, url: str, *responses) -> None:
        self.responses[(method, url)] = list(responses)

    def _call(self, method, url, **kwargs):
        self.calls.append((method, url))
        queue = self.responses.get((method, url))
        assert queue, f"unexpected {method} {url}"
        response = queue.pop(0) if len(queue) > 1 else queue[0]
        if isinstance(response, Exception):
            raise response
        return response


def compute_delete(api: FakeApi, path: str, *final_ops) -> str:
    """Script a compute DELETE whose operation goes RUNNING then final_ops."""
    url = COMPUTE + path
    operation = f"{COMPUTE}global/operations/op-{path.rsplit('/', 1)[-1]}"
    api.script("DELETE", url, {"status": "RUNNING", "selfLink": operation})
    api.script("GET", operation, *(final_ops or ({"status": "DONE"},)))
    return url


def make_reaper(api: FakeApi, results, dry_run: bool = False) -> Reaper:
    assets = create_autospec(asset_v1.AssetServiceClient, instance=True)
    assets.search_all_resources.return_value = list(results)
    return Reaper(
        PROJECT,
        24,
        dry_run,
        api=api,
        assets=assets,
        iam=create_autospec(iam_admin_v1.IAMClient, instance=True),
        projects=create_autospec(resourcemanager_v3.ProjectsClient, instance=True),
    )


def test_dry_run_makes_no_calls(capsys):
    api = FakeApi()
    reaper = make_reaper(api, [asset(FIREWALL, "global/firewalls/k8s-fw")], dry_run=True)

    reaper.reap_assets()

    assert api.calls == []
    out = capsys.readouterr().out
    assert "dry-run: delete global/firewalls/k8s-fw" in out
    assert "1 would-delete, 0 failed" in out


def test_denylisted_and_young_resources_are_kept(capsys):
    api = FakeApi()
    kept = compute_delete(api, "global/firewalls/k8s-fw")
    reaper = make_reaper(
        api,
        [
            asset(FIREWALL, "global/firewalls/default-allow-ssh"),
            asset(FIREWALL, "global/firewalls/k8s-fw"),
            asset(FIREWALL, "global/firewalls/k8s-new", hours=1),
        ],
    )

    reaper.reap_assets()

    assert [url for method, url in api.calls if method == "DELETE"] == [kept]
    assert "3 listed, 1 denylisted, 1 older than 24h, 1 deleted, 0 failed" in capsys.readouterr().out


def test_deletes_types_in_dependency_order_and_waits():
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
    assert [url for _, url in api.calls] == [
        rule,
        f"{COMPUTE}global/operations/op-fr",
        pool,
        f"{COMPUTE}global/operations/op-tp",
    ]


def test_one_failed_delete_does_not_hide_the_others(capsys):
    api = FakeApi()
    compute_delete(api, "global/firewalls/bad", {"status": "DONE", "error": {"errors": [{"message": "in use"}]}})
    compute_delete(api, "global/firewalls/good")
    reaper = make_reaper(api, [asset(FIREWALL, "global/firewalls/bad"), asset(FIREWALL, "global/firewalls/good")])

    reaper.reap_assets()

    out = capsys.readouterr().out
    assert "error: global/firewalls/bad: in use" in out
    assert "1 deleted, 1 failed" in out
    assert reaper.failed


def test_malformed_inventory_aborts_only_its_type(capsys):
    api = FakeApi()
    disk = compute_delete(api, "zones/z/disks/pvc-1")
    broken = asset_v1.ResourceSearchResult(name=f"//compute.googleapis.com/projects/{PROJECT}/x", asset_type=FIREWALL)
    reaper = make_reaper(api, [broken, asset("compute.googleapis.com/Disk", "zones/z/disks/pvc-1")])

    reaper.reap_assets()

    assert [url for method, url in api.calls if method == "DELETE"] == [disk]
    assert f"ERROR: failed to list {FIREWALL}" in capsys.readouterr().out
    assert reaper.failed
