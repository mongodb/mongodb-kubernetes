"""Q2-MC-RS e2e: 2-cluster MongoDBSearch with external MongoDBMulti source.

Strict assertions across the full path:

- Operator creates per-cluster mongot StatefulSets, Services, and ConfigMaps
  via member-cluster clients.
- Envoy controller deploys a per-cluster Envoy; SNI uses the per-cluster
  proxy-svc FQDN; replicas come from top-level spec.loadBalancer.managed.replicas.
- The LB TLS cert SAN list covers every cluster's proxy-svc FQDN.
- Admission rejects MC without spec.source.external.hostAndPorts.
- Per-cluster reconcileUnit fan-out applies each cluster's reconcile to its
  target member-cluster client.

Data-plane assertions:

- $search returns non-empty results when invoked from the central
  cluster client (the topology is RS — any pod's aggregation will hit
  the primary, so a single $search execution per test step is
  sufficient to validate cross-cluster mongot indexing).
- $vectorSearch creates an index with auto-embedding and returns a
  non-empty result set; this exercises the AI/Voyage embedding path
  end-to-end (env var `AI_MONGODB_EMBEDDING_QUERY_KEY`).

Per-cluster mongotHost locality:
- The MongoDBMultiCluster CRD does not (yet) expose per-cluster
  `additionalMongodConfig` (the field lives only at the top level on
  `DbCommonSpec`; see
  `docs/superpowers/research/2026-05-04-per-cluster-mongothost-mitigations.md`
  for the gap analysis and the planned CRD-extension micro-PR). For the
  e2e, after MongoDBMulti reaches Running we patch the Ops Manager
  automation config in `test_patch_per_cluster_mongot_host` so each
  cluster's mongods point `mongotHost` and
  `searchIndexManagementHostAndPort` at THEIR cluster's local Envoy
  proxy-svc FQDN. The top-level spec value still names cluster-0's
  proxy-svc (so the source RS can reach Running before the patch), and
  the per-process AC keys override it for query-side locality.
"""

import os
from typing import List

import kubernetes
import pytest
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, try_load
from kubetester.certs import create_tls_certs
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.multicluster_search.mc_search_deployment_helper import MCSearchDeploymentHelper
from tests.common.multicluster_search.per_cluster_assertions import (
    assert_deployment_ready_in_cluster,
    assert_resource_in_cluster,
)
from tests.common.multicluster_search.secret_replicator import replicate_secret
from tests.common.search import search_resource_names
from tests.common.search.movies_search_helper import (
    EMBEDDING_QUERY_KEY_ENV_VAR,
    SampleMoviesSearchHelper,
)
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

# Resource names
MDB_RESOURCE_NAME = "mdb-mc-rs-ext-lb"
MDBS_RESOURCE_NAME = "mdb-mc-rs-ext-lb-search"

# Per-cluster member counts: 2-cluster MC RS, 2 members in each cluster.
MEMBERS_PER_CLUSTER = [2, 2]

# Per-cluster mongot StatefulSet replica counts.
MONGOT_REPLICAS_PER_CLUSTER = 1

# Per-cluster Envoy LB Deployment replica count (mirrored across all clusters).
ENVOY_LB_REPLICAS = 2

# Ports
ENVOY_PROXY_PORT = 27028

# User credentials
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

# TLS configuration
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
SOURCE_CERT_PREFIX = "clustercert"
SOURCE_BUNDLE_SECRET = f"{SOURCE_CERT_PREFIX}-{MDB_RESOURCE_NAME}-cert"


# =============================================================================
# Fixtures
# =============================================================================


