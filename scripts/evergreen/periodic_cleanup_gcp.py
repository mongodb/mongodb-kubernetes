"""Age-based garbage collector for the GKE code-snippet test project (KUBE-268).

Deletes every resource older than AGE_THRESHOLD_HOURS, except the auditable
DENYLISTS below. Age is the only ownership filter: the project is used
exclusively by ephemeral e2e tests, and an in-flight run's resources are
younger than the threshold (deleting them is what broke the KUBE-268 run).
Clusters go first; delete failures are tolerated and retried on the next run;
malformed inventories abort their class instead of deleting from a partial
view.

GCP access uses the typed google-cloud clients. Two surfaces they do not cover
are handled over REST with the same credentials: the legacy httpHealthChecks
collection (absent from google-cloud-compute), and Cloud DNS record deletion
(google-cloud-dns cannot delete records, and a zone that still contains
records cannot be deleted).

Run through the project venv (created by the periodic_teardown setup):
  scripts/dev/run_python.sh scripts/evergreen/periodic_cleanup_gcp.py

Auth: the GCP_SERVICE_ACCOUNT_JSON_FOR_SNIPPETS_TESTS expansion in Evergreen,
Application Default Credentials locally.

Env: MDB_GKE_PROJECT (required), AGE_THRESHOLD_HOURS (24), DRY_RUN (false).
"""

import json
import os
import re
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from datetime import datetime
from typing import Callable, Optional

import google.auth
from google.api_core import exceptions as api_errors
from google.auth.transport.requests import AuthorizedSession
from google.cloud import compute_v1, container_v1, iam_admin_v1, resourcemanager_v3

SCOPES = ["https://www.googleapis.com/auth/cloud-platform"]
COMPUTE_API = "https://compute.googleapis.com/compute/v1"
DNS_API = "https://dns.googleapis.com/dns/v1"
DNS_ADMIN_ROLE = "roles/dns.admin"
CLUSTER_POLL_SECONDS = 15
USER_MANAGED_KEY = iam_admin_v1.ListServiceAccountKeysRequest.KeyType.USER_MANAGED

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


class InventoryError(ValueError):
    """A list response does not match the expected shape."""


@dataclass(frozen=True)
class ComputeKind:
    """One API collection backing a reaped compute class.

    A class like addresses has both a global and a regional collection; each
    is listed and deleted through its own client.
    """

    client: type
    collection: str  # attribute holding the resources in the list response
    delete_arg: str  # keyword argument naming the resource in delete()
    scope: str = "global"  # "global", "regional" or "zonal"


# The tests only create network load balancers (target pools / forwarding
# rules), so global-only collections are listed where the regional variant is
# never populated (backend services, proxy resources, certificates).
COMPUTE_CLASSES: dict[str, tuple[ComputeKind, ...]] = {
    "forwarding-rules": (
        ComputeKind(compute_v1.GlobalForwardingRulesClient, "forwarding_rules", "forwarding_rule"),
        ComputeKind(compute_v1.ForwardingRulesClient, "forwarding_rules", "forwarding_rule", "regional"),
    ),
    "target-pools": (ComputeKind(compute_v1.TargetPoolsClient, "target_pools", "target_pool", "regional"),),
    "backend-services": (ComputeKind(compute_v1.BackendServicesClient, "backend_services", "backend_service"),),
    "health-checks": (ComputeKind(compute_v1.HealthChecksClient, "health_checks", "health_check"),),
    "target-https-proxies": (
        ComputeKind(compute_v1.TargetHttpsProxiesClient, "target_https_proxies", "target_https_proxy"),
    ),
    "url-maps": (ComputeKind(compute_v1.UrlMapsClient, "url_maps", "url_map"),),
    "ssl-certificates": (ComputeKind(compute_v1.SslCertificatesClient, "ssl_certificates", "ssl_certificate"),),
    "addresses": (
        ComputeKind(compute_v1.GlobalAddressesClient, "addresses", "address"),
        ComputeKind(compute_v1.AddressesClient, "addresses", "address", "regional"),
    ),
    "network-endpoint-groups": (
        ComputeKind(
            compute_v1.NetworkEndpointGroupsClient, "network_endpoint_groups", "network_endpoint_group", "zonal"
        ),
    ),
    "firewall-rules": (ComputeKind(compute_v1.FirewallsClient, "firewalls", "firewall"),),
    "disks": (ComputeKind(compute_v1.DisksClient, "disks", "disk", "zonal"),),
}


@dataclass(frozen=True)
class Item:
    kind: ComputeKind
    name: str
    created: float
    scope: str = ""  # "zones/<zone>", "regions/<region>" or "" for global


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


def parse_time(value: str) -> Optional[float]:
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return None


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


