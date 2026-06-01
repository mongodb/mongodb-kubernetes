"""Shared fixtures and helpers for the MC-Search e2e tests.

Mirrors `tests/search/conftest.py` for the tools_pod fixture so the
data-plane tests have a pod for `mongorestore` runs. Other shared
fixtures (namespace, central_cluster_client, member_cluster_clients,
member_cluster_names, multi_cluster_operator) come from
`tests/conftest.py` and `tests/multicluster/conftest.py`.

Also exposes simulated-MC helpers/constants shared between the
RS (`simulated_mc_basic.py`) and sharded (`simulated_mc_sharded_basic.py`)
variants. Constants that differ per variant (e.g., MDB_RESOURCE_NAME,
MEMBERS_PER_CLUSTER, MONGOT_REPLICAS_PER_CLUSTER) stay in the test
modules. Helpers that close over per-variant constants take them as
parameters here.
"""

from __future__ import annotations

import os
from typing import Dict, List

import kubernetes
from kubetester import create_or_update_secret, read_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod

logger = test_logger.get_test_logger(__name__)

# ---------------------------------------------------------------------------
# Shared simulated-MC constants
# ---------------------------------------------------------------------------

ENVOY_LB_REPLICAS = 1
ENVOY_PROXY_PORT = 27028
SIMULATED_OPERATOR_NAME = "mongodb-kubernetes-operator-simulated"

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

# Mongot/LB server+client cert prefix on the MongoDBSearch CR; source MongoDB cert prefix.
MDBS_TLS_CERT_PREFIX = "certs"
SOURCE_CERT_PREFIX = "clustercert"


@fixture(scope="module")
def tools_pod(namespace: str) -> mongodb_tools_pod.ToolsPod:
    return mongodb_tools_pod.get_tools_pod(namespace)


# ---------------------------------------------------------------------------
# Shared simulated-MC helpers
# ---------------------------------------------------------------------------


def _idx(mcc: MultiClusterClient) -> int:
    assert mcc.cluster_index is not None, f"cluster_index unset on {mcc.cluster_name!r}"
    return mcc.cluster_index


def _install_simulated_operator(
    namespace: str,
    base_helm_args: Dict[str, str],
    mcc: MultiClusterClient,
) -> Operator:
    """Install a simulated-MC operator watching mongodbsearch only.
    Uses a DISTINCT operator.name so Helm renders fresh SA+Role+RoleBinding with
    mongodbsearch RBAC (the kube-config-creation-tool SA's Role lacks search perms).
    """
    os.environ["HELM_KUBECONTEXT"] = mcc.cluster_name

    helm_args = dict(base_helm_args)
    # multiCluster.clusters gates a kube-config-volume mount whose Secret is never created in this mode.
    for k in (
        "operator.watchNamespace",
        "multiCluster.clusters",
        "multiCluster.kubeConfigSecretName",
        "multiCluster.clusterClientTimeout",
        "multiCluster.performFailover",
    ):
        helm_args.pop(k, None)

    helm_args["operator.name"] = SIMULATED_OPERATOR_NAME
    helm_args["operator.createOperatorServiceAccount"] = "true"
    # MongoDB-resource-pod SAs are pre-created by run_kube_config_creation_tool — Helm 3
    # ownership-metadata check fails if we re-render them.
    helm_args["operator.createResourcesServiceAccountsAndRoles"] = "false"
    helm_args["operator.clusterIdentity.clusterName"] = mcc.cluster_name
    helm_args["operator.watchedResources"] = "{mongodbsearch}"

    # .upgrade(multi_cluster=True) = `helm upgrade --install` semantics + skip
    # the validating-webhook wait. The simulated operator only watches
    # MongoDBSearch (no validating webhook for that CRD); the test never
    # applies a MongoDB CR against this operator. The test pod is in
    # kind-e2e-cluster-1 which != central_cluster_name (kind-e2e-operator),
    # so the multi_cluster=True branch at kubetester/operator.py:192 skips
    # the wait.
    operator = Operator(
        namespace=namespace,
        name=SIMULATED_OPERATOR_NAME,  # matches helm_args["operator.name"] for pod-label polling
        helm_args=helm_args,
        api_client=mcc.api_client,
    ).upgrade(multi_cluster=True)
    logger.info(
        f"installed simulated-MC operator in {mcc.cluster_name} "
        f"(identity={mcc.cluster_name}, name={SIMULATED_OPERATOR_NAME}, watches=mongodbsearch only)"
    )
    return operator


def _replicate_mongot_user_password_to_members(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    mdbs_resource_name: str,
) -> None:
    """Replicate the password Secret so simulated-MC operators' source.passwordSecretRef resolves locally."""
    # Kind e2e variant exposes 3 member clusters; this test exercises a 2-cluster
    # simulated-MC topology, so restrict to the first two members.
    member_cluster_clients = member_cluster_clients[:2]
    pwd_secret_name = f"{mdbs_resource_name}-{MONGOT_USER_NAME}-password"
    src = read_secret(namespace, pwd_secret_name, api_client=central_cluster_client)
    for mcc in member_cluster_clients:
        create_or_update_secret(namespace, pwd_secret_name, src, api_client=mcc.api_client)
        logger.info(f"replicated {pwd_secret_name} to {mcc.cluster_name}")


def _build_user(
    yaml_filename: str,
    name: str,
    username: str,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    mdb_resource_name: str,
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture(yaml_filename), namespace=namespace, name=name)
    if not try_load(resource):
        resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
        resource["spec"]["username"] = username
        resource["spec"]["passwordSecretKeyRef"]["name"] = f"{name}-password"
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource
