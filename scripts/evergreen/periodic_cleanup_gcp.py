"""Age-based garbage collector for the GKE code-snippet test project (KUBE-268).

Deletes every resource of REAPED_TYPES, plus service accounts, older than
AGE_THRESHOLD_HOURS, except names matching DENYLISTS. Age is the only ownership
filter: the project only hosts ephemeral e2e resources, and an in-flight run's
resources are younger than the threshold.

Resources are discovered with one Cloud Asset Inventory search. An asset name is
"//<service>/<REST path>", so deleting it is a DELETE on API_ROOTS[service] +
path, followed by waiting for the returned operation. Types are deleted in
dependency order, each type finishing before the next one starts. The inventory
has no creation time for service accounts, so they are aged by their keys.

A malformed inventory skips its type rather than deleting from a partial view;
failed deletes are reported and retried on the next run; any failure makes the
exit code 1.

Requires cloudasset.googleapis.com on the project and roles/cloudasset.viewer.
Auth: GCP_SERVICE_ACCOUNT_JSON_FOR_SNIPPETS_TESTS, else Application Default
Credentials. Env: MDB_GKE_PROJECT (required), AGE_THRESHOLD_HOURS (24),
DRY_RUN (false). Run: scripts/dev/run_python.sh scripts/evergreen/periodic_cleanup_gcp.py
"""

import json
import os
import re
import sys
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Callable

import google.auth
from google.api_core import exceptions as api_errors
from google.auth.transport.requests import AuthorizedSession
from google.cloud import asset_v1, iam_admin_v1, resourcemanager_v3
from google.protobuf import field_mask_pb2

# Deletion (dependency) order; types not listed are never touched. The compute
# Global* types are not searchable: the regional type names include them.
REAPED_TYPES = (
    "container.googleapis.com/Cluster",
    "compute.googleapis.com/ForwardingRule",
    "compute.googleapis.com/TargetPool",
    "compute.googleapis.com/BackendService",
    "compute.googleapis.com/HttpHealthCheck",
    "compute.googleapis.com/HealthCheck",
    "compute.googleapis.com/TargetHttpsProxy",
    "compute.googleapis.com/UrlMap",
    "compute.googleapis.com/SslCertificate",
    "compute.googleapis.com/Address",
    "compute.googleapis.com/NetworkEndpointGroup",
    "compute.googleapis.com/Firewall",
    "compute.googleapis.com/Disk",
    "dns.googleapis.com/ManagedZone",
)
SERVICE_ACCOUNT_TYPE = "iam.googleapis.com/ServiceAccount"
API_ROOTS = {
    "compute.googleapis.com": "https://compute.googleapis.com/compute/v1/",
    "container.googleapis.com": "https://container.googleapis.com/v1/",
    "dns.googleapis.com": "https://dns.googleapis.com/dns/v1/",
}
# Never deleted, matched against the short resource name (email for accounts).
# Keep these small and auditable: everything else is deleted once old.
DENYLISTS = {
    "compute.googleapis.com/Firewall": (r"^default-",),  # shared VPC networking rules
    SERVICE_ACCOUNT_TYPE: (
        r"^k8s-operator-e2e-tests@",  # CI infrastructure account
        r"@developer\.gserviceaccount\.com$",  # GCE default compute account
    ),
}
SCOPES = ["https://www.googleapis.com/auth/cloud-platform"]
USER_MANAGED_KEY = iam_admin_v1.ListServiceAccountKeysRequest.KeyType.USER_MANAGED
OPERATION_TIMEOUT_SECONDS = 1800  # GKE cluster deletions take several minutes
POLL_SECONDS = 5
DELETE_WORKERS = 16


class InventoryError(ValueError):
    """An inventory entry cannot be trusted for deletion."""


class OperationError(RuntimeError):
    """A long-running operation failed or timed out."""


@dataclass(frozen=True)
class Resource:
    service: str
    path: str  # "projects/<project>/..."
    created: float

    @property
    def url(self) -> str:
        return API_ROOTS[self.service] + self.path

    @property
    def name(self) -> str:
        return self.path.rsplit("/", 1)[-1]

    @property
    def label(self) -> str:
        return self.path.split("/", 2)[2]  # without "projects/<project>/"


@dataclass
class Summary:
    listed: int = 0
    skipped: int = 0
    old: int = 0
    deleted: int = 0
    failed: int = 0


def parse_asset(result, project: str) -> Resource:
    """Fail closed on any entry that is not a dated resource of our project."""
    service, _, path = result.name.removeprefix("//").partition("/")
    if not result.name.startswith("//") or service not in API_ROOTS or not path.startswith(f"projects/{project}/"):
        raise InventoryError(f"unexpected asset name {result.name!r}")
    created = result.create_time.timestamp() if result.create_time else 0
    if created <= 0:
        raise InventoryError(f"{result.name}: missing create_time")
    return Resource(service, path, created)


