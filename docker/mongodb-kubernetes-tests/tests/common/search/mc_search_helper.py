from __future__ import annotations

import json
from typing import Callable, Dict, List, Mapping, Optional, Tuple

import kubernetes
import requests
import yaml
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, decode_secret, read_secret
from kubetester.certs import create_tls_certs
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from tests import test_logger
from tests.common.multicluster.multicluster_utils import assert_deployment_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper

logger = test_logger.get_test_logger(__name__)


# ---------------------------------------------------------------------------
# Owner labels — stamped on every per-cluster resource by
# pkg/handler/labels_search.go and used for cross-cluster watch routing by
# MapMemberClusterObjectToSearch. Cross-cluster owner refs do not GC, so
# labels are the actual provenance link.
# ---------------------------------------------------------------------------

SEARCH_OWNER_NAME_LABEL = "mongodb.com/search-name"
SEARCH_OWNER_NAMESPACE_LABEL = "mongodb.com/search-namespace"
SEARCH_CLUSTER_NAME_LABEL = "mongodb.com/cluster-name"


def _assert_search_owner_labels(
    obj_labels: Mapping[str, str], cluster_name: str, where: str, mdbs_resource_name: str
) -> None:
    """Strict ``mongodb.com/{search-name,search-namespace,cluster-name}`` check."""
    assert (
        obj_labels.get(SEARCH_OWNER_NAME_LABEL) == mdbs_resource_name
    ), f"{where}: missing/wrong {SEARCH_OWNER_NAME_LABEL!r}; got {obj_labels.get(SEARCH_OWNER_NAME_LABEL)!r}"
    # The operator stamps the search's namespace, not the central namespace —
    # equals the namespace under test in our e2e harness.
    assert obj_labels.get(SEARCH_OWNER_NAMESPACE_LABEL), f"{where}: missing {SEARCH_OWNER_NAMESPACE_LABEL!r}"
    assert obj_labels.get(SEARCH_CLUSTER_NAME_LABEL) == cluster_name, (
        f"{where}: missing/wrong {SEARCH_CLUSTER_NAME_LABEL!r}; got "
        f"{obj_labels.get(SEARCH_CLUSTER_NAME_LABEL)!r}, want {cluster_name!r}"
    )


def _resolve_cluster_index(helper: Optional[MCSearchDeploymentHelper], mcc: MultiClusterClient) -> int:
    if helper is not None:
        return helper.cluster_index(mcc.cluster_name)
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


# ---------------------------------------------------------------------------
# Cert + secret bring-up.
# ---------------------------------------------------------------------------


def create_mc_lb_certificates(
    *,
    namespace: str,
    issuer: str,
    mdbs_resource_name: str,
    tls_cert_prefix: str,
    helper: MCSearchDeploymentHelper,
    envoy_lb_replicas: int,
) -> None:
    """Per-cluster LB server + client certs, each named to match that cluster's Envoy
    Deployment and carrying SANs for every cluster's proxy-svc FQDN."""
    server_domains = [
        f"{mdbs_resource_name}-search-{helper.cluster_index(name)}-proxy-svc.{namespace}.svc.cluster.local"
        for name in helper.member_cluster_names()
    ]

    for name in helper.member_cluster_names():
        ci = helper.cluster_index(name)
        deployment_name = search_resource_names.lb_deployment_name(mdbs_resource_name, ci)
        lb_server_cert_name = search_resource_names.lb_server_cert_name(mdbs_resource_name, tls_cert_prefix, ci)
        lb_client_cert_name = search_resource_names.lb_client_cert_name(mdbs_resource_name, tls_cert_prefix, ci)

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=deployment_name,
            replicas=envoy_lb_replicas,
            service_name=deployment_name,
            additional_domains=server_domains,
            secret_name=lb_server_cert_name,
        )
        logger.info(f"LB server certificate created with SANs={server_domains}: {lb_server_cert_name}")

        create_tls_certs(
            issuer=issuer,
            namespace=namespace,
            resource_name=f"{deployment_name}-client",
            replicas=1,
            service_name=deployment_name,
            additional_domains=[f"*.{namespace}.svc.cluster.local"],
            secret_name=lb_client_cert_name,
        )
        logger.info(f"LB client certificate created: {lb_client_cert_name}")


