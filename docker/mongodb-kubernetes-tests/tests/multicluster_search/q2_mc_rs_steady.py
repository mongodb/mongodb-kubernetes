"""Q2-MC-RS e2e: 2-cluster MongoDBSearch backed by an external MongoDBMulti RS.

Exercises per-cluster fan-out in the MC reconciler: each cluster gets its own
mongot StatefulSet, headless Service, ConfigMap, and Envoy Deployment, all
written through the owning member-cluster client. The Envoy SNI config and the
per-process mongotHost AC patch are verified on-disk so we catch regressions in
Envoy fan-out and per-cluster mongotHost locality — not just that the resources
were constructed, but that they resolved correctly at runtime.

Also covers per-cluster `syncSourceSelector.matchTagSets` -> mongot `replicationReader.tagSets`
(members tagged `nodeLocation`; each cluster's tag set selects its local members), asserted
in the per-cluster mongot ConfigMap. Config-render is image-independent; the data-plane effect
is exercised opportunistically when the deployed mongot image supports replicationReader.
"""

import json
import os
import re
from typing import Dict, List

import kubernetes
import pymongo.errors
import pytest
import yaml
from kubernetes.client import CoreV1Api
from kubetester import create_or_update_configmap, create_or_update_secret, read_secret, try_load
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
from tests.common.multicluster.multicluster_utils import assert_deployment_ready_in_cluster
from tests.common.search import search_resource_names
from tests.common.search.connectivity import CLUSTER_LOCATION_TAG_KEY
from tests.common.search.movies_search_helper import (
    EMBEDDING_QUERY_KEY_ENV_VAR,
    EmbeddedMoviesSearchHelper,
    SampleMoviesSearchHelper,
)
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_issuer_ca_filepath
from tests.multicluster.conftest import cluster_spec_list

logger = test_logger.get_test_logger(__name__)

MDB_RESOURCE_NAME = "mdb-mc-rs-ext-lb"
MDBS_RESOURCE_NAME = "mdb-mc-rs-ext-lb-search"

# 2-cluster MC RS, 2 members each. Typed as `List[int | None]` to match
# cluster_spec_list's signature; strict invariance rejects `List[int]`.
MEMBERS_PER_CLUSTER: List[int | None] = [2, 2]

MONGOT_REPLICAS_PER_CLUSTER = 1
ENVOY_LB_REPLICAS = 2
ENVOY_PROXY_PORT = 27028

# Owner labels stamped on every per-cluster resource (pkg/handler/labels_search.go).
# Used for cross-cluster watch routing by MapMemberClusterObjectToSearch.
SEARCH_OWNER_NAME_LABEL = "mongodb.com/search-name"
SEARCH_OWNER_NAMESPACE_LABEL = "mongodb.com/search-namespace"
SEARCH_CLUSTER_NAME_LABEL = "mongodb.com/cluster-name"


def _idx(mcc: MultiClusterClient) -> int:
    """Narrow ``mcc.cluster_index`` (Optional[int]) to int for the resource-name helpers."""
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


