"""Shared helpers for the operator-managed certificate rotation tests of sharded and multi-cluster resources.

The generic rotation helpers (CA rotation, bundle widening and pruning) in tests/opsmanager/managedcert_helper.py
read the operator's cluster only. A multi-cluster resource has its pods, and so the trust bundles and the -pem
secrets the pods mount, in the member clusters, so the helpers here read every member cluster and check the
operator's copies match what it publishes centrally. The automation config, status and prune helpers work for
any MongoDB or MongoDBMultiCluster resource, so the single-cluster sharded rotation test uses them too.
"""

import json
from datetime import datetime
from typing import Dict, List, Optional

from kubernetes import client
from kubetester import list_matching_pods, read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from tests.multicluster.conftest import create_service_entries_objects
from tests.opsmanager.managedcert_helper import ca_bundle_snapshot
from tests.tls.managedcert_helper import (
    PRUNE_PREVIOUS_CA_ANNOTATION,
    assert_automation_config_names_files,
    read_certificate_status,
    wait_for_certificate_renewals,
)

SERVER = "server"
CLIENT = "client"

# The server CA bundle is also published under this fixed key, for applications. The client bundle only has the
# hash-named key, which ca_bundle_snapshot falls back to.
CA_BUNDLE_KEY = "ca-pem"

# The annotation the operator puts on a MongoDBMultiCluster while one of its member clusters fails the health check.
FAILED_CLUSTERS_ANNOTATION = "failedClusters"


def ca_certificate_name(resource_name: str, category: str) -> str:
    """The operator's self-signed CA Certificate (and its secret) of a category."""
    return f"{resource_name}-{category}-ca"


def ca_bundle_name(resource_name: str, category: str) -> str:
    """The trust bundle ConfigMap of a category, one per resource whatever the number of components."""
    return f"{resource_name}-managed-{category}-ca-bundle"


def bundle_snapshot(namespace: str, resource_name: str, category: str) -> dict:
    """latestHash, previousHash, certificate fingerprints and published-at of a category's bundle, centrally."""
    return ca_bundle_snapshot(namespace, ca_bundle_name(resource_name, category), CA_BUNDLE_KEY)


def read_bundle_data(namespace: str, name: str, api_client: Optional[client.ApiClient] = None) -> Dict[str, str]:
    return client.CoreV1Api(api_client=api_client).read_namespaced_config_map(name, namespace).data or {}


def assert_single_ca_bundle_per_category(namespace: str, resource_name: str):
    """A resource has exactly one server and one client bundle, shared by all of its components."""
    names = {
        config_map.metadata.name
        for config_map in client.CoreV1Api().list_namespaced_config_map(namespace).items
        if config_map.metadata.name.startswith(f"{resource_name}-") and config_map.metadata.name.endswith("-ca-bundle")
    }
    assert names == {
        ca_bundle_name(resource_name, SERVER),
        ca_bundle_name(resource_name, CLIENT),
    }, f"{resource_name} must have one shared bundle per category, found {sorted(names)}"


def wait_for_bundle_replicated(
    namespace: str, name: str, member_cluster_clients: List[MultiClusterClient], timeout: int = 900
) -> Dict[str, str]:
    """Waits until the listed member clusters hold exactly the bundle published centrally, and returns it.

    Every key is compared: the hash-named bundles, latestHash, previousHash and the ca-pem alias. The pods mount
    the member cluster copy, so a missing previousHash or bundle key there would break the members' roll.
    """
    result: Dict[str, Dict[str, str]] = {}

    def replicated() -> tuple[bool, str]:
        central = read_bundle_data(namespace, name)
        if not central.get("latestHash"):
            return False, f"{name} has no latestHash centrally"
        for cluster_client in member_cluster_clients:
            try:
                data = read_bundle_data(namespace, name, cluster_client.api_client)
            except client.ApiException as e:
                if e.status == 404:
                    return False, f"{name} missing in {cluster_client.cluster_name}"
                raise
            if data != central:
                return False, f"{name} in {cluster_client.cluster_name} differs from the central bundle"
        result["data"] = central
        return True, ""

    clusters = [cluster_client.cluster_name for cluster_client in member_cluster_clients]
    run_periodically(replicated, timeout=timeout, sleep_time=5, msg=f"{name} replicated to {clusters}")
    return result["data"]


def pod_uids_in_clusters(
    namespace: str, name_prefix: str, member_cluster_clients: List[MultiClusterClient]
) -> Dict[str, Dict[str, str]]:
    """Pod UIDs by pod name, per member cluster. A changed UID means the pod was recreated."""
    return {
        cluster_client.cluster_name: {
            pod.metadata.name: pod.metadata.uid
            for pod in list_matching_pods(namespace, name_prefix=name_prefix, api_client=cluster_client.api_client)
        }
        for cluster_client in member_cluster_clients
    }