def create_mc_mongot_tls_cert(
    *,
    namespace: str,
    issuer: str,
    mdbs_resource_name: str,
    tls_cert_prefix: str,
    helper: MCSearchDeploymentHelper,
) -> None:
    """mongot TLS cert with SANs for every per-cluster mongot Service +
    every per-cluster proxy-svc FQDN.

    Without per-cluster SANs, mongot pods fail their TLS handshake.
    """
    secret_name = search_resource_names.mongot_tls_cert_name(mdbs_resource_name, tls_cert_prefix)
    additional_domains: List[str] = []
    for name in helper.member_cluster_names():
        idx = helper.cluster_index(name)
        additional_domains.append(f"{mdbs_resource_name}-search-{idx}-svc.{namespace}.svc.cluster.local")
        additional_domains.append(f"{mdbs_resource_name}-search-{idx}-proxy-svc.{namespace}.svc.cluster.local")

    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.mongot_statefulset_name(mdbs_resource_name),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    logger.info(f"mongot TLS certificate created with SANs={additional_domains}: {secret_name}")


def replicate_search_secrets_to_members(
    *,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdbs_resource_name: str,
    mdbs_tls_cert_prefix: str,
    mongot_user_name: str,
    ca_configmap_name: str,
) -> None:
    """Replicate TLS Secrets, mongot password, and CA ConfigMap to every member cluster.

    MCK does not replicate Secrets in production — that's the customer's responsibility.
    Without this step, mongot pods in member clusters stay PodInitializing forever.
    """
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, targets: List[MultiClusterClient]) -> None:
        source = central_core.read_namespaced_secret(name=secret_name, namespace=namespace)
        data = decode_secret(source.data)
        for mcc in targets:
            create_or_update_secret(
                namespace, secret_name, data, type=source.type or "Opaque", api_client=mcc.api_client
            )

    # Shared Secrets — same copy to every member cluster.
    shared_secrets = [
        search_resource_names.mongot_tls_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix),
        f"{mdbs_resource_name}-{mongot_user_name}-password",
    ]
    for secret_name in shared_secrets:
        _copy(secret_name, member_cluster_clients)
        logger.info(f"replicated Secret {secret_name} to {len(member_cluster_clients)} member cluster(s)")

    # Per-cluster LB certs — each member only needs the cert matching its own Envoy.
    for mcc in member_cluster_clients:
        ci = _resolve_cluster_index(None, mcc)
        for secret_name in (
            search_resource_names.lb_server_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix, ci),
            search_resource_names.lb_client_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix, ci),
        ):
            _copy(secret_name, [mcc])
            logger.info(f"replicated per-cluster Secret {secret_name} into cluster {mcc.cluster_name}")

    # CA ConfigMap — mongot verifies the source RS TLS cert against this CA.
    source_cm = central_core.read_namespaced_config_map(name=ca_configmap_name, namespace=namespace)
    cm_data = dict(source_cm.data or {})
    for mcc in member_cluster_clients:
        create_or_update_configmap(namespace, ca_configmap_name, cm_data, api_client=mcc.api_client)
        logger.info(f"replicated CA ConfigMap {ca_configmap_name} into cluster {mcc.cluster_name}")


def replicate_sharded_search_secrets_to_members(
    *,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdb_resource_name: str,
    mdbs_resource_name: str,
    mdbs_tls_cert_prefix: str,
    shard_count: int,
    mongot_user_name: str,
) -> None:
    """Copy centrally-issued Search Secrets to each member cluster (sharded MC).

    The MongoDBSearch controller does not auto-replicate Secrets (customer's job in
    production). The mongot password goes to every member; per-cluster LB certs and
    per-(cluster, shard) mongot certs go only to their owning cluster. The CA is already
    on every member (Layer-1 ``ensure_ca_configmap``).
    """
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, targets: List[MultiClusterClient]) -> None:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in targets:
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)

    # Cluster-agnostic Secrets — same copy to every member cluster.
    _copy(f"{mdbs_resource_name}-{mongot_user_name}-password", member_cluster_clients)
    logger.info(f"replicated mongot password to {len(member_cluster_clients)} member(s)")

    # Per-cluster Secrets — LB certs + per-shard mongot certs go only to their owning cluster.
    for i, mcc in enumerate(member_cluster_clients):
        _copy(search_resource_names.lb_server_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix, i), [mcc])
        _copy(search_resource_names.lb_client_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix, i), [mcc])
        for shard_idx in range(shard_count):
            shard_name = f"{mdb_resource_name}-{shard_idx}"
            _copy(
                search_resource_names.shard_tls_cert_name(
                    mdbs_resource_name, shard_name, mdbs_tls_cert_prefix, cluster_index=i
                ),
                [mcc],
            )
        logger.info(f"replicated per-cluster Secrets to {mcc.cluster_name} (cluster_index={i})")


# ---------------------------------------------------------------------------
# Per-cluster shape verification.
# ---------------------------------------------------------------------------


def assert_mongot_sync_source_hosts(
    mcc: MultiClusterClient,
    cm_name: str,
    namespace: str,
    expected_hosts: List[str],
    cm: Optional[kubernetes.client.V1ConfigMap] = None,
) -> None:
    """Iterates every config doc key (`config.yml`, or the per-pod
    `config-leader.yml`/`config-follower.yml` pair) so the syncSource-hosts check holds in
    both single- and per-pod-config modes. Pass a pre-fetched ``cm`` to skip the GET.
    """
    if cm is None:
        cm = mcc.read_namespaced_config_map(cm_name, namespace)
    docs = {k: v for k, v in (cm.data or {}).items() if k.endswith((".yml", ".yaml"))}
    assert docs, f"[{mcc.cluster_name}] mongot CM {cm_name} has no config doc; got keys {list(cm.data or {})}"
    want = sorted(expected_hosts)
    for key, payload in docs.items():
        parsed = yaml.safe_load(payload) or {}
        got = parsed.get("syncSource", {}).get("replicaSet", {}).get("hostAndPort", [])
        assert sorted(got) == want, (
            f"[{mcc.cluster_name}] mongot CM {cm_name}[{key}]: syncSource.replicaSet.hostAndPort "
            f"{sorted(got)} != expected seed list {want}"
        )
    logger.info(f"[{mcc.cluster_name}] mongot CM {cm_name} syncSource hosts == seed list ({len(docs)} doc(s))")


def verify_per_cluster_mongot_resources(
    *,
    mdb: MongoDBMulti,
    namespace: str,
    mdbs_resource_name: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
) -> None:
    """Each cluster has its own mongot StatefulSet, Service, ConfigMap with owner labels."""
    expected_hosts = sorted(f"{svc}.{namespace}.svc.cluster.local:27017" for svc in mdb.service_names())

    for mcc in member_cluster_clients:
        idx = helper.cluster_index(mcc.cluster_name)
        sts_name = f"{mdbs_resource_name}-search-{idx}"
        svc_name = f"{mdbs_resource_name}-search-{idx}-svc"
        cm_name = search_resource_names.mongot_configmap_name_for_cluster(mdbs_resource_name, idx)
        proxy_svc_name = f"{mdbs_resource_name}-search-{idx}-proxy-svc"

        sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
        headless = mcc.read_namespaced_service(svc_name, namespace)
        proxy = mcc.read_namespaced_service(proxy_svc_name, namespace)
        cm = mcc.read_namespaced_config_map(cm_name, namespace)
        _assert_search_owner_labels(sts.metadata.labels or {}, mcc.cluster_name, f"STS {sts_name}", mdbs_resource_name)
        _assert_search_owner_labels(
            headless.metadata.labels or {}, mcc.cluster_name, f"headless Service {svc_name}", mdbs_resource_name
        )
        _assert_search_owner_labels(
            proxy.metadata.labels or {}, mcc.cluster_name, f"proxy Service {proxy_svc_name}", mdbs_resource_name
        )
        _assert_search_owner_labels(
            cm.metadata.labels or {}, mcc.cluster_name, f"mongot CM {cm_name}", mdbs_resource_name
        )

        assert_mongot_sync_source_hosts(mcc, cm_name, namespace, expected_hosts, cm=cm)

        logger.info(
            f"per-cluster mongot resources verified in cluster {mcc.cluster_name} "
            f"(idx={idx}): {sts_name}, {svc_name}, {cm_name}, {proxy_svc_name}"
        )