def is_denylisted(kind: str, name: str) -> bool:
    return any(re.search(pattern, name) for pattern in DENYLISTS.get(kind, ()))


class Api:
    """Minimal JSON REST client raising google-api-core errors."""

    def __init__(self, session: AuthorizedSession):
        self._session = session

    def _call(self, method: str, url: str, **kwargs) -> dict:
        response = self._session.request(method, url, **kwargs)
        if not response.ok:
            raise api_errors.from_http_response(response)
        return response.json() if response.content else {}

    def get(self, url: str, params: dict | None = None) -> dict:
        return self._call("GET", url, params=params)

    def post(self, url: str, body: dict) -> dict:
        return self._call("POST", url, json=body)

    def delete(self, url: str) -> dict:
        return self._call("DELETE", url)

    def get_all(self, url: str, key: str) -> list[dict]:
        items, token = [], None
        while True:
            page = self.get(url, {"pageToken": token} if token else None)
            items += page.get(key, [])
            token = page.get("nextPageToken")
            if not token:
                return items


class Reaper:
    def __init__(self, project: str, age_threshold_hours: int, dry_run: bool, *, api, assets, iam, projects):
        self.project = project
        self.age_threshold_hours = age_threshold_hours
        self.dry_run = dry_run
        self.api, self.assets, self.iam, self.projects = api, assets, iam, projects
        self.cutoff = time.time() - age_threshold_hours * 3600
        self.failed = False

    def run(self) -> None:
        if self.dry_run:
            print("=== DRY RUN: no delete or IAM mutation commands will execute ===")
        self.reap_assets()
        self.reap_service_accounts()

    # --- bookkeeping -------------------------------------------------------

    @contextmanager
    def _section(self, kind: str):
        """Print a header and summary around one kind; listing errors abort it."""
        print(f"\n=== {kind} (threshold: {self.age_threshold_hours}h) ===")
        summary = Summary()
        try:
            # this yield line is effectively replaced by the body of with self._section(kind) as summary: expression
            yield summary
        except (api_errors.GoogleAPIError, InventoryError) as exc:
            print(f"ERROR: failed to list {kind}: {exc}")
            summary.failed += 1
        self.failed |= summary.failed > 0
        print(
            f"summary: {summary.listed} listed, {summary.skipped} denylisted, "
            f"{summary.old} older than {self.age_threshold_hours}h, "
            f"{summary.deleted} {'would-delete' if self.dry_run else 'deleted'}, {summary.failed} failed"
        )

    def _allowed(self, summary: Summary, kind: str, items: list, name: Callable) -> list:
        summary.listed = len(items)
        allowed = [item for item in items if not is_denylisted(kind, name(item))]
        summary.skipped = len(items) - len(allowed)
        return allowed

    def _attempt(self, label: str, action: Callable[[], object]) -> bool:
        """Run a mutation; True on success, in dry-run, or when already gone."""
        print(f"  {'dry-run' if self.dry_run else 'running'}: delete {label}")
        if self.dry_run:
            return True
        try:
            action()
            return True
        except api_errors.NotFound:
            return True
        except (api_errors.GoogleAPIError, OperationError) as exc:
            print(f"  error: {label}: {exc}")
            return False

    def _delete_all(self, summary: Summary, items: list, label: Callable, delete: Callable) -> None:
        """Delete in parallel and return only when every item is done."""
        with ThreadPoolExecutor(DELETE_WORKERS) as pool:
            results = list(pool.map(lambda item: self._attempt(label(item), lambda: delete(item)), items))
        summary.deleted += results.count(True)
        summary.failed += results.count(False)

    # --- inventory resources -----------------------------------------------

    def reap_assets(self) -> None:
        request = asset_v1.SearchAllResourcesRequest(
            scope=f"projects/{self.project}",
            asset_types=REAPED_TYPES,
            read_mask=field_mask_pb2.FieldMask(paths=["name", "asset_type", "create_time"]),
        )
        try:
            results = list(self.assets.search_all_resources(request=request))
        except api_errors.GoogleAPIError as exc:
            print(f"ERROR: asset search failed, nothing deleted: {exc}")
            self.failed = True
            return
        for kind in REAPED_TYPES:
            with self._section(kind) as summary:
                resources = [parse_asset(result, self.project) for result in results if result.asset_type == kind]
                old = [r for r in self._allowed(summary, kind, resources, lambda r: r.name) if r.created <= self.cutoff]
                summary.old = len(old)
                self._delete_all(summary, old, lambda r: r.label, self._delete)

    def _delete(self, resource: Resource) -> None:
        if resource.service == "dns.googleapis.com":  # a zone must be empty before deletion
            records = self.api.get_all(f"{resource.url}/rrsets", "rrsets")
            deletions = [record for record in records if record["type"] not in ("NS", "SOA")]
            if deletions:
                change = self.api.post(f"{resource.url}/changes", {"deletions": deletions})
                self._wait(change, f"{resource.url}/changes/{change['id']}")
        operation = self.api.delete(resource.url)
        if "selfLink" in operation:  # compute and GKE deletes are asynchronous
            self._wait(operation, operation["selfLink"])

    def _wait(self, operation: dict, url: str) -> None:
        """Poll a compute/GKE operation or DNS change until done."""
        deadline = time.monotonic() + OPERATION_TIMEOUT_SECONDS
        while operation.get("status", "").upper() != "DONE":
            if time.monotonic() > deadline:
                raise OperationError(f"{url} not done after {OPERATION_TIMEOUT_SECONDS}s")
            time.sleep(POLL_SECONDS)
            operation = self.api.get(url)
        if error := operation.get("error"):
            raise OperationError("; ".join(e.get("message", str(e)) for e in error.get("errors", [error])))

    # --- service accounts --------------------------------------------------

    def reap_service_accounts(self) -> None:
        with self._section(SERVICE_ACCOUNT_TYPE) as summary:
            accounts = list(self.iam.list_service_accounts(name=f"projects/{self.project}"))
            old = []
            for account in self._allowed(summary, SERVICE_ACCOUNT_TYPE, accounts, lambda a: a.email):
                try:
                    if self._is_old(account):
                        old.append(account)
                except (api_errors.GoogleAPIError, InventoryError) as exc:
                    print(f"  error: cannot age {account.email}: {exc}")
                    summary.failed += 1
            summary.old = len(old)
            # Unbind first, or deleted accounts linger as "deleted:serviceAccount:" members.
            if old and not self._attempt(f"IAM bindings of {len(old)} account(s)", lambda: self._unbind(old)):
                summary.failed += len(old)
                return
            self._delete_all(
                summary,
                old,
                lambda a: f"serviceAccounts/{a.email}",
                lambda a: self.iam.delete_service_account(name=a.name),
            )

    def _is_old(self, account) -> bool:
        """Accounts have no creation time: an account is old when it has
        user-managed keys and all of them are. Keyless accounts are kept, since
        they may belong to a test that is still setting up."""
        keys = self.iam.list_service_account_keys(name=account.name, key_types=[USER_MANAGED_KEY]).keys
        created = [key.valid_after_time.timestamp() for key in keys]
        if any(timestamp <= 0 for timestamp in created):
            raise InventoryError("key without valid_after_time")
        return bool(created) and max(created) <= self.cutoff

    def _unbind(self, accounts: list) -> None:
        """Remove the accounts from every project IAM binding in one update."""
        members = {f"serviceAccount:{account.email}" for account in accounts}
        resource = f"projects/{self.project}"
        policy = self.projects.get_iam_policy(resource=resource)
        if not any(members.intersection(binding.members) for binding in policy.bindings):
            return
        for index in reversed(range(len(policy.bindings))):
            binding = policy.bindings[index]
            remaining = [member for member in binding.members if member not in members]
            del binding.members[:]
            binding.members.extend(remaining)
            if not remaining:
                del policy.bindings[index]
        self.projects.set_iam_policy(request={"resource": resource, "policy": policy})