def default_credentials():
    raw = os.environ.get("GCP_SERVICE_ACCOUNT_JSON_FOR_SNIPPETS_TESTS")
    if raw:
        credentials, _ = google.auth.load_credentials_from_dict(json.loads(raw), scopes=SCOPES)
        return credentials
    credentials, _ = google.auth.default(scopes=SCOPES)
    return credentials


class RestClient:
    """AuthorizedSession wrapper for the API surfaces the typed clients miss."""

    def __init__(self, credentials):
        self._session = AuthorizedSession(credentials)

    def get(self, url: str, key: str) -> list[dict]:
        """GET a paginated collection, returning concatenated ``key`` items."""
        items, page_token = [], None
        while True:
            params = {"pageToken": page_token} if page_token else None
            response = self._session.get(url, params=params)
            if not response.ok:
                raise api_errors.from_http_response(response)
            data = response.json()
            items.extend(data.get(key, []))
            page_token = data.get("nextPageToken")
            if not page_token:
                return items

    def post(self, url: str, body: dict) -> dict:
        response = self._session.post(url, json=body)
        if not response.ok:
            raise api_errors.from_http_response(response)
        return response.json()

    def delete(self, url: str) -> None:
        response = self._session.delete(url)
        if not response.ok:
            raise api_errors.from_http_response(response)


class Clients:
    """Lazily created, cached GCP API clients plus the REST helper."""

    def __init__(self, credentials):
        self._credentials = credentials
        self._cache: dict[type, object] = {}
        self.rest = RestClient(credentials)

    def get(self, client_class: type):
        if client_class not in self._cache:
            self._cache[client_class] = client_class(credentials=self._credentials)
        return self._cache[client_class]