def _assert_search_owner_labels(obj_labels: Dict[str, str], cluster_name: str, where: str) -> None:
    """Assert all three search-owner labels are present and correct on a per-cluster resource."""
    assert (
        obj_labels.get(SEARCH_OWNER_NAME_LABEL) == MDBS_RESOURCE_NAME
    ), f"{where}: missing/wrong {SEARCH_OWNER_NAME_LABEL!r}; got {obj_labels.get(SEARCH_OWNER_NAME_LABEL)!r}"
    # The operator stamps the search's namespace, not the central namespace, so this
    # equals `namespace` (the test-time namespace where both CR and members run).
    assert obj_labels.get(SEARCH_OWNER_NAMESPACE_LABEL), f"{where}: missing {SEARCH_OWNER_NAMESPACE_LABEL!r}"
    assert obj_labels.get(SEARCH_CLUSTER_NAME_LABEL) == cluster_name, (
        f"{where}: missing/wrong {SEARCH_CLUSTER_NAME_LABEL!r}; got "
        f"{obj_labels.get(SEARCH_CLUSTER_NAME_LABEL)!r}, want {cluster_name!r}"
    )


ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

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
    # Tag every member `nodeLocation=<clusterName>` — this is what each cluster's mongot
    # matchTagSets selects below, so cluster-i's mongot reads from cluster-i's local members.
    member_configs = [
        [{"tags": {CLUSTER_LOCATION_TAG_KEY: name}} for _ in range(count or 0)]
        for name, count in zip(member_cluster_names, MEMBERS_PER_CLUSTER)
    ]
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        member_cluster_names, MEMBERS_PER_CLUSTER, member_configs=member_configs
    )

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

    spec.source.external.hostAndPorts is the same top-level seed list rendered to
    every cluster's mongot ConfigMap. Each cluster's loadBalancer.managed.externalHostname
    carries its own index so all three per-cluster names (cert SANs, AC mongotHost,
    Envoy SNI) resolve to the same per-cluster proxy-svc FQDN.
    """
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-q2-mc-rs-search.yaml"),
        name=MDBS_RESOURCE_NAME,
        namespace=namespace,
    )

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

    # Per-cluster matchTagSets -> mongot syncSource.replicationReader.tagSets. spec.version is left
    # unpinned on purpose: the operator then uses the pipeline's dev image (MDB_SEARCH_VERSION,
    # a mongot-master build whose replicationReader understands tagSets), whereas pinning a
    # public release tag here would ImagePullBackOff (the e2e ECR only carries dev tags).
    clusters = []
    for mcc in member_cluster_clients:
        assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
        clusters.append(
            {
                "name": mcc.cluster_name,
                "index": mcc.cluster_index,
                "replicas": MONGOT_REPLICAS_PER_CLUSTER,
                "loadBalancer": {
                    "managed": {
                        "externalHostname": search_resource_names.mc_proxy_svc_fqdn(
                            MDBS_RESOURCE_NAME, namespace, _idx(mcc)
                        ),
                    },
                },
                "syncSourceSelector": {"matchTagSets": [{CLUSTER_LOCATION_TAG_KEY: mcc.cluster_name}]},
            }
        )
    resource["spec"]["clusters"] = clusters

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
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


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
    """Assert MongoDBMulti reaches Running."""
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
    """Managed LB server + client certs, with SANs covering every cluster's proxy-svc FQDN.

    The LB cert SAN validator requires every cluster's proxy-svc FQDN in the cert;
    producing them here keeps the test honest when the validator is wired in.
    """
    server_domains = [
        f"{MDBS_RESOURCE_NAME}-search-{helper.cluster_index(name)}-proxy-svc.{namespace}.svc.cluster.local"
        for name in helper.member_cluster_names()
    ]

    for name in helper.member_cluster_names():
        ci = helper.cluster_index(name)
        deployment_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME, ci)
        lb_server_cert_name = search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci)
        lb_client_cert_name = search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci)

        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=deployment_name,
            replicas=ENVOY_LB_REPLICAS,
            service_name=deployment_name,
            additional_domains=server_domains,
            secret_name=lb_server_cert_name,
        )
        logger.info(f"LB server certificate created with SANs={server_domains}: {lb_server_cert_name}")

        create_tls_certs(
            issuer=multi_cluster_issuer,
            namespace=namespace,
            resource_name=f"{deployment_name}-client",
            replicas=1,
            service_name=deployment_name,
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
    """Replicate TLS Secrets, mongot password, and CA ConfigMap to every member cluster.

    MCK does not replicate Secrets in production — that's the customer's responsibility.
    Without this step, mongot pods in member clusters stay PodInitializing forever.
    """
    central_core = CoreV1Api(api_client=central_cluster_client)

    def _copy(secret_name: str, targets: List[MultiClusterClient]) -> None:
        source = central_core.read_namespaced_secret(name=secret_name, namespace=namespace)
        data = read_secret(namespace, secret_name, api_client=central_cluster_client)
        for mcc in targets:
            create_or_update_secret(
                namespace, secret_name, data, type=source.type or "Opaque", api_client=mcc.api_client
            )

    # Shared Secrets — same copy to every member cluster.
    shared_secrets = [
        search_resource_names.mongot_tls_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX),
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
    ]
    for secret_name in shared_secrets:
        _copy(secret_name, member_cluster_clients)
        logger.info(f"replicated Secret {secret_name} to {len(member_cluster_clients)} member cluster(s)")

    # Per-cluster LB certs — each member only needs the cert matching its own Envoy.
    for mcc in member_cluster_clients:
        ci = _idx(mcc)
        for secret_name in (
            search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci),
            search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX, ci),
        ):
            _copy(secret_name, [mcc])
            logger.info(f"replicated per-cluster Secret {secret_name} into cluster {mcc.cluster_name}")

    # CA ConfigMap — mongot verifies the source RS TLS cert against this CA.
    source_cm = central_core.read_namespaced_config_map(name=CA_CONFIGMAP_NAME, namespace=namespace)
    cm_data = dict(source_cm.data or {})
    for mcc in member_cluster_clients:
        create_or_update_configmap(namespace, CA_CONFIGMAP_NAME, cm_data, api_client=mcc.api_client)
        logger.info(f"replicated CA ConfigMap {CA_CONFIGMAP_NAME} into cluster {mcc.cluster_name}")


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    """Assert MongoDBSearch reaches Running — all per-cluster resources must converge first."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