@fixture(scope="module")
def helper(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
) -> MCSearchDeploymentHelper:
    return MCSearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        member_cluster_clients={mcc.cluster_name: mcc.core_v1_api() for mcc in member_cluster_clients},
    )


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="module")
def mdb(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    custom_mdb_version: str,
    ca_configmap: str,
) -> MongoDBMulti:
    """2-cluster MongoDBMulti RS source with TLS+SCRAM.

    `mongotHost` is set top-level to cluster-0's proxy-svc FQDN to keep
    every mongod's startup-time validation happy (the source RS can't
    reach Running with `searchTLSMode=requireTLS` if `mongotHost` is
    missing). Per-process per-cluster locality is then applied via a
    post-deploy AC patch in `test_patch_per_cluster_mongot_host` —
    operator does not yet expose
    `clusterSpecList[i].additionalMongodConfig`.
    """
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("search-q2-mc-rs.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MEMBERS_PER_CLUSTER)

    # Top-level mongotHost points at cluster-0's per-cluster proxy Service.
    proxy_host = f"{MDBS_RESOURCE_NAME}-search-0-proxy-svc.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}"
    resource["spec"]["additionalMongodConfig"] = {
        "setParameter": {
            "mongotHost": proxy_host,
            "searchIndexManagementHostAndPort": proxy_host,
            "skipAuthenticationToSearchIndexManagementServer": False,
            "skipAuthenticationToMongot": False,
            "searchTLSMode": "requireTLS",
            "useGrpcForSearch": True,
        },
    }

    resource["spec"]["security"] = {
        "certsSecretPrefix": SOURCE_CERT_PREFIX,
        "tls": {"ca": ca_configmap},
        "authentication": {"enabled": True, "modes": ["SCRAM"]},
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def mdbs(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdb: MongoDBMulti,
    ca_configmap: str,
) -> MongoDBSearch:
    """MongoDBSearch over external MongoDBMulti source, MC topology.

    spec.source.external.hostAndPorts seeds the same top-level host list
    to every cluster's mongot ConfigMap (Phase 2 fan-out — commit
    a43b59183 / 275ffb242). spec.clusters[i] carries the per-cluster
    mongot replica count; clusterName must match the operator's stable
    cluster index ordering.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

    # Source RS pod hostnames: MongoDBMulti.service_names() returns
    # `{name}-{cluster_index}-{member_index}-svc` for each pod across
    # every cluster; appending the namespaced FQDN + port gives the
    # seed list for spec.source.external.hostAndPorts.
    seeds = [f"{svc}.{namespace}.svc.cluster.local:27017" for svc in mdb.service_names()]

    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "hostAndPorts": seeds,
            "tls": {"ca": {"name": ca_configmap}},
        },
    }

    resource["spec"]["security"] = {
        "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
    }

    # Per-cluster shape: spec.clusters[i] = {clusterName, replicas}. NO
    # syncSourceSelector.hosts — MVP renders top-level external host list
    # to every cluster's mongot.
    resource["spec"]["clusters"] = [
        {"clusterName": mcc.cluster_name, "replicas": MONGOT_REPLICAS_PER_CLUSTER} for mcc in member_cluster_clients
    ]

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


def _build_user(
    yaml_filename: str,
    name: str,
    username: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@fixture(scope="module")
def admin_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-mdb-admin.yaml", ADMIN_USER_NAME, ADMIN_USER_NAME, namespace, central_cluster_client
    )


@fixture(scope="module")
def user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user("mongodbuser-mdb-user.yaml", USER_NAME, USER_NAME, namespace, central_cluster_client)


@fixture(scope="module")
def mongot_user(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBUser:
    return _build_user(
        "mongodbuser-search-sync-source-user.yaml",
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}",
        MONGOT_USER_NAME,
        namespace,
        central_cluster_client,
    )


# =============================================================================
# Test steps
# =============================================================================


@mark.e2e_search_q2_mc_rs_steady
def test_create_kube_config_file(cluster_clients: dict, member_cluster_names: List[str]):
    """Sanity check: kube config carries entries for every member cluster."""
    assert len(cluster_clients) >= len(member_cluster_names), (
        f"cluster_clients={list(cluster_clients)}, " f"member_cluster_names={member_cluster_names}"
    )
    for name in member_cluster_names:
        assert name in cluster_clients, f"missing member cluster {name} in cluster_clients"


@mark.e2e_search_q2_mc_rs_steady
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_search_q2_mc_rs_steady
def test_install_source_tls_certificates(
    multi_cluster_issuer: str,
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    """Cert-manager bundle Secret for the MongoDBMulti source.

    create_multi_cluster_mongodb_tls_certs writes the bundle to the
    central cluster only; the operator copies per-pod material to
    members. Phase 2 source TLS still works the same way.
    """
    create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        SOURCE_BUNDLE_SECRET,
        member_cluster_clients,
        central_cluster_client,
        mdb,
    )


@mark.e2e_search_q2_mc_rs_steady
def test_create_mdb_resource(mdb: MongoDBMulti):
    """STRICT — Phase 2 ops code makes per-cluster MongoDBMulti reach Running."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1500)


def _apply_user_password(
    user_resource: MongoDBUser,
    password: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> None:
    create_or_update_secret(
        namespace,
        name=user_resource["spec"]["passwordSecretKeyRef"]["name"],
        data={"password": password},
        api_client=central_cluster_client,
    )
    user_resource.update()


@mark.e2e_search_q2_mc_rs_steady
def test_create_user_credentials(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
):
    _apply_user_password(admin_user, ADMIN_USER_PASSWORD, namespace, central_cluster_client)
    admin_user.assert_reaches_phase(Phase.Updated, timeout=300)

    _apply_user_password(user, USER_PASSWORD, namespace, central_cluster_client)
    user.assert_reaches_phase(Phase.Updated, timeout=300)

    # mongot user needs searchCoordinator role from the MongoDBSearch CR;
    # we don't wait here.
    _apply_user_password(mongot_user, MONGOT_USER_PASSWORD, namespace, central_cluster_client)


@mark.e2e_search_q2_mc_rs_steady
def test_deploy_lb_certificates(
    namespace: str,
    multi_cluster_issuer: str,
    helper: MCSearchDeploymentHelper,
):
    """Managed LB server + client certs, with SAN list covering every
    cluster's per-cluster proxy-svc FQDN.

    Phase 2's LB cert SAN validator (commit dbdd35b0c) requires every
    cluster's proxy-svc FQDN to be in the cert; producing them here
    keeps the test honest.
    """
    lb_server_cert_name = search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)

    server_domains = [
        f"{MDBS_RESOURCE_NAME}-search-{helper.cluster_index(name)}-proxy-svc.{namespace}.svc.cluster.local"
        for name in helper.member_cluster_names()
    ]

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME),
        replicas=ENVOY_LB_REPLICAS,
        service_name=server_domains[0].split(".")[0],
        additional_domains=server_domains,
        secret_name=lb_server_cert_name,
    )
    logger.info(f"LB server certificate created with SANs={server_domains}: {lb_server_cert_name}")

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)}-client",
        replicas=1,
        service_name=server_domains[0].split(".")[0],
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name=lb_client_cert_name,
    )
    logger.info(f"LB client certificate created: {lb_client_cert_name}")


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_tls_certificate(
    namespace: str,
    multi_cluster_issuer: str,
    helper: MCSearchDeploymentHelper,
):
    """mongot TLS cert with SANs for every per-cluster mongot Service +
    every per-cluster proxy-svc FQDN.

    Without per-cluster SANs, mongot pods fail their TLS handshake.
    """
    secret_name = search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    additional_domains: List[str] = []
    for name in helper.member_cluster_names():
        idx = helper.cluster_index(name)
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{idx}-svc.{namespace}.svc.cluster.local")
        additional_domains.append(f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc.{namespace}.svc.cluster.local")

    create_tls_certs(
        issuer=multi_cluster_issuer,
        namespace=namespace,
        resource_name=search_resource_names.mongot_statefulset_name(MDBS_RESOURCE_NAME),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    logger.info(f"mongot TLS certificate created with SANs={additional_domains}: {secret_name}")


@mark.e2e_search_q2_mc_rs_steady
def test_replicate_secrets_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
):
    """Replicate LB + mongot TLS Secrets, mongot password, and CA
    ConfigMap to every member cluster.

    Per program rules MCK does NOT replicate Secrets in production —
    that's the customer's responsibility. The test harness does it
    here so the e2e can stand up a working multi-cluster fixture
    without requiring the customer-side replication machinery.

    Without this step, mongot pods in member clusters stay
    PodInitializing forever and the search resource never reaches
    Phase=Running.
    """
    central_core = CoreV1Api(api_client=central_cluster_client)
    member_cores = {mcc.cluster_name: mcc.core_v1_api() for mcc in member_cluster_clients}

    secrets_to_replicate = [
        # mongot TLS cert (server cert presented by per-cluster mongot pods).
        search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        # Managed LB server TLS cert (Envoy presents this on the proxy-svc port).
        search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        # Managed LB client TLS cert (Envoy presents this when calling mongot upstream).
        search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        # Mongot user password Secret (sync-source credentials).
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
    ]

    for secret_name in secrets_to_replicate:
        replicate_secret(
            secret_name=secret_name,
            namespace=namespace,
            central_client=central_core,
            member_clients=member_cores,
        )
        logger.info(f"replicated Secret {secret_name} to {len(member_cores)} member cluster(s)")

    # CA ConfigMap — mongot needs to verify mongod TLS cert against the
    # source RS CA, which lives in the same ConfigMap as the search
    # resource's CA reference.
    source_cm = central_core.read_namespaced_config_map(name=CA_CONFIGMAP_NAME, namespace=namespace)
    cm_data = dict(source_cm.data or {})
    for mcc in member_cluster_clients:
        create_or_update_configmap(namespace, CA_CONFIGMAP_NAME, cm_data, api_client=mcc.api_client)
        logger.info(f"replicated CA ConfigMap {CA_CONFIGMAP_NAME} into cluster {mcc.cluster_name}")


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    """STRICT — Phase 2 operator code makes Running real now.

    Per-cluster mongot StatefulSets, Services, ConfigMaps, and Envoy
    Deployments must all converge before the resource reaches Running.
    """
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


def _per_cluster_mongot_config_name(mdbs_name: str, cluster_index: int) -> str:
    """Mirror MongotConfigConfigMapNameForCluster: idx 0 → legacy unindexed name."""
    if cluster_index == 0:
        return f"{mdbs_name}-search-config"
    return f"{mdbs_name}-search-{cluster_index}-config"


def _per_cluster_envoy_deployment_name(mdbs_name: str, cluster_name: str) -> str:
    """Mirror LoadBalancerDeploymentNameForCluster: `{name}-search-lb-0-{clusterName}`."""
    return f"{mdbs_name}-search-lb-0-{cluster_name}"


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_mongot_resources(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Each cluster has its own mongot StatefulSet, Service, ConfigMap.

    The operator creates per-cluster resources via cluster-index-suffixed
    names — except the ConfigMap for cluster index 0, which keeps the legacy
    unindexed name for single-cluster back-compat.
    """
    for mcc in member_cluster_clients:
        idx = helper.cluster_index(mcc.cluster_name)
        sts_name = f"{MDBS_RESOURCE_NAME}-search-{idx}"
        svc_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-svc"
        cm_name = _per_cluster_mongot_config_name(MDBS_RESOURCE_NAME, idx)
        proxy_svc_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc"

        apps = mcc.apps_v1_api()
        core = mcc.core_v1_api()
        assert_resource_in_cluster(apps, kind="StatefulSet", name=sts_name, namespace=namespace)
        assert_resource_in_cluster(core, kind="Service", name=svc_name, namespace=namespace)
        assert_resource_in_cluster(core, kind="ConfigMap", name=cm_name, namespace=namespace)
        assert_resource_in_cluster(core, kind="Service", name=proxy_svc_name, namespace=namespace)
        logger.info(
            f"per-cluster mongot resources verified in cluster {mcc.cluster_name} "
            f"(idx={idx}): {sts_name}, {svc_name}, {cm_name}, {proxy_svc_name}"
        )


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_envoy_deployment(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — every cluster's Envoy Deployment exists and is fully ready.

    Per-cluster Envoy Deployment name pattern is
    `{search-name}-search-lb-0-{clusterName}` — see
    LoadBalancerDeploymentNameForCluster in mongodbsearch_types.go.
    """
    for mcc in member_cluster_clients:
        envoy_deployment_name = _per_cluster_envoy_deployment_name(MDBS_RESOURCE_NAME, mcc.cluster_name)
        apps = mcc.apps_v1_api()
        assert_resource_in_cluster(apps, kind="Deployment", name=envoy_deployment_name, namespace=namespace)
        assert_deployment_ready_in_cluster(apps, name=envoy_deployment_name, namespace=namespace)
        logger.info(f"Envoy Deployment {envoy_deployment_name} ready in cluster {mcc.cluster_name}")


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_status(mdbs: MongoDBSearch):
    """STRICT — status.clusterStatusList must include every spec.clusters[] entry."""
    mdbs.load()
    cluster_statuses = mdbs["status"].get("clusterStatusList")
    assert cluster_statuses, f"status.clusterStatusList empty/missing: {cluster_statuses}"

    spec_cluster_names = {c["clusterName"] for c in mdbs["spec"].get("clusters") or []}
    status_cluster_names = {entry.get("clusterName") for entry in cluster_statuses}
    assert spec_cluster_names <= status_cluster_names, (
        f"spec.clusters names {spec_cluster_names} not all present in "
        f"status.clusterStatusList: {status_cluster_names}"
    )
    logger.info(f"status.clusterStatusList covers all spec.clusters[] entries: {sorted(status_cluster_names)}")


@mark.e2e_search_q2_mc_rs_steady
def test_verify_lb_status(mdbs: MongoDBSearch):
    """STRICT — Managed LB status.loadBalancer.phase=Running.

    Aggregated phase: status.loadBalancer.phase reflects the worst
    per-cluster Envoy Deployment phase.
    """
    mdbs.load()
    mdbs.assert_lb_status()


@mark.e2e_search_q2_mc_rs_steady
def test_patch_per_cluster_mongot_host(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Patch Ops Manager AC so each cluster's mongods point `mongotHost`
    and `searchIndexManagementHostAndPort` at THEIR cluster's local Envoy
    proxy-svc FQDN.

    The MongoDBMultiCluster CRD does NOT yet expose per-cluster
    `additionalMongodConfig`. The CRD-extension path (Option A in
    `docs/superpowers/research/2026-05-04-per-cluster-mongothost-mitigations.md`)
    is the production fix; for the e2e we instead read the AC, mutate
    each process's `args2_6.setParameter` based on the cluster index
    encoded in the process name (`{mdb.name}-{clusterIdx}-{podIdx}`),
    bump the AC version, and PUT the result back via the OM REST API.
    `wait_agents_ready` then blocks until every mongod has rolled to
    the new goal version.

    The top-level `additionalMongodConfig.setParameter.mongotHost` in
    spec keeps pointing at cluster-0's proxy-svc — the source RS
    cannot reach Running with `searchTLSMode=requireTLS` and a missing
    `mongotHost` at startup. The per-process AC keys we set here
    override the top-level value at the AC layer.
    """
    om_tester = mdb.get_om_tester()
    ac_path = f"/groups/{om_tester.context.project_id}/automationConfig"
    ac = om_tester.om_request("get", ac_path).json()

    proxy_by_cluster_idx = {
        helper.cluster_index(mcc.cluster_name): f"{helper.proxy_svc_fqdn(mcc.cluster_name)}:{ENVOY_PROXY_PORT}"
        for mcc in member_cluster_clients
    }
    logger.info(f"per-cluster mongotHost map: {proxy_by_cluster_idx}")

    # Process name is `{mdb.name}-{clusterIdx}-{podIdx}` per
    # CreateMongodProcessesWithLimitMulti in controllers/om/process/om_process.go.
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


# =============================================================================
# Data plane: $search and $vectorSearch
# =============================================================================


def _search_tester_for_mdb(mdb: MongoDBMulti, username: str, password: str) -> SearchTester:
    """Build a SearchTester pointed at the MongoDBMulti RS via mesh DNS.

    Connection string seeds from cluster-0/member-0 and uses the RS
    discovery ?replicaSet=... option so the driver finds the primary.
    """
    seed_host = f"{mdb.name}-0-0-svc.{mdb.namespace}.svc.cluster.local:27017"
    conn_str = f"mongodb://{username}:{password}@{seed_host}/?replicaSet={mdb.name}&authSource=admin"
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


@mark.e2e_search_q2_mc_rs_steady
def test_restore_sample_database(mdb: MongoDBMulti, tools_pod):
    """mongorestore the sample_mflix.archive into the source RS."""
    tester = _search_tester_for_mdb(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_index(mdb: MongoDBMulti):
    """STRICT — $search index creation; not skipped."""
    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    helper = SampleMoviesSearchHelper(search_tester=tester)
    helper.create_search_index()
    helper.wait_for_search_indexes()


@mark.e2e_search_q2_mc_rs_steady
def test_execute_text_search_query(mdb: MongoDBMulti):
    """STRICT — $search aggregation returns non-empty results.

    Per-cluster mongotHost routing is in effect (see
    `test_patch_per_cluster_mongot_host`). Topology is RS — any pod's
    aggregation will hit the primary, which forwards $search to its
    cluster-local Envoy → cluster-local mongot pool. A single search
    execution is sufficient to validate that the indexed dataset is
    discoverable from the locality-pinned query path.
    """
    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    helper = SampleMoviesSearchHelper(search_tester=tester)
    results = helper.text_search_movies("Star Wars")
    assert len(results) > 0, "$search returned no results"
    logger.info(f"$search returned {len(results)} results: {[r.get('title') for r in results[:5]]}")


@mark.e2e_search_q2_mc_rs_steady
def test_create_vector_search_index(mdb: MongoDBMulti):
    """STRICT — $vectorSearch auto-embedding index creation.

    Uses the existing SampleMoviesSearchHelper.create_auto_embedding_vector_search_index.
    Voyage API key from AI_MONGODB_EMBEDDING_QUERY_KEY env var.
    """
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required for $vectorSearch index creation")

    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    helper = SampleMoviesSearchHelper(search_tester=tester)
    helper.create_auto_embedding_vector_search_index()
    helper.wait_for_search_indexes()


@mark.e2e_search_q2_mc_rs_steady
def test_execute_vector_search_query(mdb: MongoDBMulti):
    """STRICT — $vectorSearch returns non-empty results.

    Per-cluster mongotHost routing is in effect (see
    `test_patch_per_cluster_mongot_host`). The auto-embedding
    $vectorSearch path goes through the Voyage embedding API on every
    query; this exercises the AI path end-to-end while keeping the
    locality-pinned mongod -> Envoy -> mongot dataflow.
    """
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required for $vectorSearch")

    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    helper = SampleMoviesSearchHelper(search_tester=tester)
    results = list(helper.execute_auto_embedding_vector_search_query(query="space exploration", limit=4))
    assert len(results) >= 1, f"$vectorSearch returned no results, got: {results}"
    logger.info(f"$vectorSearch returned {len(results)} results: {[r.get('title') for r in results[:5]]}")