def verify_per_cluster_envoy_deployment(
    *,
    namespace: str,
    mdbs_resource_name: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
) -> None:
    """Every cluster's Envoy Deployment + ConfigMap exist, fully ready, owner-labelled."""
    for mcc in member_cluster_clients:
        cluster_idx = helper.cluster_index(mcc.cluster_name)
        envoy_deployment_name = search_resource_names.lb_deployment_name(mdbs_resource_name, cluster_idx)
        envoy_cm_name = search_resource_names.lb_configmap_name(mdbs_resource_name, cluster_idx)
        apps = mcc.apps_v1_api()
        assert_deployment_ready_in_cluster(apps, name=envoy_deployment_name, namespace=namespace)
        envoy_deploy = apps.read_namespaced_deployment(name=envoy_deployment_name, namespace=namespace)
        envoy_cm = mcc.read_namespaced_config_map(envoy_cm_name, namespace)
        _assert_search_owner_labels(
            envoy_deploy.metadata.labels or {},
            mcc.cluster_name,
            f"Envoy Deployment {envoy_deployment_name}",
            mdbs_resource_name,
        )
        _assert_search_owner_labels(
            envoy_cm.metadata.labels or {}, mcc.cluster_name, f"Envoy CM {envoy_cm_name}", mdbs_resource_name
        )

        logger.info(f"Envoy Deployment {envoy_deployment_name} ready in cluster {mcc.cluster_name} (idx={cluster_idx})")


