from __future__ import annotations

import json
from typing import Dict, List, Mapping, Optional, Tuple

import kubernetes
import yaml
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret
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


# ---------------------------------------------------------------------------
# Internal naming mirrors — track ``pkg/handler/names_search.go``.
# ---------------------------------------------------------------------------


def _per_cluster_mongot_config_name(mdbs_name: str, cluster_index: int) -> str:
    return f"{mdbs_name}-search-{cluster_index}-config"


def _per_cluster_envoy_deployment_name(mdbs_name: str, cluster_index: int) -> str:
    return f"{mdbs_name}-search-lb-0-{cluster_index}"


def _per_cluster_envoy_configmap_name(mdbs_name: str, cluster_index: int) -> str:
    return f"{mdbs_name}-search-lb-0-{cluster_index}-config"


def _expected_proxy_svc_fqdn(mdbs_name: str, cluster_index: int, namespace: str) -> str:
    return f"{mdbs_name}-search-{cluster_index}-proxy-svc.{namespace}.svc.cluster.local"


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
    """LB server + client certs with SANs covering every cluster's proxy-svc FQDN."""
    lb_server_cert_name = search_resource_names.lb_server_cert_name(mdbs_resource_name, tls_cert_prefix)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(mdbs_resource_name, tls_cert_prefix)

    server_domains = [
        f"{mdbs_resource_name}-search-{helper.cluster_index(name)}-proxy-svc.{namespace}.svc.cluster.local"
        for name in helper.member_cluster_names()
    ]

    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(mdbs_resource_name),
        replicas=envoy_lb_replicas,
        service_name=server_domains[0].split(".")[0],
        additional_domains=server_domains,
        secret_name=lb_server_cert_name,
    )
    logger.info(f"LB server certificate created with SANs={server_domains}: {lb_server_cert_name}")

    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(mdbs_resource_name)}-client",
        replicas=1,
        service_name=server_domains[0].split(".")[0],
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

    secrets_to_replicate = [
        search_resource_names.mongot_tls_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix),
        search_resource_names.lb_server_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix),
        search_resource_names.lb_client_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix),
        f"{mdbs_resource_name}-{mongot_user_name}-password",
        # The CA is stored as both a ConfigMap and a Secret; replicate the Secret half here
        # (the operator mounts it as a Secret volume on per-cluster mongot pods).
        ca_configmap_name,
    ]

    for secret_name in secrets_to_replicate:
        source = central_core.read_namespaced_secret(name=secret_name, namespace=namespace)
        for mcc in member_cluster_clients:
            create_or_update_secret(
                namespace,
                secret_name,
                read_secret(namespace, secret_name, api_client=central_cluster_client),
                type=source.type or "Opaque",
                api_client=mcc.api_client,
            )
        logger.info(f"replicated Secret {secret_name} to {len(member_cluster_clients)} member cluster(s)")

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
    production). Shared Secrets (LB server/client cert, mongot password) go to every
    member; per-(cluster, shard) mongot certs go only to their owning cluster. The CA
    is already on every member (Layer-1 ``ensure_ca_configmap``).
    """
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, targets: List[MultiClusterClient]) -> None:
        secret_type = central_core.read_namespaced_secret(name=secret_name, namespace=namespace).type or "Opaque"
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in targets:
            create_or_update_secret(namespace, secret_name, data, type=secret_type, api_client=mcc.api_client)

    # Cluster-agnostic Secrets — same copy to every member cluster.
    for secret_name in [
        search_resource_names.lb_server_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix),
        search_resource_names.lb_client_cert_name(mdbs_resource_name, mdbs_tls_cert_prefix),
        f"{mdbs_resource_name}-{mongot_user_name}-password",
    ]:
        _copy(secret_name, member_cluster_clients)
        logger.info(f"replicated shared Secret {secret_name} to {len(member_cluster_clients)} member(s)")

    # Per-(cluster, shard) mongot certs — each cluster only needs its own per-shard certs.
    for i, mcc in enumerate(member_cluster_clients):
        for shard_idx in range(shard_count):
            shard_name = f"{mdb_resource_name}-{shard_idx}"
            _copy(
                search_resource_names.shard_tls_cert_name(
                    mdbs_resource_name, shard_name, mdbs_tls_cert_prefix, cluster_index=i
                ),
                [mcc],
            )
        logger.info(f"replicated per-shard Secrets to {mcc.cluster_name} (cluster_index={i})")


# ---------------------------------------------------------------------------
# Per-cluster shape verification.
# ---------------------------------------------------------------------------


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
        cm_name = _per_cluster_mongot_config_name(mdbs_resource_name, idx)
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

        config_yaml = cm.data.get("config.yml") or cm.data.get("mongot.yaml")
        assert config_yaml, f"mongot CM {cm_name} missing config payload; data keys={list(cm.data or {})}"
        parsed = yaml.safe_load(config_yaml)
        cm_hosts = parsed.get("syncSource", {}).get("replicaSet", {}).get("hostAndPort", [])
        assert sorted(cm_hosts) == expected_hosts, (
            f"mongot CM {cm_name} in cluster {mcc.cluster_name}: hostAndPort {sorted(cm_hosts)} "
            f"!= expected seed list {expected_hosts}"
        )

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
        envoy_deployment_name = _per_cluster_envoy_deployment_name(mdbs_resource_name, cluster_idx)
        envoy_cm_name = _per_cluster_envoy_configmap_name(mdbs_resource_name, cluster_idx)
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
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
) -> None:
    """Each per-cluster Envoy ConfigMap's lds.json references exactly its
    cluster-local proxy-svc FQDN in SNI server_names — no cross-cluster leakage.
    """
    for mcc in member_cluster_clients:
        cluster_idx = helper.cluster_index(mcc.cluster_name)
        cm_name = _per_cluster_envoy_configmap_name(mdbs_resource_name, cluster_idx)
        expected_fqdn = _expected_proxy_svc_fqdn(mdbs_resource_name, cluster_idx, namespace)

        cm = mcc.core_v1_api().read_namespaced_config_map(name=cm_name, namespace=namespace)
        lds_json = (cm.data or {}).get("lds.json")
        assert lds_json, f"lds.json missing in ConfigMap {cm_name} ({mcc.cluster_name})"

        lds_cfg = json.loads(lds_json)
        sni_names: List[str] = []
        for resource in lds_cfg.get("resources", []):
            for fc in resource.get("filter_chains", []):
                fcm = fc.get("filter_chain_match", {}) or {}
                sni_names.extend(fcm.get("server_names", []) or [])

        assert expected_fqdn in sni_names, (
            f"[{mcc.cluster_name}] expected SNI server_name {expected_fqdn!r} "
            f"in lds.json filter_chain_match.server_names, got {sni_names}"
        )

        # Defensive: no OTHER cluster's proxy-svc FQDN should appear.
        for other in member_cluster_clients:
            if other.cluster_name == mcc.cluster_name:
                continue
            other_idx = helper.cluster_index(other.cluster_name)
            other_fqdn = _expected_proxy_svc_fqdn(mdbs_resource_name, other_idx, namespace)
            assert other_fqdn not in sni_names, (
                f"[{mcc.cluster_name}] foreign SNI {other_fqdn!r} present in "
                f"lds.json server_names — per-cluster Envoy must only match its own FQDN"
            )

        logger.info(f"[{mcc.cluster_name}] lds.json SNI server_names={sni_names} (expected match: {expected_fqdn})")


# ---------------------------------------------------------------------------
# Per-cluster AC mongotHost patch + observation.
# ---------------------------------------------------------------------------


def _read_mongod_set_parameter(
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


def patch_per_cluster_mongot_host_via_om(
    *,
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
    envoy_proxy_port: int,
) -> None:
    """PUT the OM automation config with per-cluster mongotHost / searchIndexManagementHostAndPort.

    MongoDBMultiCluster doesn't yet expose per-cluster additionalMongodConfig, so the
    per-process AC keys are set directly. Leaving them out of the MongoDBMulti spec
    ensures subsequent operator reconciles never clobber this per-cluster locality.
    """
    om_tester = mdb.get_om_tester()
    ac_path = f"/groups/{om_tester.context.project_id}/automationConfig"
    ac = om_tester.om_request("get", ac_path).json()

    proxy_by_cluster_idx = {
        helper.cluster_index(mcc.cluster_name): f"{helper.proxy_svc_fqdn(mcc.cluster_name)}:{envoy_proxy_port}"
        for mcc in member_cluster_clients
    }
    logger.info(f"per-cluster mongotHost map: {proxy_by_cluster_idx}")

    process_prefix = f"{mdb.name}-"
    patched_processes: List[str] = []
    for process in ac.get("processes", []):
        process_name = process.get("name", "")
        if not process_name.startswith(process_prefix):
            continue
        try:
            cluster_idx = int(process_name[len(process_prefix) :].split("-")[0])
        except ValueError:
            continue
        if cluster_idx not in proxy_by_cluster_idx:
            continue

        mongot_host = proxy_by_cluster_idx[cluster_idx]
        set_parameter = process.setdefault("args2_6", {}).setdefault("setParameter", {})
        set_parameter["mongotHost"] = mongot_host
        set_parameter["searchIndexManagementHostAndPort"] = mongot_host
        patched_processes.append(f"{process_name}->{mongot_host}")

    assert patched_processes, (
        f"no AC processes matched prefix {process_prefix!r}; "
        f"AC contained {[p.get('name') for p in ac.get('processes', [])]}"
    )
    logger.info(f"patched {len(patched_processes)} processes: {patched_processes}")

    ac["version"] = ac.get("version", 0) + 1
    om_tester.om_request("put", ac_path, json_object=ac)
    logger.info(f"PUT automation config v{ac['version']} with per-cluster mongotHost")

    # Block until every mongod has applied the new goal version — setParameter
    # changes here require a process restart.
    om_tester.wait_agents_ready(timeout=900)


def assert_per_cluster_mongot_host_observed(
    *,
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
    envoy_proxy_port: int,
    timeout: int = 300,
) -> None:
    """Poll each cluster's first mongod and confirm the AC mongotHost / index-mgmt host
    resolve to that cluster's local Envoy proxy-svc FQDN+port.

    Reads /data/automation-mongod.conf on disk so we verify the AC patch
    landed AND the agent applied it — not just that we set OM REST.
    """
    expected_per_cluster: Dict[str, str] = {
        mcc.cluster_name: f"{helper.proxy_svc_fqdn(mcc.cluster_name)}:{envoy_proxy_port}"
        for mcc in member_cluster_clients
    }

    def check() -> tuple:
        all_correct = True
        msgs: List[str] = []
        for mcc in member_cluster_clients:
            cluster_idx = helper.cluster_index(mcc.cluster_name)
            pod_name = f"{mdb.name}-{cluster_idx}-0"  # first member of each cluster
            expected = expected_per_cluster[mcc.cluster_name]
            try:
                params = _read_mongod_set_parameter(pod_name, mdb.namespace, mcc.api_client)
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
        return all_correct, "\n".join(msgs)

    run_periodically(check, timeout=timeout, sleep_time=10, msg="per-cluster mongotHost on disk")


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

    om_tester = mdb.get_om_tester()
    ac_path = f"/groups/{om_tester.context.project_id}/automationConfig"
    ac = om_tester.om_request("get", ac_path).json()

    patched: List[str] = []
    for process in ac.get("processes", []):
        process_name = process.get("name", "")
        classified = _classify_sharded_process(process_name, mdb.name, multi_cluster)
        if classified is None:
            continue
        role, cluster_index, shard_index = classified
        if role == "mongos":
            host = mongos_proxy_host.get(cluster_index)
        else:
            # non-mongos roles (shard/config) always carry a concrete shard index
            assert shard_index is not None
            host = shard_proxy_host.get((cluster_index, shard_index))
        if host is None:
            continue

        set_parameter = process.setdefault("args2_6", {}).setdefault("setParameter", {})
        set_parameter["mongotHost"] = host
        set_parameter["searchIndexManagementHostAndPort"] = host
        patched.append(f"{process_name}->{host}")

    assert patched, (
        f"no sharded AC processes matched mdb {mdb.name!r}; "
        f"AC contained {[p.get('name') for p in ac.get('processes', [])]}"
    )
    logger.info(f"patched {len(patched)} sharded processes: {patched}")

    ac["version"] = ac.get("version", 0) + 1
    om_tester.om_request("put", ac_path, json_object=ac)
    logger.info(f"PUT automation config v{ac['version']} with per-(cluster,shard) mongotHost")

    # setParameter changes require a process restart — block until every agent
    # has applied the new goal version.
    om_tester.wait_agents_ready(timeout=900)


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
                params = _read_mongod_set_parameter(pod_name, namespace, api_client)
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