def pem_latest_hashes_in_clusters(
    namespace: str, pem_secret_name: str, member_cluster_clients: List[MultiClusterClient]
) -> Dict[str, str]:
    """The latestHash of a -pem secret in every member cluster."""
    return {
        cluster_client.cluster_name: read_secret(namespace, pem_secret_name, api_client=cluster_client.api_client).get(
            "latestHash", ""
        )
        for cluster_client in member_cluster_clients
    }


def pem_latest_hash(namespace: str, pem_secret_name: str, member_cluster_clients: List[MultiClusterClient]) -> str:
    """The latestHash of a -pem secret, which every member cluster must agree on."""
    hashes = pem_latest_hashes_in_clusters(namespace, pem_secret_name, member_cluster_clients)
    values = set(hashes.values())
    assert len(values) == 1 and "" not in values, f"{pem_secret_name} differs between member clusters: {hashes}"
    return values.pop()


def wait_for_pem_secret_rolled_in_clusters(
    namespace: str,
    pem_secret_name: str,
    previous_hash: str,
    member_cluster_clients: List[MultiClusterClient],
    timeout: int = 900,
) -> str:
    """Waits until every member cluster's -pem secret names the same new certificate, and returns its hash.

    The previous certificate must stay in the secret, so the members that have not restarted yet still find
    the file the automation config named before.
    """
    result: Dict[str, str] = {}

    def rolled() -> tuple[bool, str]:
        hashes = pem_latest_hashes_in_clusters(namespace, pem_secret_name, member_cluster_clients)
        values = set(hashes.values())
        if len(values) != 1:
            return False, f"{pem_secret_name} differs between member clusters: {hashes}"
        latest = values.pop()
        if not latest or latest == previous_hash:
            return False, f"{pem_secret_name} still names {previous_hash}"
        for cluster_client in member_cluster_clients:
            secret = read_secret(namespace, pem_secret_name, api_client=cluster_client.api_client)
            if previous_hash and previous_hash not in secret:
                return False, f"{pem_secret_name} in {cluster_client.cluster_name} dropped the previous certificate"
        result["hash"] = latest
        return True, ""

    run_periodically(
        rolled, timeout=timeout, sleep_time=5, msg=f"{pem_secret_name} rolled to a new certificate in every cluster"
    )
    return result["hash"]


def wait_for_leaf_signed_by(
    namespace: str, secret_name: str, previous_leaf: str, ca_pem: str, timeout: int = 1800
) -> str:
    """Waits until a certificate secret holds a new certificate that ca_pem verifies, and returns it."""
    result: Dict[str, str] = {}

    def reissued() -> bool:
        leaf = read_secret(namespace, secret_name).get("tls.crt", "")
        if not leaf.strip() or leaf == previous_leaf or not cert_signed_by_bundle(leaf, ca_pem):
            return False
        result["leaf"] = leaf
        return True

    run_periodically(reissued, timeout=timeout, sleep_time=5, msg=f"{secret_name} reissued on the expected CA")
    return result["leaf"]


def wait_for_certificate_revision_above(
    namespace: str, certificate_name: str, revision: int, timeout: int = 600
) -> int:
    """Waits until cert-manager has issued the Certificate again, and returns the new revision."""
    run_periodically(
        lambda: read_certificate_status(namespace, certificate_name).get("revision", 0) > revision,
        timeout=timeout,
        sleep_time=5,
        msg=f"Certificate/{certificate_name} revision above {revision}",
    )
    return read_certificate_status(namespace, certificate_name).get("revision", 0)


def user_certificate(user: MongoDBUser, namespace: str) -> tuple[str, str]:
    """The secret name and certificate of a MongoDBUser's operator-issued client certificate."""
    user.load()
    secret_name = user["status"]["clientCertificate"]["secretName"]
    return secret_name, read_secret(namespace, secret_name)["tls.crt"]


def leaf_certificates(namespace: str, secret_names: List[str]) -> Dict[str, str]:
    """The current certificate of each leaf secret, issued centrally."""
    return {name: read_secret(namespace, name)["tls.crt"] for name in secret_names}