def verify_per_cluster_envoy_sni(
    *,
    namespace: str,
    mdbs_resource_name: str,
    member_cluster_clients: List[MultiClusterClient],
    helper: Optional[MCSearchDeploymentHelper] = None,
    expected_upstreams_by_idx: Optional[Dict[int, List[str]]] = None,
) -> None:
    """Each per-cluster Envoy ConfigMap routes ONLY to its own cluster's slice — pass
    ``helper`` (SNI-FQDN anchor) and/or ``expected_upstreams_by_idx`` (cds.json upstream
    anchor; deterministic regardless of the templated SNI externalHostname), then leak-scan
    for any foreign ``-search-{idx}-`` segment.

    The SNI-FQDN anchor is helper-only: simulated-MC callers omit ``helper`` (indexes come
    from ``mcc.cluster_index``) because simulated sharded SNI carries per-shard FQDNs, not
    the per-cluster proxy-svc FQDN this anchor checks.

    Requires a TLS-enabled managed-LB config: Envoy emits SNI server_names only under TLS,
    and the SNI assertions hard-fail without them.
    """
    assert helper is not None or expected_upstreams_by_idx is not None, (
        "verify_per_cluster_envoy_sni needs a positive anchor: pass helper (SNI FQDN anchor) "
        "and/or expected_upstreams_by_idx (cds.json upstream anchor)"
    )

    indices = [_resolve_cluster_index(helper, mcc) for mcc in member_cluster_clients]

    for mcc in member_cluster_clients:
        cluster_idx = _resolve_cluster_index(helper, mcc)
        cm_name = search_resource_names.lb_configmap_name(mdbs_resource_name, cluster_idx)

        cm = mcc.core_v1_api().read_namespaced_config_map(name=cm_name, namespace=namespace)
        data = cm.data or {}
        lds_json = data.get("lds.json")
        assert lds_json, f"lds.json missing in ConfigMap {cm_name} ({mcc.cluster_name})"

        lds_cfg = json.loads(lds_json)
        sni_names: List[str] = []
        for resource in lds_cfg.get("resources", []):
            for fc in resource.get("filter_chains", []):
                fcm = fc.get("filter_chain_match", {}) or {}
                sni_names.extend(fcm.get("server_names", []) or [])
        assert sni_names, (
            f"[{mcc.cluster_name}] Envoy LB (idx={cluster_idx}) lds.json parsed to zero SNI server_names — "
            f"the SNI half of the foreign-leak check would be vacuously green"
        )

        if helper is not None:
            expected_fqdn = search_resource_names.mc_proxy_svc_fqdn(mdbs_resource_name, namespace, cluster_idx)
            assert expected_fqdn in sni_names, (
                f"[{mcc.cluster_name}] expected SNI server_name {expected_fqdn!r} "
                f"in lds.json filter_chain_match.server_names, got {sni_names}"
            )

            # Defensive: no OTHER cluster's proxy-svc FQDN should appear.
            for other in member_cluster_clients:
                if other.cluster_name == mcc.cluster_name:
                    continue
                other_fqdn = search_resource_names.mc_proxy_svc_fqdn(
                    mdbs_resource_name, namespace, _resolve_cluster_index(helper, other)
                )
                assert other_fqdn not in sni_names, (
                    f"[{mcc.cluster_name}] foreign SNI {other_fqdn!r} present in "
                    f"lds.json server_names — per-cluster Envoy must only match its own FQDN"
                )

        upstreams: List[str] = []
        if expected_upstreams_by_idx is not None:
            expected_upstreams = expected_upstreams_by_idx.get(cluster_idx)
            assert expected_upstreams, (
                f"[{mcc.cluster_name}] empty/missing expected_upstreams for cluster_idx={cluster_idx} — "
                f"the positive local-route anchor would be vacuously green"
            )
            cds_json = data.get("cds.json")
            assert cds_json, f"[{mcc.cluster_name}] cds.json missing in ConfigMap {cm_name}; got keys {list(data)}"

            for cluster in json.loads(cds_json).get("resources", []):
                for ep in cluster.get("load_assignment", {}).get("endpoints", []):
                    for lb in ep.get("lb_endpoints", []):
                        addr = lb.get("endpoint", {}).get("address", {}).get("socket_address", {}).get("address")
                        if addr:
                            upstreams.append(addr)

            for want in expected_upstreams:
                assert want in upstreams, (
                    f"[{mcc.cluster_name}] Envoy LB (idx={cluster_idx}) missing local mongot upstream {want!r}; "
                    f"got upstreams={upstreams}"
                )

            all_names = sni_names + upstreams
            for foreign_idx in indices:
                if foreign_idx == cluster_idx:
                    continue
                foreign_segment = (
                    f"{search_resource_names.mongot_statefulset_name_for_cluster(mdbs_resource_name, foreign_idx)}-"
                )
                for name in all_names:
                    assert foreign_segment not in name, (
                        f"[{mcc.cluster_name}] Envoy LB (idx={cluster_idx}) leaks foreign cluster idx={foreign_idx} "
                        f"segment {foreign_segment!r} in {name!r} — per-cluster Envoy must reference only its own slice"
                    )

        logger.info(
            f"[{mcc.cluster_name}] Envoy LB (idx={cluster_idx}) routes local-only: "
            f"sni={sni_names} upstreams={upstreams}"
        )


# ---------------------------------------------------------------------------
# Per-cluster AC mongotHost patch + observation.
# ---------------------------------------------------------------------------


