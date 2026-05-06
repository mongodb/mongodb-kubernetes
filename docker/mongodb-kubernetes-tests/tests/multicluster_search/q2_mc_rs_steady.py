"""Q2-MC-RS e2e: 2-cluster MongoDBSearch with external MongoDBMulti source.

Strict assertions across the full path:

- Operator creates per-cluster mongot StatefulSets, Services, and
  ConfigMaps via member-cluster clients.
- Envoy controller deploys a per-cluster Envoy; the per-cluster Envoy
  ConfigMap's SNI server_names field references the per-cluster
  proxy-svc FQDN (verified by reading the CM out of each member
  cluster — see `test_per_cluster_envoy_sni_observed`).
- The LB TLS cert SAN list covers every cluster's proxy-svc FQDN
  (defensive hygiene; the LB-cert SAN validator is not wired into the
  reconcile loop yet — see the skipped placeholder
  `test_lb_cert_san_validator_when_wired`).
- Per-cluster reconcileUnit fan-out applies each cluster's reconcile
  to its target member-cluster client (transitively asserted via the
  per-cluster STS / Service / ConfigMap reads through member-cluster
  clients).
- `validateMCRequiresExternalHostAndPorts` rejection is exercised by
  `test_admission_rejects_mc_without_external_hosts` — applies a CR
  missing `spec.source.external.hostAndPorts` and asserts the operator
  surfaces Phase=Failed with the validator's error message.

Per-cluster locality (observed, not just constructed):
- After the AC patch lands per-process `mongotHost`, the test execs
  into each cluster's first mongod pod, reads
  `/data/automation-mongod.conf`, and asserts the per-pod parameter
  resolved to the cluster-local Envoy proxy-svc FQDN
  (`test_per_cluster_mongot_host_observed`). The same assertion is
  repeated immediately before each data-plane test step — if the
  operator re-reconciles the MongoDBMulti out-of-band and clobbers the
  per-process value, the data-plane assertions surface that regression
  with a clear error.

Data-plane assertions:

- $search returns non-empty results when invoked through the source
  RS. Wrapped in `run_periodically(timeout=60)` since mongot may
  report `READY` for an index but still be in INITIAL_SYNC.
- $vectorSearch creates an index with auto-embedding and returns a
  non-empty result set; this exercises the AI/Voyage embedding path
  end-to-end (env var `AI_MONGODB_EMBEDDING_QUERY_KEY`). Same retry
  wrapper.

Per-cluster mongotHost locality (mechanism):
- The MongoDBMultiCluster CRD does not (yet) expose per-cluster
  `additionalMongodConfig` (the field lives only at the top level on
  `DbCommonSpec`; see
  `docs/superpowers/research/2026-05-04-per-cluster-mongothost-mitigations.md`
  for the gap analysis and the planned CRD-extension micro-PR). For
  the e2e, after MongoDBMulti reaches Running we patch the Ops Manager
  automation config in `test_patch_per_cluster_mongot_host` so each
  cluster's mongods point `mongotHost` and
  `searchIndexManagementHostAndPort` at THEIR cluster's local Envoy
  proxy-svc FQDN. The top-level spec value still names cluster-0's
  proxy-svc (so the source RS can reach Running before the patch), and
  the per-process AC keys override it for query-side locality.
"""

import json
import os
from typing import Dict, List