class Reaper:
    def __init__(self, project: str, age_threshold_hours: int, dry_run: bool, clients: Clients):
        self.project = project
        self.age_threshold_hours = age_threshold_hours
        self.dry_run = dry_run
        self.clients = clients
        self.cutoff = time.time() - age_threshold_hours * 3600
        self.failed = False

    def _start(self, summary: Summary) -> None:
        print(f"\n=== {summary.resource} (threshold: {self.age_threshold_hours}h) ===")

    def _finish(self, summary: Summary) -> None:
        print(summary.footer(self.age_threshold_hours, self.dry_run))

    def _record(self, summary: Summary, ok: bool) -> None:
        if ok:
            summary.deleted += 1
        else:
            summary.failed += 1
            self.failed = True

    def _mutate(self, description: str, call: Callable[[], object]) -> bool:
        """Run a mutating API call; True on success or when it is already gone."""
        if self.dry_run:
            print(f"  dry-run: delete {description}")
            return True
        print(f"  running: delete {description}")
        try:
            call()
            return True
        except api_errors.NotFound:
            return True
        except api_errors.GoogleAPIError as exc:
            print(f"  error: {exc}")
            return False

    def _list_compute(self, resource: str) -> Optional[list[Item]]:
        """List every collection of a compute class, failing closed on any error."""
        items: list[Item] = []
        for kind in COMPUTE_CLASSES[resource]:
            try:
                client = self.clients.get(kind.client)
                if kind.scope == "global":
                    items.extend(self._item(kind, raw) for raw in client.list(project=self.project))
                else:
                    for scope, scoped in client.aggregated_list(project=self.project):
                        items.extend(self._item(kind, raw, scope) for raw in getattr(scoped, kind.collection))
            except (api_errors.GoogleAPIError, InventoryError) as exc:
                print(f"ERROR: failed to list {resource}: {exc}")
                self.failed = True
                return None
        return items

    def _item(self, kind: ComputeKind, raw, scope: str = "") -> Item:
        created = parse_time(raw.creation_timestamp)
        if created is None:
            raise InventoryError(f"{kind.collection} {raw.name}: bad creation timestamp {raw.creation_timestamp!r}")
        return Item(kind, raw.name, created, scope)

    def _stale(self, items: list[Item]) -> list[Item]:
        return [item for item in items if item.created <= self.cutoff]

    def _delete_item(self, resource: str, item: Item) -> bool:
        client = self.clients.get(item.kind.client)
        kwargs = {"project": self.project, item.kind.delete_arg: item.name}
        if item.scope.startswith("zones/"):
            kwargs["zone"] = item.scope.removeprefix("zones/")
        elif item.scope.startswith("regions/"):
            kwargs["region"] = item.scope.removeprefix("regions/")
        return self._mutate(f"{resource}/{item.name}", lambda: client.delete(**kwargs))

    def reap_compute(self, resource: str) -> None:
        summary = Summary(resource)
        self._start(summary)
        items = self._list_compute(resource)
        if items is not None:
            stale = self._stale(items)
            summary.listed = len(items)
            summary.old = len(stale)
            for item in stale:
                self._record(summary, self._delete_item(resource, item))
        self._finish(summary)

    def reap_firewall_rules(self) -> None:
        summary = Summary("firewall-rules")
        self._start(summary)
        items = self._list_compute("firewall-rules")
        if items is not None:
            stale = self._stale(items)
            summary.listed = len(items)
            # summary.old includes denylisted rules; skipped reports the difference.
            summary.old = len(stale)
            candidates = [item for item in stale if not is_denylisted("firewall-rules", item.name)]
            summary.skipped = len(stale) - len(candidates)
            if summary.skipped:
                print(f"  skipped {summary.skipped} denylisted rule(s)")
            for item in candidates:
                self._record(summary, self._delete_item("firewall-rules", item))
        self._finish(summary)

    def reap_clusters(self) -> None:
        summary = Summary("clusters")
        self._start(summary)
        client = self.clients.get(container_v1.ClusterManagerClient)
        try:
            clusters = list(client.list_clusters(parent=f"projects/{self.project}/locations/-").clusters)
            stale = []
            for cluster in clusters:
                created = parse_time(cluster.create_time)
                if created is None:
                    raise InventoryError(f"cluster {cluster.name}: bad create_time {cluster.create_time!r}")
                if created <= self.cutoff:
                    stale.append(cluster)
        except (api_errors.GoogleAPIError, InventoryError) as exc:
            print(f"ERROR: failed to list clusters: {exc}")
            self.failed = True
            self._finish(summary)
            return
        summary.listed = len(clusters)
        summary.old = len(stale)
        if stale:
            # Cluster deletions take minutes; run them in parallel so the
            # dependent load-balancer resources are released together.
            with ThreadPoolExecutor() as pool:
                futures = [pool.submit(self._delete_cluster, cluster) for cluster in stale]
                for future in as_completed(futures):
                    self._record(summary, future.result())
        self._finish(summary)

    def _delete_cluster(self, cluster) -> bool:
        client = self.clients.get(container_v1.ClusterManagerClient)
        name = f"projects/{self.project}/locations/{cluster.location}/clusters/{cluster.name}"
        if self.dry_run:
            print(f"  dry-run: delete clusters/{cluster.name}")
            return True
        print(f"  running: delete clusters/{cluster.name}")
        try:
            operation = client.delete_cluster(name=name)
            while operation.status != container_v1.Operation.Status.DONE:
                time.sleep(CLUSTER_POLL_SECONDS)
                operation = client.get_operation(name=operation.name)
            if operation.error.code:
                print(f"  error: {operation.error.message}")
                return False
            return True
        except api_errors.NotFound:
            return True
        except api_errors.GoogleAPIError as exc:
            print(f"  error: {exc}")
            return False

    def reap_http_health_checks(self) -> None:
        """Legacy global collection handled over REST: google-cloud-compute
        does not generate an httpHealthChecks client."""
        summary = Summary("http-health-checks")
        self._start(summary)
        url = f"{COMPUTE_API}/projects/{self.project}/global/httpHealthChecks"
        try:
            items = []
            for raw in self.clients.rest.get(url, "items"):
                created = parse_time(raw.get("creationTimestamp", ""))
                if created is None:
                    raise InventoryError(f"httpHealthCheck {raw.get('name')}: bad creationTimestamp")
                items.append((raw["name"], created))
        except (api_errors.GoogleAPIError, InventoryError) as exc:
            print(f"ERROR: failed to list http-health-checks: {exc}")
            self.failed = True
            self._finish(summary)
            return
        summary.listed = len(items)
        stale = [(name, created) for name, created in items if created <= self.cutoff]
        summary.old = len(stale)
        for name, _ in stale:
            self._record(
                summary,
                self._mutate(f"http-health-checks/{name}", lambda name=name: self.clients.rest.delete(f"{url}/{name}")),
            )
        self._finish(summary)

    def reap_dns_zones(self) -> None:
        summary = Summary("dns-managed-zones")
        self._start(summary)
        url = f"{DNS_API}/projects/{self.project}/managedZones"
        try:
            zones = self.clients.rest.get(url, "managedZones")
            stale = []
            for zone in zones:
                created = parse_time(zone.get("creationTime", ""))
                if created is None:
                    raise InventoryError(f"managed zone {zone.get('name')}: bad creationTime")
                if created <= self.cutoff:
                    stale.append(zone)
        except (api_errors.GoogleAPIError, InventoryError) as exc:
            print(f"ERROR: failed to list dns-managed-zones: {exc}")
            self.failed = True
            self._finish(summary)
            return
        summary.listed = len(zones)
        summary.old = len(stale)
        for zone in stale:
            self._record(summary, self._delete_zone(zone["name"]))
        self._finish(summary)

    def _delete_zone(self, zone: str) -> bool:
        """Delete a zone together with its records: Cloud DNS refuses to delete
        a zone that still contains resource records, and google-cloud-dns
        cannot delete records, so the records go through a change set."""
        records_url = f"{DNS_API}/projects/{self.project}/managedZones/{zone}/rrsets"
        try:
            records = self.clients.rest.get(records_url, "rrsets")
        except api_errors.GoogleAPIError as exc:
            print(f"ERROR: failed to list records of {zone}: {exc}")
            return False
        deletions = [record for record in records if record.get("type") not in ("NS", "SOA")]
        if deletions:
            changes_url = f"{DNS_API}/projects/{self.project}/managedZones/{zone}/changes"
            if not self._mutate(
                f"dns records in {zone} ({len(deletions)})",
                lambda: self.clients.rest.post(changes_url, {"deletions": deletions}),
            ):
                return False
        return self._mutate(
            f"dns-managed-zones/{zone}",
            lambda: self.clients.rest.delete(f"{DNS_API}/projects/{self.project}/managedZones/{zone}"),
        )

    def reap_service_accounts(self) -> None:
        summary = Summary("service-accounts")
        self._start(summary)
        client = self.clients.get(iam_admin_v1.IAMClient)
        try:
            accounts = list(client.list_service_accounts(name=f"projects/{self.project}"))
        except api_errors.GoogleAPIError as exc:
            print(f"ERROR: failed to list service-accounts: {exc}")
            self.failed = True
            self._finish(summary)
            return
        summary.listed = len(accounts)
        for account in accounts:
            if is_denylisted("service-accounts", account.email):
                summary.skipped += 1
                continue
            # Service accounts have no creation timestamp: age is established
            # by requiring at least one user-managed key and all keys being
            # old. An account without user-managed keys has no age signal and
            # is skipped (an interrupted ra-09 run can leave one behind, but
            # deleting it blindly could hit a test mid-setup).
            try:
                keys = client.list_service_account_keys(name=account.name, key_types=[USER_MANAGED_KEY]).keys
                valid_after = [key.valid_after_time.timestamp() for key in keys]
                if any(valid_after_time <= 0 for valid_after_time in valid_after):
                    raise InventoryError(f"service account {account.email}: key without valid_after_time")
            except (api_errors.GoogleAPIError, InventoryError) as exc:
                print(f"ERROR: failed to age {account.email}: {exc}")
                summary.failed += 1
                self.failed = True
                continue
            if not valid_after or any(valid_after_time > self.cutoff for valid_after_time in valid_after):
                continue
            summary.old += 1
            if self._remove_dns_admin_binding(account.email):
                self._record(
                    summary,
                    self._mutate(
                        f"service-accounts/{account.email}", lambda: client.delete_service_account(name=account.name)
                    ),
                )
            else:
                summary.failed += 1
                self.failed = True
        if summary.skipped:
            print(f"  skipped {summary.skipped} denylisted account(s)")
        self._finish(summary)

    def _remove_dns_admin_binding(self, email: str) -> bool:
        """Remove the account's project-level roles/dns.admin binding so a
        deleted account does not linger as a deleted:serviceAccount member."""
        client = self.clients.get(resourcemanager_v3.ProjectsClient)
        resource = f"projects/{self.project}"

        def remove() -> None:
            policy = client.get_iam_policy(resource=resource)
            member = f"serviceAccount:{email}"
            changed = False
            for binding in policy.bindings:
                if binding.role == DNS_ADMIN_ROLE and member in binding.members:
                    binding.members.remove(member)
                    changed = True
            if not changed:
                return
            kept = [binding for binding in policy.bindings if len(binding.members) > 0]
            del policy.bindings[:]
            policy.bindings.extend(kept)
            client.set_iam_policy(resource=resource, policy=policy)

        return self._mutate(f"iam-binding serviceAccount:{email}", remove)

    def run(self) -> None:
        if self.dry_run:
            print("=== DRY RUN: no delete or IAM mutation commands will execute ===")
        self.reap_clusters()
        self.reap_compute("forwarding-rules")
        self.reap_compute("target-pools")
        self.reap_compute("backend-services")
        self.reap_http_health_checks()
        self.reap_compute("health-checks")
        self.reap_compute("target-https-proxies")
        self.reap_compute("url-maps")
        self.reap_compute("ssl-certificates")
        self.reap_compute("addresses")
        self.reap_compute("network-endpoint-groups")
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
        dry_run = bool_env("DRY_RUN", False)
    except ValueError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    reaper = Reaper(project, age_threshold_hours, dry_run, Clients(default_credentials()))
    reaper.run()
    return 1 if reaper.failed else 0


if __name__ == "__main__":
    sys.exit(main())