def read_mongod_set_parameter(
    pod_name: str,
    namespace: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> Dict[str, object]:
    """Read /data/automation-mongod.conf inside a mongod pod and return setParameter map.

    Caller chooses the api_client (member cluster).
    """
    raw = KubernetesTester.run_command_in_pod_container(
        pod_name,
        namespace,
        ["cat", "/data/automation-mongod.conf"],
        api_client=api_client,
    )
    parsed = yaml.safe_load(raw) or {}
    return parsed.get("setParameter", {}) or {}


def patch_mongot_host_via_ac(
    mdb,
    resolve_host: Callable[[str], Optional[str]],
    log=logger,
    timeout: int = 900,
    no_match_detail: str = "no AC processes matched the mongot-host resolver",
) -> None:
    """PUT mongotHost+searchIndexManagementHostAndPort per AC process, then block until
    every agent applies the new goal version (setParameter changes require a restart).

    MongoDBMultiCluster/MC-sharded expose no per-process additionalMongodConfig, so these
    are set directly on the OM Automation Config — keeping them out of the CR spec ensures
    operator reconciles never clobber them.
    """
    om_tester = mdb.get_om_tester()
    ac_path = f"/groups/{om_tester.context.project_id}/automationConfig"
    ac = om_tester.om_request("get", ac_path).json()
    patched: List[str] = []
    for process in ac.get("processes", []):
        host = resolve_host(process.get("name", ""))
        if host is None:
            continue
        sp = process.setdefault("args2_6", {}).setdefault("setParameter", {})
        sp["mongotHost"] = host
        sp["searchIndexManagementHostAndPort"] = host
        patched.append(f"{process.get('name', '')}->{host}")
    assert patched, f"{no_match_detail}; AC contained {[p.get('name') for p in ac.get('processes', [])]}"
    log.info(f"patched {len(patched)} processes: {patched}")
    ac["version"] = ac.get("version", 0) + 1
    _put_automation_config_past_lock(om_tester, ac_path, ac)
    log.info(f"PUT automation config v{ac['version']} with per-cluster mongotHost")
    om_tester.wait_agents_ready(timeout=timeout)


def _put_automation_config_past_lock(om_tester, ac_path: str, ac: dict, attempts: int = 3) -> None:
    """clear_feature_controls + PUT, retried on 401 — the operator can re-assert
    EXTERNALLY_MANAGED_LOCK between the clear and the PUT."""
    for attempt in range(1, attempts + 1):
        om_tester.clear_feature_controls()
        try:
            om_tester.om_request("put", ac_path, json_object=ac)
            return
        except requests.HTTPError as e:
            if e.response is None or e.response.status_code != 401 or attempt == attempts:
                raise
            logger.warning(f"automationConfig PUT got 401 (operator re-locked); retry {attempt}/{attempts - 1}")


def patch_per_cluster_mongot_host_via_om(
    *,
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
    envoy_proxy_port: int,
) -> None:
    proxy_by_cluster_idx = {
        helper.cluster_index(mcc.cluster_name): f"{helper.proxy_svc_fqdn(mcc.cluster_name)}:{envoy_proxy_port}"
        for mcc in member_cluster_clients
    }
    logger.info(f"per-cluster mongotHost map: {proxy_by_cluster_idx}")

    process_prefix = f"{mdb.name}-"

    def resolve_host(process_name: str) -> Optional[str]:
        if not process_name.startswith(process_prefix):
            return None
        try:
            cluster_idx = int(process_name[len(process_prefix) :].split("-")[0])
        except ValueError:
            return None
        return proxy_by_cluster_idx.get(cluster_idx)

    patch_mongot_host_via_ac(
        mdb,
        resolve_host,
        no_match_detail=f"no AC processes matched prefix {process_prefix!r}",
    )


def _assert_mongot_host_on_disk(
    mdb: MongoDBMulti,
    expected_by_idx: Dict[int, str],
    member_cluster_clients: List[MultiClusterClient],
    members_for_idx: Optional[Callable[[int], int]] = None,
    index_of: Optional[Callable[[MultiClusterClient], int]] = None,
    timeout: int = 300,
) -> None:
    """Reads /data/automation-mongod.conf on disk so we verify the AC patch landed AND the
    agent applied it — not just that we set OM REST. ``members_for_idx`` widens the check
    beyond the (possibly-primary) pod-0 to every member pod.
    """
    if members_for_idx is None:

        def members_for_idx(_cluster_idx: int) -> int:
            return 1  # pod-0 only — the central-MC default

    if index_of is None:

        def index_of(client: MultiClusterClient) -> int:
            return _resolve_cluster_index(None, client)

    def check() -> tuple:
        all_correct = True
        checked = 0
        msgs: List[str] = []
        for mcc in member_cluster_clients:
            cluster_idx = index_of(mcc)
            expected = expected_by_idx[cluster_idx]
            for member in range(members_for_idx(cluster_idx)):
                checked += 1
                pod_name = f"{mdb.name}-{cluster_idx}-{member}"
                try:
                    params = read_mongod_set_parameter(pod_name, mdb.namespace, mcc.api_client)
                    got_host = params.get("mongotHost", "")
                    got_idx_mgmt = params.get("searchIndexManagementHostAndPort", "")
                    if got_host != expected:
                        all_correct = False
                        msgs.append(f"[{mcc.cluster_name}] {pod_name}: mongotHost={got_host!r} expected={expected!r}")
                    elif got_idx_mgmt != expected:
                        all_correct = False
                        msgs.append(
                            f"[{mcc.cluster_name}] {pod_name}: searchIndexManagementHostAndPort="
                            f"{got_idx_mgmt!r} expected={expected!r}"
                        )
                    else:
                        msgs.append(f"[{mcc.cluster_name}] {pod_name}: mongotHost={expected} OK")
                except Exception as exc:
                    all_correct = False
                    msgs.append(f"[{mcc.cluster_name}] {pod_name}: error reading conf: {exc}")
        if checked == 0:
            return (
                False,
                "no mongod pods checked (empty member_clients or members_for_idx==0) — would be vacuously green",
            )
        return all_correct, "\n".join(msgs)

    run_periodically(check, timeout=timeout, sleep_time=10, msg="per-cluster mongotHost on disk")


def assert_per_cluster_mongot_host_observed(
    *,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    helper: MCSearchDeploymentHelper,
    envoy_proxy_port: int,
    timeout: int = 300,
) -> None:
    expected_by_idx = {
        helper.cluster_index(mcc.cluster_name): f"{helper.proxy_svc_fqdn(mcc.cluster_name)}:{envoy_proxy_port}"
        for mcc in member_cluster_clients
    }
    _assert_mongot_host_on_disk(
        mdb,
        expected_by_idx,
        member_cluster_clients,
        index_of=lambda mcc: _resolve_cluster_index(helper, mcc),
        timeout=timeout,
    )


# ---------------------------------------------------------------------------
# Sharded per-(cluster, shard) AC mongotHost patch.
# ---------------------------------------------------------------------------


def _classify_sharded_process(
    process_name: str,
    mdb_name: str,
    multi_cluster: bool,
) -> Optional[Tuple[str, int, Optional[int]]]:
    """Map an AC process name to ``(role, cluster_index, shard_index)``.

    ``role`` is ``"mongos"`` (shard_index None) or ``"shard"``. config-server and
    foreign processes return None. Naming mirrors api/mongodb/v1/mdb/mongodb_types.go
    + pkg/dns: SC shard ``{mdb}-{shardIdx}-{member}`` / mongos ``{mdb}-mongos-{pod}``;
    MC shard ``{mdb}-{shardIdx}-{clusterIdx}-{member}`` / mongos ``{mdb}-mongos-{clusterIdx}-{pod}``.
    """
    prefix = f"{mdb_name}-"
    if not process_name.startswith(prefix):
        return None
    tokens = process_name[len(prefix) :].split("-")
    if tokens[0] == "config":
        return None  # config servers carry no mongotHost
    if tokens[0] == "mongos":
        cluster_index = int(tokens[1]) if multi_cluster else 0
        return "mongos", cluster_index, None
    if not tokens[0].isdigit():
        return None
    shard_index = int(tokens[0])
    cluster_index = int(tokens[1]) if multi_cluster else 0
    return "shard", cluster_index, shard_index


def patch_per_cluster_sharded_mongot_host_via_om(
    *,
    mdb,
    mdbs_resource_name: str,
    namespace: str,
    shard_count: int,
    cluster_indexes: List[int],
    envoy_proxy_port: int,
    multi_cluster: bool,
) -> None:
    """PUT the OM automation config so each sharded process targets its cluster-local proxy.

    Per (cluster, shard): shard mongod → ``shard_proxy_service_host`` (cluster-local per-shard
    proxy); per cluster: mongos → ``mc_proxy_svc_fqdn`` (cluster-level proxy). Cluster-generic:
    ``cluster_indexes=[0]`` + ``multi_cluster=False`` is the single-cluster sharded invocation.
    """
    shard_proxy_host = {
        (cluster_index, shard_index): search_resource_names.shard_proxy_service_host(
            mdbs_resource_name, f"{mdb.name}-{shard_index}", namespace, envoy_proxy_port, cluster_index
        )
        for cluster_index in cluster_indexes
        for shard_index in range(shard_count)
    }
    mongos_proxy_host = {
        cluster_index: f"{search_resource_names.mc_proxy_svc_fqdn(mdbs_resource_name, namespace, cluster_index)}:{envoy_proxy_port}"
        for cluster_index in cluster_indexes
    }
    logger.info(f"sharded shard-proxy map: {shard_proxy_host}")
    logger.info(f"sharded mongos-proxy map: {mongos_proxy_host}")

    def resolve_host(process_name: str) -> Optional[str]:
        classified = _classify_sharded_process(process_name, mdb.name, multi_cluster)
        if classified is None:
            return None
        role, cluster_index, shard_index = classified
        if role == "mongos":
            return mongos_proxy_host.get(cluster_index)
        # non-mongos roles (shard/config) always carry a concrete shard index
        assert shard_index is not None
        return shard_proxy_host.get((cluster_index, shard_index))

    patch_mongot_host_via_ac(
        mdb,
        resolve_host,
        no_match_detail=f"no sharded AC processes matched mdb {mdb.name!r}",
    )


def assert_sharded_mongot_host_observed(
    *,
    mdb,
    mdbs_resource_name: str,
    namespace: str,
    shard_count: int,
    cluster_indexes: List[int],
    envoy_proxy_port: int,
    multi_cluster: bool,
    member_api_client_by_cluster: Optional[Mapping[int, kubernetes.client.ApiClient]] = None,
    timeout: int = 300,
) -> None:
    """Poll each shard's first mongod on disk and confirm its cluster-local proxy host landed.

    Reads ``/data/automation-mongod.conf`` so we verify the agent applied the AC patch,
    not just that OM accepted it. ``member_api_client_by_cluster`` targets the cluster
    hosting each pod (MC); SC leaves it None (default client).
    """
    expected: Dict[str, str] = {}
    pod_to_cluster: Dict[str, int] = {}
    for cluster_index in cluster_indexes:
        for shard_index in range(shard_count):
            pod_name = f"{mdb.name}-{shard_index}-{cluster_index}-0" if multi_cluster else f"{mdb.name}-{shard_index}-0"
            expected[pod_name] = search_resource_names.shard_proxy_service_host(
                mdbs_resource_name, f"{mdb.name}-{shard_index}", namespace, envoy_proxy_port, cluster_index
            )
            pod_to_cluster[pod_name] = cluster_index

    def check() -> tuple:
        all_correct = True
        msgs: List[str] = []
        for pod_name, want in expected.items():
            api_client = None
            if member_api_client_by_cluster is not None:
                api_client = member_api_client_by_cluster.get(pod_to_cluster[pod_name])
            try:
                params = read_mongod_set_parameter(pod_name, namespace, api_client)
                got_host = params.get("mongotHost", "")
                got_idx = params.get("searchIndexManagementHostAndPort", "")
                if got_host != want or got_idx != want:
                    all_correct = False
                    msgs.append(f"{pod_name}: mongotHost={got_host!r}/idxMgmt={got_idx!r} expected={want!r}")
                else:
                    msgs.append(f"{pod_name}: mongotHost={want} OK")
            except Exception as exc:
                all_correct = False
                msgs.append(f"{pod_name}: error reading conf: {exc}")
        return all_correct, "\n".join(msgs)

    run_periodically(check, timeout=timeout, sleep_time=10, msg="per-shard mongotHost on disk")