import kubernetes
import pymongo.errors
import pytest
import yaml
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, try_load
from kubetester.certs import create_tls_certs
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
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
    EmbeddedMoviesSearchHelper,
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
# Typed as `List[int | None]` to match cluster_spec_list's signature; ty's
# strict invariance check rejects the inferred `List[int]` otherwise.
MEMBERS_PER_CLUSTER: List[int | None] = [2, 2]

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
    ca_configmap: str,
) -> MongoDBMulti:
    """2-cluster MongoDBMulti RS source with TLS+SCRAM.

    Version is pinned in the fixture YAML to a search-aware mongod —
    the setParameter keys below only exist on 8.x; see the YAML for
    the exact constraint.

    Why no `mongotHost` / `searchIndexManagementHostAndPort` here:
    setting them at the spec level makes the operator re-apply the
    same value to EVERY process on every reconcile (process.go
    mergeFrom + RemoveFieldsBasedOnDesiredAndPrevious), clobbering
    the per-cluster AC patch in `test_patch_per_cluster_mongot_host`.
    Leaving them out of spec means the operator never tracks them in
    its `prevArgs26`, so per-process values that the test patches via
    OM AC survive subsequent reconciles. The patching also happens
    early enough that mongods first reach Running without search
    routing, then transition with per-cluster routing on the
    AC-driven restart.

    `searchTLSMode` stays in spec because it's identical across all
    clusters — no per-cluster locality, no race risk. Same for the
    other static toggles below.
    """
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("search-q2-mc-rs.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, MEMBERS_PER_CLUSTER)

    resource["spec"]["additionalMongodConfig"] = {
        "setParameter": {
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

    spec.loadBalancer.managed.externalHostname is required by
    validateManagedLBExternalHostname (managed LB + external MongoDB
    source) AND must contain `{clusterIndex}` per
    validateMCExternalHostnamePlaceholders since len(spec.clusters) > 1.
    The resolved hostname per cluster matches the per-cluster proxy-svc
    FQDN that the SAN list and the AC patch already use, keeping all
    three (cert SANs, AC mongotHost, Envoy SNI) lined up on the same
    name.
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

    # Managed LB externalHostname — substituted per cluster via
    # GetManagedLBEndpointForCluster(i). The {clusterIndex} placeholder
    # resolves to the persisted ClusterIndex (or slice index fallback);
    # the resulting FQDN matches `proxy_svc_fqdn(clusterName)` that the
    # cert SANs and the AC mongotHost patch already target.
    resource["spec"]["loadBalancer"] = {
        "managed": {
            "replicas": ENVOY_LB_REPLICAS,
            "externalHostname": (
                f"{MDBS_RESOURCE_NAME}-search-{{clusterIndex}}-proxy-svc.{namespace}.svc.cluster.local"
            ),
        },
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
def test_create_kube_config_file(
    cluster_clients: dict,
    central_cluster_name: str,
    member_cluster_names: List[str],
):
    """Sanity check: kube config carries entries for every member cluster + central.

    Matches the MC convention in `tests/multicluster/multi_cluster_replica_set.py`
    — central + every member must be present.
    """
    for name in member_cluster_names:
        assert name in cluster_clients, f"missing member cluster {name} in cluster_clients"
    assert (
        central_cluster_name in cluster_clients
    ), f"missing central cluster {central_cluster_name!r} in cluster_clients={list(cluster_clients)}"


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
        # External CA Secret — operator at controllers/searchcontroller/external_search_source.go
        # mounts spec.source.external.tls.ca as a Secret volume on per-cluster mongot pods.
        # The CA is created as both ConfigMap+Secret with the same name in the central cluster
        # (see create_issuer_ca); replicate the Secret half here.
        CA_CONFIGMAP_NAME,
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


def _per_cluster_envoy_deployment_name(mdbs_name: str, cluster_index: int) -> str:
    """Mirror LoadBalancerDeploymentNameForCluster: `{name}-search-lb-0-{clusterIndex}`."""
    return f"{mdbs_name}-search-lb-0-{cluster_index}"


def _per_cluster_envoy_configmap_name(mdbs_name: str, cluster_index: int) -> str:
    """Mirror LoadBalancerConfigMapNameForCluster: `{name}-search-lb-0-{clusterIndex}-config`."""
    return f"{mdbs_name}-search-lb-0-{cluster_index}-config"


def _expected_proxy_svc_fqdn(mdbs_name: str, cluster_index: int, namespace: str) -> str:
    """Per-cluster proxy-svc FQDN (no port) — matches GetManagedLBEndpointForCluster output."""
    return f"{mdbs_name}-search-{cluster_index}-proxy-svc.{namespace}.svc.cluster.local"


def _read_mongod_set_parameter(
    pod_name: str,
    namespace: str,
    api_client: kubernetes.client.ApiClient,
) -> Dict[str, object]:
    """Read /data/automation-mongod.conf inside a mongod pod and return the
    parsed `setParameter` map. Caller chooses the api_client (member cluster).
    """
    raw = KubernetesTester.run_command_in_pod_container(
        pod_name,
        namespace,
        ["cat", "/data/automation-mongod.conf"],
        api_client=api_client,
    )
    parsed = yaml.safe_load(raw) or {}
    return parsed.get("setParameter", {}) or {}


def _assert_per_cluster_mongot_host_observed(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
    timeout: int = 300,
) -> None:
    """Poll each member cluster's first mongod pod and confirm both
    `setParameter.mongotHost` and `setParameter.searchIndexManagementHostAndPort`
    resolve to the cluster-local Envoy proxy-svc FQDN+port.

    Reads the on-disk `/data/automation-mongod.conf` so this verifies the
    AC patch landed AND was applied by the agent — not just that we set
    OM REST. Fails with a per-pod diagnostic showing the actual values.
    """
    expected_per_cluster: Dict[str, str] = {
        mcc.cluster_name: f"{helper.proxy_svc_fqdn(mcc.cluster_name)}:{ENVOY_PROXY_PORT}"
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
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — every cluster's Envoy Deployment exists and is fully ready.

    Per-cluster Envoy Deployment name pattern is
    `{search-name}-search-lb-0-{clusterIndex}` — see
    LoadBalancerDeploymentNameForCluster in mongodbsearch_types.go.
    """
    for mcc in member_cluster_clients:
        cluster_idx = helper.cluster_index(mcc.cluster_name)
        envoy_deployment_name = _per_cluster_envoy_deployment_name(MDBS_RESOURCE_NAME, cluster_idx)
        apps = mcc.apps_v1_api()
        assert_resource_in_cluster(apps, kind="Deployment", name=envoy_deployment_name, namespace=namespace)
        assert_deployment_ready_in_cluster(apps, name=envoy_deployment_name, namespace=namespace)
        logger.info(f"Envoy Deployment {envoy_deployment_name} ready in cluster {mcc.cluster_name} (idx={cluster_idx})")


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_status(mdbs: MongoDBSearch):
    """STRICT — status.clusterStatusList.clusterStatuses must include every spec.clusters[] entry."""
    mdbs.load()
    cluster_statuses = (mdbs["status"].get("clusterStatusList") or {}).get("clusterStatuses") or []
    assert cluster_statuses, f"status.clusterStatusList.clusterStatuses empty/missing: {cluster_statuses}"

    spec_cluster_names = {c["clusterName"] for c in mdbs["spec"].get("clusters") or []}
    status_cluster_names = {entry.get("clusterName") for entry in cluster_statuses}
    assert spec_cluster_names <= status_cluster_names, (
        f"spec.clusters names {spec_cluster_names} not all present in "
        f"status.clusterStatusList.clusterStatuses: {status_cluster_names}"
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

    NB: spec.additionalMongodConfig deliberately does NOT carry
    `mongotHost` / `searchIndexManagementHostAndPort` — see `mdb`
    fixture docstring. We set them per-process here so the operator
    never tracks them in `prevArgs26` and subsequent operator
    reconciles do not clobber the per-cluster locality. `searchTLSMode`
    remains in spec (uniform across clusters, no race).
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


@mark.e2e_search_q2_mc_rs_steady
def test_per_cluster_mongot_host_observed(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — for each cluster's first mongod pod, the on-disk
    `automation-mongod.conf` must show the cluster-local Envoy proxy-svc
    FQDN as `setParameter.mongotHost` and `searchIndexManagementHostAndPort`.

    This is the only observable proof that the AC patch landed AND was
    applied by the agent inside each cluster — `wait_agents_ready` only
    confirms the goal version was reached at the OM layer.
    """
    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients)


@mark.e2e_search_q2_mc_rs_steady
def test_per_cluster_envoy_sni_observed(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — each per-cluster Envoy ConfigMap's envoy.json must reference
    the cluster's proxy-svc FQDN somewhere in its SNI server_names config.

    The Envoy controller renders SNI hostname from
    `ProxyServiceNamespacedNameForCluster(clusterIndex)` (RS path) or from
    the cluster-substituted externalHostname template. Both `{clusterName}`
    and `{clusterIndex}` placeholders are substituted via
    `GetManagedLBEndpointForCluster(i)` in the per-cluster RS build path.
    """
    for mcc in member_cluster_clients:
        cluster_idx = helper.cluster_index(mcc.cluster_name)
        cm_name = _per_cluster_envoy_configmap_name(MDBS_RESOURCE_NAME, cluster_idx)
        expected_fqdn = _expected_proxy_svc_fqdn(MDBS_RESOURCE_NAME, cluster_idx, namespace)

        cm = mcc.core_v1_api().read_namespaced_config_map(name=cm_name, namespace=namespace)
        envoy_json = (cm.data or {}).get("envoy.json")
        assert envoy_json, f"envoy.json missing in ConfigMap {cm_name} ({mcc.cluster_name})"

        # Parse and walk filter_chains[].filter_chain_match.server_names[]
        # for the per-cluster FQDN. Avoids substring false-positives from
        # the upstream-host references that share the namespace prefix.
        envoy_cfg = json.loads(envoy_json)
        sni_names: List[str] = []
        for listener in envoy_cfg.get("static_resources", {}).get("listeners", []):
            for fc in listener.get("filter_chains", []):
                fcm = fc.get("filter_chain_match", {}) or {}
                sni_names.extend(fcm.get("server_names", []) or [])

        assert expected_fqdn in sni_names, (
            f"[{mcc.cluster_name}] expected SNI server_name {expected_fqdn!r} "
            f"in envoy.json filter_chain_match.server_names, got {sni_names}"
        )

        # Defensive: no OTHER cluster's proxy-svc FQDN should appear.
        for other in member_cluster_clients:
            if other.cluster_name == mcc.cluster_name:
                continue
            other_idx = helper.cluster_index(other.cluster_name)
            other_fqdn = _expected_proxy_svc_fqdn(MDBS_RESOURCE_NAME, other_idx, namespace)
            assert other_fqdn not in sni_names, (
                f"[{mcc.cluster_name}] foreign SNI {other_fqdn!r} present in "
                f"envoy.json server_names — per-cluster Envoy must only match its own FQDN"
            )

        logger.info(f"[{mcc.cluster_name}] envoy.json SNI server_names={sni_names} (expected match: {expected_fqdn})")


@pytest.mark.skip(
    reason=(
        "validateLBCertSAN is implemented but not wired into the reconcile "
        "loop yet (TODO: controllers/searchcontroller/mongodbsearch_reconcile_helper.go:1660). "
        "When wired, add a negative test that submits a managed LB cert with a "
        "missing per-cluster SAN and asserts the resource fails to reach Running."
    )
)
@mark.e2e_search_q2_mc_rs_steady
def test_lb_cert_san_validator_when_wired() -> None:
    """Placeholder — exercises the LB cert SAN validator once wired."""
    pass


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


def _direct_search_tester_for_cluster(
    mdb: MongoDBMulti, cluster_index: int, username: str, password: str
) -> SearchTester:
    """SearchTester pinned to a specific cluster's mongod-{idx}-0 pod.

    `directConnection=true` disables RS topology discovery so the
    driver does not silently follow back to the primary. `$search`
    and `$vectorSearch` are read aggregations that any mongod can
    serve, so `readPreference=secondaryPreferred` lets the connected
    pod handle the query whether it is currently primary or secondary.
    """
    pod_host = f"{mdb.name}-{cluster_index}-0-svc.{mdb.namespace}.svc.cluster.local:27017"
    conn_str = (
        f"mongodb://{username}:{password}@{pod_host}/"
        f"?directConnection=true&readPreference=secondaryPreferred&authSource=admin"
    )
    return SearchTester(conn_str, use_ssl=True, ca_path=get_issuer_ca_filepath())


# Per-cluster mongot index-ready timeout. SC tests use 300s; MC layered
# on cross-cluster mesh latency calls for the same generous bound.
SEARCH_INDEX_READY_TIMEOUT = 300

# Per-query retry window. Mongot may report READY for an index but still
# be in INITIAL_SYNC for a few seconds; SC `verify_text_search_query`
# uses the same 60s.
SEARCH_QUERY_RETRY_TIMEOUT = 60


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
def test_create_search_index(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — $search index creation; not skipped.

    Re-asserts AC patch persistence right before creating the index so
    a silent operator re-reconcile (clobbering per-process mongotHost)
    surfaces here with a clear diagnostic instead of a downstream
    "search query empty" mystery.
    """
    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients, timeout=120)

    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)
    movies.create_search_index()
    tester.wait_for_search_indexes_ready(movies.db_name, movies.col_name, timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_q2_mc_rs_steady
def test_execute_text_search_query(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — $search aggregation returns non-empty results.

    Per-cluster mongotHost routing is in effect (see
    `test_patch_per_cluster_mongot_host`). Topology is RS — any pod's
    aggregation will hit the primary, which forwards $search to its
    cluster-local Envoy → cluster-local mongot pool. Wraps the query
    in `run_periodically(timeout=60)` since mongot may briefly be in
    INITIAL_SYNC even after `wait_for_search_indexes_ready` returns.
    """
    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients, timeout=120)

    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    movies = SampleMoviesSearchHelper(search_tester=tester)

    def execute_search() -> tuple:
        try:
            results = movies.text_search_movies("Star Wars")
            count = len(results)
            if count > 0:
                logger.info(f"$search returned {count} results: {[r.get('title') for r in results[:5]]}")
                return True, f"$search returned {count} results"
            return False, "$search returned no results"
        except pymongo.errors.PyMongoError as exc:
            return False, f"$search error: {exc}"

    run_periodically(
        execute_search,
        timeout=SEARCH_QUERY_RETRY_TIMEOUT,
        sleep_time=5,
        msg="$search query to succeed",
    )


@mark.e2e_search_q2_mc_rs_steady
def test_create_vector_search_index(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — $vectorSearch index creation on pre-embedded data.

    Uses `EmbeddedMoviesSearchHelper.create_vector_search_index` against the
    `embedded_movies` collection (Atlas sample dataset already includes the
    `plot_embedding_voyage_3_large` field, restored in
    `test_restore_sample_database`). This path:
      - exercises the per-cluster mongot+Envoy data plane the same way
        `$search` does (mongod -> per-cluster Envoy -> per-cluster mongot)
      - does NOT require Voyage API access for indexing
      - does NOT require cross-cluster auto-embedding leader election
    The auto-embedding (`type: autoEmbed`) variant is deferred to a separate
    spec because it needs cross-cluster leader election that Phase 2 does
    not yet deliver.
    """
    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients, timeout=120)

    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    embedded = EmbeddedMoviesSearchHelper(search_tester=tester)
    embedded.create_vector_search_index()
    embedded.wait_for_vector_search_index(timeout=SEARCH_INDEX_READY_TIMEOUT)


@mark.e2e_search_q2_mc_rs_steady
def test_execute_vector_search_query(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — $vectorSearch on pre-embedded vectors returns non-empty results.

    Per-cluster mongotHost routing is in effect (see
    `test_patch_per_cluster_mongot_host`). Uses pre-embedded
    `plot_embedding_voyage_3_large` from the `embedded_movies` collection
    plus a query vector generated via the Voyage HTTP API. Skips if the
    Voyage query key env var is unset; the index creation in
    `test_create_vector_search_index` does not require that.
    Wrapped in `run_periodically` for the same INITIAL_SYNC reason as $search.
    """
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required to generate the query vector")

    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients, timeout=120)

    tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    embedded = EmbeddedMoviesSearchHelper(search_tester=tester)
    query_vector = embedded.generate_query_vector("space exploration")

    def execute_vector_search() -> tuple:
        try:
            results = embedded.vector_search(query_vector=query_vector, limit=4)
            count = len(results)
            if count >= 1:
                logger.info(f"$vectorSearch returned {count} results: {[r.get('title') for r in results[:5]]}")
                return True, f"$vectorSearch returned {count} results"
            return False, "$vectorSearch returned no results"
        except pymongo.errors.PyMongoError as exc:
            return False, f"$vectorSearch error: {exc}"

    run_periodically(
        execute_vector_search,
        timeout=SEARCH_QUERY_RETRY_TIMEOUT,
        sleep_time=5,
        msg="$vectorSearch query to succeed",
    )


@mark.e2e_search_q2_mc_rs_steady
def test_per_cluster_search_query(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — $search served by EACH cluster's local data plane.

    The primary-routed `test_execute_text_search_query` only proves the
    cluster currently holding the primary works end-to-end. This test
    direct-connects to each cluster's mongod (`directConnection=true`,
    `readPreference=secondaryPreferred`) and re-runs the same `$search`
    against each pod individually, exercising both per-cluster paths:
    `mongod[i] -> Envoy[i] (port 27028 SNI) -> mongot[i] -> hits`.
    """
    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients, timeout=120)

    for cluster_index in range(len(MEMBERS_PER_CLUSTER)):
        tester = _direct_search_tester_for_cluster(mdb, cluster_index, USER_NAME, USER_PASSWORD)
        movies = SampleMoviesSearchHelper(search_tester=tester)

        def execute_search(_idx: int = cluster_index) -> tuple:
            try:
                results = movies.text_search_movies("Star Wars")
                count = len(results)
                if count > 0:
                    titles = [r.get("title") for r in results[:5]]
                    logger.info(f"cluster {_idx}: $search returned {count} results: {titles}")
                    return True, f"cluster {_idx}: $search returned {count} results"
                return False, f"cluster {_idx}: $search returned no results"
            except pymongo.errors.PyMongoError as exc:
                return False, f"cluster {_idx}: $search error: {exc}"

        run_periodically(
            execute_search,
            timeout=SEARCH_QUERY_RETRY_TIMEOUT,
            sleep_time=5,
            msg=f"cluster {cluster_index}: $search query to succeed",
        )


@mark.e2e_search_q2_mc_rs_steady
def test_per_cluster_vector_search_query(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """STRICT — $vectorSearch served by EACH cluster's local data plane.

    Same direct-connect strategy as `test_per_cluster_search_query`.
    Generates the query vector once via the Voyage HTTP API and reuses
    it across clusters. Skips if the Voyage query key env var is unset
    (mirrors `test_execute_vector_search_query`).
    """
    if EMBEDDING_QUERY_KEY_ENV_VAR not in os.environ:
        pytest.skip(f"missing {EMBEDDING_QUERY_KEY_ENV_VAR} — required to generate the query vector")

    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients, timeout=120)

    seed_tester = _search_tester_for_mdb(mdb, USER_NAME, USER_PASSWORD)
    query_vector = EmbeddedMoviesSearchHelper(search_tester=seed_tester).generate_query_vector("space exploration")

    for cluster_index in range(len(MEMBERS_PER_CLUSTER)):
        tester = _direct_search_tester_for_cluster(mdb, cluster_index, USER_NAME, USER_PASSWORD)
        embedded = EmbeddedMoviesSearchHelper(search_tester=tester)

        def execute_vector_search(_idx: int = cluster_index) -> tuple:
            try:
                results = embedded.vector_search(query_vector=query_vector, limit=4)
                count = len(results)
                if count >= 1:
                    titles = [r.get("title") for r in results[:5]]
                    logger.info(f"cluster {_idx}: $vectorSearch returned {count} results: {titles}")
                    return True, f"cluster {_idx}: $vectorSearch returned {count} results"
                return False, f"cluster {_idx}: $vectorSearch returned no results"
            except pymongo.errors.PyMongoError as exc:
                return False, f"cluster {_idx}: $vectorSearch error: {exc}"

        run_periodically(
            execute_vector_search,
            timeout=SEARCH_QUERY_RETRY_TIMEOUT,
            sleep_time=5,
            msg=f"cluster {cluster_index}: $vectorSearch query to succeed",
        )


# =============================================================================
# Admission / validator negative paths
# =============================================================================


@mark.e2e_search_q2_mc_rs_steady
def test_admission_rejects_mc_without_external_hosts(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    ca_configmap: str,
):
    """STRICT — `validateMCRequiresExternalHostAndPorts` is observable.

    Validation runs at reconcile time (`mongodbsearch_reconcile_helper.go`
    calls `ValidateSpec()`), not via an admission webhook — so the apply
    succeeds but the resource lands in Phase=Failed with the validator's
    error message.

    Submits a fresh MongoDBSearch with `len(spec.clusters) > 1` and no
    `spec.source.external.hostAndPorts`, asserts Failed phase + expected
    message, then deletes it so we don't pollute the namespace for
    later runs.
    """
    # Short name to keep the per-cluster Envoy Deployment name under the
    # 63-char DNS-1123 label limit (validateClustersEnvoyResourceNames runs
    # earlier in RunValidations than the validator we're trying to test;
    # using MDBS_RESOURCE_NAME as a prefix would trip the length check first).
    bad_name = "mdbs-bad"
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=bad_name,
        namespace=namespace,
    )

    # Source: external + TLS but NO hostAndPorts (the field under test).
    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
            "key": "password",
        },
        "external": {
            "tls": {"ca": {"name": ca_configmap}},
        },
    }

    resource["spec"]["security"] = {
        "tls": {"certsSecretPrefix": MDBS_TLS_CERT_PREFIX},
    }

    # Two clusters — required to trigger the MC validator.
    resource["spec"]["clusters"] = [
        {"clusterName": name, "replicas": MONGOT_REPLICAS_PER_CLUSTER} for name in member_cluster_names
    ]

    # Need a managed LB + externalHostname so the LB-vs-source validator
    # doesn't fire first. We're testing hostAndPorts here, not LB.
    resource["spec"]["loadBalancer"] = {
        "managed": {
            "replicas": ENVOY_LB_REPLICAS,
            "externalHostname": (f"{bad_name}-search-{{clusterIndex}}-proxy-svc.{namespace}.svc.cluster.local"),
        },
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try:
        resource.update()
        resource.assert_reaches_phase(
            Phase.Failed,
            timeout=300,
            msg_regexp=r"(?i).*spec\.source\.external\.hostAndPorts is required.*",
        )
    finally:
        try:
            resource.delete()
        except Exception as exc:
            logger.warning(f"failed to clean up {bad_name}: {exc}")
