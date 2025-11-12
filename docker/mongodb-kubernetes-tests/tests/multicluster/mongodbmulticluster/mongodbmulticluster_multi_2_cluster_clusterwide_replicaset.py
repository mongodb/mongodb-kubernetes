from typing import Dict, List

import kubernetes
import pytest
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    read_configmap,
    read_secret,
)
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster import prepare_multi_cluster_namespaces
from tests.multicluster.conftest import cluster_spec_list, create_namespace

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"


@pytest.fixture(scope="module")
def mdba_ns(namespace: str):
    return "{}-mdb-ns-a".format(namespace)


@pytest.fixture(scope="module")
def mdbb_ns(namespace: str):
    return "{}-mdb-ns-b".format(namespace)


@pytest.fixture(scope="module")
def mongodb_multi_a_unmarshalled(
    central_cluster_client: kubernetes.client.ApiClient,
    mdba_ns: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), "multi-replica-set", mdba_ns)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1])
    resource.set_version(ensure_ent_version(custom_mdb_version))

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@pytest.fixture(scope="module")
def mongodb_multi_b_unmarshalled(
    central_cluster_client: kubernetes.client.ApiClient,
    mdbb_ns: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), "multi-replica-set", mdbb_ns)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1])
    resource.set_version(ensure_ent_version(custom_mdb_version))

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@pytest.fixture(scope="module")
def server_certs_a(
    multi_cluster_clusterissuer: str,
    mongodb_multi_a_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_clusterissuer,
        f"{CERT_SECRET_PREFIX}-{mongodb_multi_a_unmarshalled.name}-cert",
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_a_unmarshalled,
        clusterwide=True,
    )


@pytest.fixture(scope="module")
def server_certs_b(
    multi_cluster_clusterissuer: str,
    mongodb_multi_b_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_clusterissuer,
        f"{CERT_SECRET_PREFIX}-{mongodb_multi_b_unmarshalled.name}-cert",
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_b_unmarshalled,
        clusterwide=True,
    )


@pytest.fixture(scope="module")
def mongodb_multi_a(
    central_cluster_client: kubernetes.client.ApiClient,
    mdba_ns: str,
    server_certs_a: str,
    mongodb_multi_a_unmarshalled: MongoDBMulti,
    issuer_ca_filepath: str,
) -> MongoDBMulti:
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}
    name = "issuer-ca"

    create_or_update_configmap(mdba_ns, name, data, api_client=central_cluster_client)

    resource = mongodb_multi_a_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": name,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@pytest.fixture(scope="module")
def mongodb_multi_b(
    central_cluster_client: kubernetes.client.ApiClient,
    mdbb_ns: str,
    server_certs_b: str,
    mongodb_multi_b_unmarshalled: MongoDBMulti,
    issuer_ca_filepath: str,
) -> MongoDBMulti:
    ca = open(issuer_ca_filepath).read()

    # The operator expects the CA that validates Ops Manager is contained in
    # an entry with a name of "mms-ca.crt"
    data = {"ca-pem": ca, "mms-ca.crt": ca}
    name = "issuer-ca"

    create_or_update_configmap(mdbb_ns, name, data, api_client=central_cluster_client)

    resource = mongodb_multi_b_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": name,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_create_kube_config_file(cluster_clients: Dict, member_cluster_names: List[str]):
    clients = cluster_clients

    assert len(clients) == 2
    assert member_cluster_names[0] in clients
    assert member_cluster_names[1] in clients


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_create_namespaces(
    namespace: str,
    mdba_ns: str,
    mdbb_ns: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    evergreen_task_id: str,
    multi_cluster_operator_installation_config: Dict[str, str],
):
    image_pull_secret_name = multi_cluster_operator_installation_config["registry.imagePullSecrets"]
    image_pull_secret_data = read_secret(namespace, image_pull_secret_name, api_client=central_cluster_client)

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        mdba_ns,
        image_pull_secret_name,
        image_pull_secret_data,
    )

    create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        mdbb_ns,
        image_pull_secret_name,
        image_pull_secret_data,
    )


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_deploy_operator(multi_cluster_operator_clustermode: Operator):
    multi_cluster_operator_clustermode.assert_is_running()


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_prepare_namespace(
    multi_cluster_operator_installation_config: Dict[str, str],
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_name: str,
    mdba_ns: str,
    mdbb_ns: str,
):
    prepare_multi_cluster_namespaces(
        mdba_ns,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
        skip_central_cluster=False,
    )

    prepare_multi_cluster_namespaces(
        mdbb_ns,
        multi_cluster_operator_installation_config,
        member_cluster_clients,
        central_cluster_name,
        skip_central_cluster=False,
    )


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_copy_configmap_and_secret_across_ns(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_operator_installation_config: Dict[str, str],
    mdba_ns: str,
    mdbb_ns: str,
):
    data = read_configmap(namespace, "my-project", api_client=central_cluster_client)
    data["projectName"] = mdba_ns
    create_or_update_configmap(mdba_ns, "my-project", data, api_client=central_cluster_client)

    data["projectName"] = mdbb_ns
    create_or_update_configmap(mdbb_ns, "my-project", data, api_client=central_cluster_client)

    data = read_secret(namespace, "my-credentials", api_client=central_cluster_client)
    create_or_update_secret(mdba_ns, "my-credentials", data, api_client=central_cluster_client)
    create_or_update_secret(mdbb_ns, "my-credentials", data, api_client=central_cluster_client)


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_create_mongodb_multi_nsa(mongodb_multi_a: MongoDBMulti):
    mongodb_multi_a.assert_reaches_phase(Phase.Running, timeout=800)


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_enable_mongodb_multi_nsa_auth(mongodb_multi_a: MongoDBMulti):
    mongodb_multi_a.reload()
    mongodb_multi_a["spec"]["authentication"] = (
        {
            "agents": {"mode": "SCRAM"},
            "enabled": True,
            "modes": ["SCRAM"],
        },
    )


@pytest.mark.e2e_multi_cluster_2_clusters_clusterwide
def test_create_mongodb_multi_nsb(mongodb_multi_b: MongoDBMulti):
    mongodb_multi_b.assert_reaches_phase(Phase.Running, timeout=800)