def wait_for_automation_config_ca_files(
    resource: MongoDB, server_hash: str, client_hash: Optional[str] = None, timeout: int = 900
) -> dict:
    """Waits until the automation config names the given trust bundles, and returns it.

    The operator names the CA files after the bundle hash, so a changed bundle moves tls.CAFilePath (and
    tls.clusterCAFilePath for the client bundle) and the agents restart the members onto the new file. The
    setting is project wide, so this covers every component of a sharded cluster; a process level CAFile, if
    one is set, must not keep naming an older bundle.
    """
    result: dict = {}

    def names_bundles() -> tuple[bool, str]:
        automation_config = resource.get_automation_config_tester().automation_config
        tls = automation_config.get("tls") or {}
        if not str(tls.get("CAFilePath", "")).endswith(f"/{server_hash}"):
            return False, f"CAFilePath is {tls.get('CAFilePath')}"
        if client_hash and not str(tls.get("clusterCAFilePath", "")).endswith(f"/{client_hash}"):
            return False, f"clusterCAFilePath is {tls.get('clusterCAFilePath')}"
        for process in automation_config.get("processes") or []:
            process_tls = ((process.get("args2_6") or {}).get("net") or {}).get("tls") or {}
            ca_file = process_tls.get("CAFile")
            if ca_file and not ca_file.endswith(f"/{server_hash}"):
                return False, f"{process.get('name')} CAFile is {ca_file}"
            cluster_ca_file = process_tls.get("clusterCAFile")
            if client_hash and cluster_ca_file and not cluster_ca_file.endswith(f"/{client_hash}"):
                return False, f"{process.get('name')} clusterCAFile is {cluster_ca_file}"
        result["automation_config"] = automation_config
        return True, ""

    run_periodically(
        names_bundles,
        timeout=timeout,
        sleep_time=5,
        msg=f"{resource.name} automation config to name the server CA bundle {server_hash}"
        + (f" and the client CA bundle {client_hash}" if client_hash else ""),
    )
    return result["automation_config"]


def wait_for_automation_config_names_files(
    agent_hash: str, member_hashes: Dict[str, str], clusterfile_hashes: Dict[str, str], timeout: int = 900
):
    """Waits until the automation config names the current certificate files of every component.

    member_hashes and clusterfile_hashes map the StatefulSet name prefix of a component to the -pem hash of its
    certificate, see assert_automation_config_names_files.
    """

    def names_files() -> bool:
        try:
            assert_automation_config_names_files(agent_hash, member_hashes, clusterfile_hashes)
            return True
        except (AssertionError, KeyError):
            return False

    run_periodically(
        names_files, timeout=timeout, sleep_time=5, msg="automation config to name the current certificate files"
    )


def wait_for_prune_annotation_removed(resource: MongoDB, timeout: int = 900):
    """Waits until the operator has applied the prune-previous-ca annotation and removed it."""

    def removed() -> bool:
        resource.load()
        return PRUNE_PREVIOUS_CA_ANNOTATION not in (resource["metadata"].get("annotations") or {})

    run_periodically(
        removed, timeout=timeout, sleep_time=5, msg=f"{PRUNE_PREVIOUS_CA_ANNOTATION} removed from {resource.name}"
    )


def _as_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def assert_certificate_renewals_reported(
    resource: MongoDB, namespace: str, server_certificates: List[str], client_certificates: List[str]
):
    """status.certificateRenewals names, per category, the certificate cert-manager renews first.

    The reported renewalTime is cert-manager's own schedule of the named Certificate, and no other
    certificate of the category renews earlier.
    """
    renewals = wait_for_certificate_renewals(resource)
    for category, candidates in ((SERVER, server_certificates), (CLIENT, client_certificates)):
        renewal = renewals.get(category)
        assert renewal is not None, f"status.certificateRenewals.{category} must be reported"
        assert renewal["name"] in candidates, f"{category} renewal names {renewal['name']}, not one of {candidates}"
        assert renewal.get("notAfter"), f"the {category} renewal must carry the certificate expiry"
        assert renewal.get("renewalTime"), f"the {category} renewal must carry the next renewal time"

        scheduled = {}
        for name in candidates:
            renewal_time = read_certificate_status(namespace, name).get("renewalTime")
            assert renewal_time, f"cert-manager must have scheduled the renewal of {name}"
            scheduled[name] = _as_time(renewal_time)
        assert _as_time(renewal["renewalTime"]) == scheduled[renewal["name"]]
        assert _as_time(renewal["renewalTime"]) == min(
            scheduled.values()
        ), f"the {category} renewal must be the soonest of {scheduled}"


def allow_operator_to_reach(namespace: str, central_cluster_client: client.ApiClient, cluster_names: List[str]):
    """Lets the operator reach the API servers of the given member clusters only.

    The operator pod runs in the Istio mesh with a registry-only egress policy, so the ServiceEntries decide which
    member clusters it can reach. The member clusters keep running and the test runner still reaches them.
    """
    for service_entry in create_service_entries_objects(namespace, central_cluster_client, cluster_names):
        service_entry.update()


def failed_cluster_names(resource: MongoDB) -> List[str]:
    """The member clusters the operator has marked as failed on a MongoDBMultiCluster."""
    resource.load()
    raw = (resource["metadata"].get("annotations") or {}).get(FAILED_CLUSTERS_ANNOTATION)
    if not raw:
        return []
    return [failed_cluster["ClusterName"] for failed_cluster in json.loads(raw)]


def wait_for_failed_cluster(resource: MongoDB, cluster_name: str, *, failed: bool, timeout: int = 600):
    """Waits until the operator has (or no longer has) cluster_name in the failedClusters annotation."""
    run_periodically(
        lambda: (cluster_name in failed_cluster_names(resource)) == failed,
        timeout=timeout,
        sleep_time=5,
        msg=f"{cluster_name} {'marked' if failed else 'no longer marked'} as failed on {resource.name}",
    )