def _per_cluster_mongot_config_name(mdbs_name: str, cluster_index: int) -> str:
    """Mirror MongotConfigConfigMapNameForCluster."""
    return f"{mdbs_name}-search-{cluster_index}-config"


def _per_cluster_envoy_deployment_name(mdbs_name: str, cluster_index: int) -> str:
    """Mirror LoadBalancerDeploymentNameForCluster: `{name}-search-lb-{clusterIndex}`."""
    return f"{mdbs_name}-search-lb-{cluster_index}"


def _per_cluster_envoy_configmap_name(mdbs_name: str, cluster_index: int) -> str:
    """Mirror LoadBalancerConfigMapNameForCluster: `{name}-search-lb-{clusterIndex}-config`."""
    return f"{mdbs_name}-search-lb-{cluster_index}-config"


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
    mdb: MongoDBMulti,
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Each cluster has its own mongot StatefulSet, Service, ConfigMap.

    Strict checks:
    - per-cluster resources exist on the owning member-cluster client
    - every per-cluster write site carries the three canonical owner labels
      (search-name, search-namespace, cluster-name) — used by
      MapMemberClusterObjectToSearch for cross-cluster watch routing; cross-
      cluster owner refs do not GC, so labels are the actual provenance link
    - no per-cluster write site (incl. the operator TLS secret) carries
      ownerReferences — a ref to the central CR's UID would get the resource
      garbage-collected by the member cluster (KUBE-154)
    - the mongot ConfigMap on each cluster lists `spec.source.external.hostAndPorts`
      from the seed list (Phase 2 fan-out: top-level external host list is
      rendered into every cluster's mongot CM)
    - each cluster's mongot ConfigMap carries a `replicationReader.tagSets` derived from
      its `syncSourceSelector.matchTagSets`, under a non-primary read preference
    """
    expected_hosts = sorted(f"{svc}.{namespace}.svc.cluster.local:27017" for svc in mdb.service_names())

    for mcc in member_cluster_clients:
        idx = helper.cluster_index(mcc.cluster_name)
        sts_name = f"{MDBS_RESOURCE_NAME}-search-{idx}"
        svc_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-svc"
        cm_name = _per_cluster_mongot_config_name(MDBS_RESOURCE_NAME, idx)
        proxy_svc_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-proxy-svc"

        sts = mcc.read_namespaced_stateful_set(sts_name, namespace)
        headless = mcc.read_namespaced_service(svc_name, namespace)
        proxy = mcc.read_namespaced_service(proxy_svc_name, namespace)
        cm = mcc.read_namespaced_config_map(cm_name, namespace)
        _assert_search_owner_labels(sts.metadata.labels or {}, mcc.cluster_name, f"STS {sts_name}")
        _assert_search_owner_labels(headless.metadata.labels or {}, mcc.cluster_name, f"headless Service {svc_name}")
        _assert_search_owner_labels(proxy.metadata.labels or {}, mcc.cluster_name, f"proxy Service {proxy_svc_name}")
        _assert_search_owner_labels(cm.metadata.labels or {}, mcc.cluster_name, f"mongot CM {cm_name}")

        # KUBE-154: member-cluster writes carry NO ownerReferences — the central CR's UID
        # doesn't exist in the member cluster, so a ref would get these resources GC'd.
        # The operator-managed cert+key Secret is written per member cluster by the same path.
        tls_secret_name = f"{MDBS_RESOURCE_NAME}-search-certificate-key"
        tls_secret = mcc.core_v1_api().read_namespaced_secret(tls_secret_name, namespace)
        for obj, where in (
            (sts, f"STS {sts_name}"),
            (headless, f"headless Service {svc_name}"),
            (proxy, f"proxy Service {proxy_svc_name}"),
            (cm, f"mongot CM {cm_name}"),
            (tls_secret, f"operator TLS Secret {tls_secret_name}"),
        ):
            refs = obj.metadata.owner_references or []
            assert not refs, f"{where} in cluster {mcc.cluster_name}: unexpected ownerReferences {refs}"

        config_yaml = cm.data.get("config.yml") or cm.data.get("mongot.yaml")
        assert config_yaml, f"mongot CM {cm_name} missing config payload; data keys={list(cm.data or {})}"
        parsed = yaml.safe_load(config_yaml)
        cm_hosts = parsed.get("syncSource", {}).get("replicaSet", {}).get("hostAndPort", [])
        assert sorted(cm_hosts) == expected_hosts, (
            f"mongot CM {cm_name} in cluster {mcc.cluster_name}: hostAndPort {sorted(cm_hosts)} "
            f"!= expected seed list {expected_hosts}"
        )

        # matchTagSets -> replicationReader.tagSets, derived from this cluster's syncSourceSelector
        # under a non-primary read pref. Written before pod readiness, so this holds even if the
        # mongot image predates replicationReader support (the CR just won't reach Running then).
        rr = parsed.get("syncSource", {}).get("replicationReader")
        expected_tag_sets = [[{"name": CLUSTER_LOCATION_TAG_KEY, "value": mcc.cluster_name}]]
        assert rr is not None, f"mongot CM {cm_name} in cluster {mcc.cluster_name}: syncSource.replicationReader absent"
        assert rr.get("readPreference") != "primary", (
            f"mongot CM {cm_name} in cluster {mcc.cluster_name}: readPreference is 'primary' "
            "(tagSets require a non-primary read preference)"
        )
        assert rr.get("tagSets") == expected_tag_sets, (
            f"mongot CM {cm_name} in cluster {mcc.cluster_name}: replicationReader.tagSets "
            f"{rr.get('tagSets')!r} != expected {expected_tag_sets!r}"
        )

        logger.info(
            f"per-cluster mongot resources verified in cluster {mcc.cluster_name} "
            f"(idx={idx}): {sts_name}, {svc_name}, {cm_name}, {proxy_svc_name}"
        )


@mark.e2e_search_q2_mc_rs_steady
def test_verify_mongot_config_server_name(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """The runtime mongot config inside each pod must have server.name set to the pod hostname.

    The operator writes a placeholder in the ConfigMap; the container start script copies
    the config to /tmp/config.yml and replaces the placeholder with $HOSTNAME via sed.
    """
    for mcc in member_cluster_clients:
        idx = helper.cluster_index(mcc.cluster_name)
        pod_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-0"

        raw = KubernetesTester.run_command_in_pod_container(
            pod_name,
            namespace,
            ["cat", "/tmp/config.yml"],
            container="mongot",
            api_client=mcc.api_client,
        )
        parsed = yaml.safe_load(raw) or {}
        server_name = parsed.get("server", {}).get("name", "")
        assert server_name == pod_name, (
            f"mongot pod {pod_name} in cluster {mcc.cluster_name}: "
            f"server.name={server_name!r}, expected {pod_name!r}"
        )
        logger.info(f"[{mcc.cluster_name}] {pod_name}: server.name={server_name} OK")


@mark.e2e_search_q2_mc_rs_steady
def test_per_cluster_mongot_sync_source_is_cluster_local(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Each cluster's mongot must have selected its initial sync host from its own
    cluster's members only, verified from the 'Selected initial sync host' lines in
    the mongot logs — proves replicationReader.tagSets actually constrained sync-source
    selection at runtime, not just that the config rendered.
    """
    host_pattern = re.compile(rf"({re.escape(MDB_RESOURCE_NAME)}-(\d+)-\d+-svc\.[^\"]*)")

    def check() -> tuple:
        all_correct = True
        msgs: List[str] = []
        for mcc in member_cluster_clients:
            idx = helper.cluster_index(mcc.cluster_name)
            pod_name = f"{MDBS_RESOURCE_NAME}-search-{idx}-0"
            try:
                log = KubernetesTester.read_pod_logs(namespace, pod_name, container="mongot", api_client=mcc.api_client)
            except Exception as exc:
                all_correct = False
                msgs.append(f"[{mcc.cluster_name}] {pod_name}: error reading mongot log: {exc}")
                continue

            total = 0
            foreign_hosts: List[str] = []
            for line in log.splitlines():
                if "Selected initial sync host" not in line:
                    continue
                total += 1
                m = host_pattern.search(line)
                if m is None or int(m.group(2)) != idx:
                    foreign_hosts.append(m.group(1) if m else line.strip())

            if total == 0:
                all_correct = False
                msgs.append(f"[{mcc.cluster_name}] {pod_name}: no 'Selected initial sync host' lines in mongot log")
            elif foreign_hosts:
                all_correct = False
                msgs.append(
                    f"[{mcc.cluster_name}] {pod_name}: {len(foreign_hosts)}/{total} sync-source selections "
                    f"are not cluster-local (want source cluster {idx}): {sorted(set(foreign_hosts))}"
                )
            else:
                msgs.append(f"[{mcc.cluster_name}] {pod_name}: all {total} sync-source selections cluster-local")
        return all_correct, "\n".join(msgs)

    run_periodically(check, timeout=120, sleep_time=10, msg="per-cluster mongot sync-source selections cluster-local")
    logger.info("per-cluster mongot sync-source locality verified from mongot logs")


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_envoy_deployment(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Every cluster's Envoy Deployment and ConfigMap must exist, be fully ready,
    and carry owner labels for cross-cluster watch routing.
    """
    for mcc in member_cluster_clients:
        cluster_idx = helper.cluster_index(mcc.cluster_name)
        envoy_deployment_name = _per_cluster_envoy_deployment_name(MDBS_RESOURCE_NAME, cluster_idx)
        envoy_cm_name = _per_cluster_envoy_configmap_name(MDBS_RESOURCE_NAME, cluster_idx)
        apps = mcc.apps_v1_api()
        assert_deployment_ready_in_cluster(apps, name=envoy_deployment_name, namespace=namespace)
        envoy_deploy = apps.read_namespaced_deployment(name=envoy_deployment_name, namespace=namespace)
        envoy_cm = mcc.read_namespaced_config_map(envoy_cm_name, namespace)
        _assert_search_owner_labels(
            envoy_deploy.metadata.labels or {}, mcc.cluster_name, f"Envoy Deployment {envoy_deployment_name}"
        )
        _assert_search_owner_labels(envoy_cm.metadata.labels or {}, mcc.cluster_name, f"Envoy CM {envoy_cm_name}")

        logger.info(f"Envoy Deployment {envoy_deployment_name} ready in cluster {mcc.cluster_name} (idx={cluster_idx})")


@mark.e2e_search_q2_mc_rs_steady
def test_verify_lb_status(mdbs: MongoDBSearch):
    """status.loadBalancer.phase must be Running (aggregated from per-cluster Envoy phases)."""
    mdbs.load()
    mdbs.assert_lb_status()


@mark.e2e_search_q2_mc_rs_steady
def test_patch_per_cluster_mongot_host(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Patch the OM automation config so each cluster's mongods use their cluster-local
    Envoy proxy-svc FQDN for mongotHost and searchIndexManagementHostAndPort.

    MongoDBMultiCluster does not yet expose per-cluster additionalMongodConfig, so the
    per-process AC keys are set directly. Leaving them out of spec ensures subsequent
    operator reconciles never clobber this per-cluster locality.
    """
    om_tester = mdb.get_om_tester()
    ac_path = f"/groups/{om_tester.context.project_id}/automationConfig"
    ac = om_tester.om_request("get", ac_path).json()

    proxy_by_cluster_idx = {
        helper.cluster_index(mcc.cluster_name): f"{helper.proxy_svc_fqdn(mcc.cluster_name)}:{ENVOY_PROXY_PORT}"
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


@mark.e2e_search_q2_mc_rs_steady
def test_per_cluster_mongot_host_observed(
    mdb: MongoDBMulti,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Verify the AC patch landed on disk: each cluster's first mongod must show the
    cluster-local proxy-svc FQDN in mongotHost and searchIndexManagementHostAndPort.
    """
    _assert_per_cluster_mongot_host_observed(mdb, helper, member_cluster_clients)


@mark.e2e_search_q2_mc_rs_steady
def test_per_cluster_envoy_sni_observed(
    namespace: str,
    helper: MCSearchDeploymentHelper,
    member_cluster_clients: List[MultiClusterClient],
):
    """Each per-cluster Envoy ConfigMap's lds.json must reference exactly the
    cluster-local proxy-svc FQDN in its SNI server_names — no cross-cluster leakage.
    """
    for mcc in member_cluster_clients:
        cluster_idx = helper.cluster_index(mcc.cluster_name)
        cm_name = _per_cluster_envoy_configmap_name(MDBS_RESOURCE_NAME, cluster_idx)
        expected_fqdn = search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, cluster_idx)

        cm = mcc.core_v1_api().read_namespaced_config_map(name=cm_name, namespace=namespace)
        lds_json = (cm.data or {}).get("lds.json")
        assert lds_json, f"lds.json missing in ConfigMap {cm_name} ({mcc.cluster_name})"

        # Parse and walk filter_chains[].filter_chain_match.server_names[]
        # for the per-cluster FQDN. Avoids substring false-positives from
        # the upstream-host references that share the namespace prefix.
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
            other_fqdn = search_resource_names.mc_proxy_svc_fqdn(MDBS_RESOURCE_NAME, namespace, other_idx)
            assert other_fqdn not in sni_names, (
                f"[{mcc.cluster_name}] foreign SNI {other_fqdn!r} present in "
                f"lds.json server_names — per-cluster Envoy must only match its own FQDN"
            )

        logger.info(f"[{mcc.cluster_name}] lds.json SNI server_names={sni_names} (expected match: {expected_fqdn})")


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


# Generous timeout to account for cross-cluster mesh latency during index sync.
SEARCH_INDEX_READY_TIMEOUT = 300
# Mongot may still be in INITIAL_SYNC briefly after reporting READY.
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
    """Create the $search index, re-asserting per-cluster mongotHost first so any
    silent operator re-reconcile that clobbers the AC patch surfaces here.
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
    """$search must return non-empty results. Wrapped in run_periodically since mongot
    may still be in INITIAL_SYNC briefly after the index reports READY.
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
    """Create a $vectorSearch index on the pre-embedded embedded_movies collection.

    Uses plot_embedding_voyage_3_large (already in the sample dataset) so no Voyage
    API key is needed for indexing. The auto-embedding variant is deferred — it
    requires cross-cluster leader election not yet supported.
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
    """$vectorSearch must return non-empty results using a query vector from the Voyage API.
    Skips if the Voyage query key env var is unset.
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
    """$search direct-connected to each cluster's mongod, exercising every per-cluster
    Envoy+mongot path — not just the cluster currently holding the primary.
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
    """$vectorSearch direct-connected to each cluster's mongod, same strategy as
    test_per_cluster_search_query. Skips if the Voyage query key env var is unset.
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