def default_credentials():
    raw = os.environ.get("GCP_SERVICE_ACCOUNT_JSON_FOR_SNIPPETS_TESTS")
    if raw:
        return google.auth.load_credentials_from_dict(json.loads(raw), scopes=SCOPES)[0]
    return google.auth.default(scopes=SCOPES)[0]


def main() -> int:
    project = os.environ.get("MDB_GKE_PROJECT", "")
    hours = os.environ.get("AGE_THRESHOLD_HOURS", "24")
    dry_run = os.environ.get("DRY_RUN", "false").lower()
    for problem, message in (
        (not project, "MDB_GKE_PROJECT is required"),
        (not hours.isdigit() or int(hours) < 1, f"AGE_THRESHOLD_HOURS must be a positive integer, got {hours!r}"),
        (dry_run not in ("true", "false"), f"DRY_RUN must be 'true' or 'false', got {dry_run!r}"),
    ):
        if problem:
            print(f"ERROR: {message}", file=sys.stderr)
            return 1
    credentials = default_credentials()
    reaper = Reaper(
        project,
        int(hours),
        dry_run == "true",
        api=Api(AuthorizedSession(credentials)),
        assets=asset_v1.AssetServiceClient(credentials=credentials),
        iam=iam_admin_v1.IAMClient(credentials=credentials),
        projects=resourcemanager_v3.ProjectsClient(credentials=credentials),
    )
    reaper.run()
    return 1 if reaper.failed else 0


if __name__ == "__main__":
    sys.exit(main())
