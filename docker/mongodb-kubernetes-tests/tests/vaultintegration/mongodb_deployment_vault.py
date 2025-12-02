import time
import uuid

import kubetester
import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import (
    create_configmap,
    delete_secret,
    get_statefulset,
    random_k8s_name,
    read_secret,
)
from kubetester.certs import (
    create_mongodb_tls_certs,
    create_x509_agent_tls_certs,
    create_x509_mongodb_tls_certs,
)
from kubetester.http import https_endpoint_is_reachable
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import get_pods, is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

from ..constants import DATABASE_SA_NAME, OPERATOR_NAME
from . import run_command_in_vault, store_secret_in_vault

MDB_RESOURCE = "my-replica-set"

USER_NAME = "my-user-1"
PASSWORD_SECRET_NAME = "mms-user-1-password"
USER_PASSWORD = "my-password"


def certs_for_prometheus(issuer: str, namespace: str, resource_name: str) -> str:
    secret_name = random_k8s_name(resource_name + "-") + "-prometheus-cert"

    return create_mongodb_tls_certs(
        issuer,
        namespace,
        resource_name,
        secret_name,
        secret_backend="Vault",
        vault_subpath="database",
    )


@fixture(scope="module")
def replica_set(
    namespace: str,
    custom_mdb_version: str,
    server_certs: str,
    agent_certs: str,
    clusterfile_certs: str,
    issuer_ca_configmap: str,
    issuer: str,
    vault_namespace: str,
    vault_name: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["security"] = {
        "tls": {"enabled": True, "ca": issuer_ca_configmap},
        "authentication": {
            "enabled": True,
            "modes": ["X509", "SCRAM"],
            "agents": {"mode": "X509"},
            "internalCluster": "X509",
        },
    }

    prom_cert_secret = certs_for_prometheus(issuer, namespace, resource.name)
    store_secret_in_vault(
        vault_namespace,
        vault_name,
        {"password": "prom-password"},
        f"secret/mongodbenterprise/operator/{namespace}/prom-password",
    )
    resource["spec"]["prometheus"] = {
        "username": "prom-user",
        "passwordSecretRef": {
            "name": "prom-password",
        },
        "tlsSecretKeyRef": {
            "name": prom_cert_secret,
        },
    }
    resource.create()

    return resource


@fixture(scope="module")
def agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE, secret_backend="Vault")


@fixture(scope="module")
def server_certs(vault_namespace: str, vault_name: str, namespace: str, issuer: str) -> str:
    create_x509_mongodb_tls_certs(
        issuer,
        namespace,
        MDB_RESOURCE,
        f"{MDB_RESOURCE}-cert",
        secret_backend="Vault",
        vault_subpath="database",
    )


@fixture(scope="module")
def clusterfile_certs(vault_namespace: str, vault_name: str, namespace: str, issuer: str) -> str:
    create_x509_mongodb_tls_certs(
        issuer,
        namespace,
        MDB_RESOURCE,
        f"{MDB_RESOURCE}-clusterfile",
        secret_backend="Vault",
        vault_subpath="database",
    )


@fixture(scope="module")
def sharded_cluster_configmap(namespace: str) -> str:
    cm = KubernetesTester.read_configmap(namespace, "my-project")
    epoch_time = int(time.time())
    project_name = "sharded-" + str(epoch_time) + "-" + uuid.uuid4().hex[0:10]
    data = {
        "baseUrl": cm["baseUrl"],
        "projectName": project_name,
        "orgId": cm["orgId"],
    }
    create_configmap(namespace=namespace, name=project_name, data=data)
    return project_name


@fixture(scope="module")
def sharded_cluster(
    namespace: str,
    sharded_cluster_configmap: str,
    issuer: str,
    vault_namespace: str,
    vault_name: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster.yaml"), namespace=namespace)
    resource["spec"]["cloudManager"]["configMapRef"]["name"] = sharded_cluster_configmap

    # Password stored in Prometheus
    store_secret_in_vault(
        vault_namespace,
        vault_name,
        {"password": "prom-password"},
        f"secret/mongodbenterprise/operator/{namespace}/prom-password-cluster",
    )

    # A prometheus certificate stored in Vault
    prom_cert_secret = certs_for_prometheus(issuer, namespace, resource.name)
    resource["spec"]["prometheus"] = {
        "username": "prom-user",
        "passwordSecretRef": {
            "name": "prom-password-cluster",
        },
        "tlsSecretKeyRef": {
            "name": prom_cert_secret,
        },
    }

    return resource.create()


@fixture(scope="module")
def mongodb_user(namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodb-user.yaml"), "vault-replica-set-scram-user", namespace)

    resource["spec"]["username"] = USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {
        "name": PASSWORD_SECRET_NAME,
        "key": "password",
    }

    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource["spec"]["mongodbResourceRef"]["namespace"] = namespace
    return resource.create()


@mark.e2e_vault_setup
def test_vault_creation(vault: str, vault_name: str, vault_namespace: str):
    vault

    # assert if vault statefulset is ready, this is sort of redundant(we already assert for pod phase)
    # but this is basic assertion at the moment, will remove in followup PR
    sts = get_statefulset(namespace=vault_namespace, name=vault_name)
    assert sts.status.ready_replicas == 1


@mark.e2e_vault_setup
def test_create_vault_operator_policy(vault_name: str, vault_namespace: str):
    # copy hcl file from local machine to pod
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/operator-policy.hcl",
        "/tmp/operator-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterprise",
        "/tmp/operator-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup
def test_enable_kubernetes_auth(vault_name: str, vault_namespace: str):
    # enable Kubernetes auth for Vault
    cmd = [
        "vault",
        "auth",
        "enable",
        "kubernetes",
    ]

    run_command_in_vault(
        vault_namespace,
        vault_name,
        cmd,
        expected_message=["Success!", "path is already in use at kubernetes"],
    )

    cmd = [
        "cat",
        "/var/run/secrets/kubernetes.io/serviceaccount/token",
    ]

    token = run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=[])

    cmd = ["env"]

    response = run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=[])

    response = response.split("\n")
    for line in response:
        l = line.strip()
        if str.startswith(l, "KUBERNETES_PORT_443_TCP_ADDR"):
            cluster_ip = l.split("=")[1]
            break

    cmd = [
        "vault",
        "write",
        "auth/kubernetes/config",
        f"token_reviewer_jwt={token}",
        f"kubernetes_host=https://{cluster_ip}:443",
        "kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
        "disable_iss_validation=true",
    ]

    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup
def test_enable_vault_role_for_operator_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_operator_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        "auth/kubernetes/role/mongodbenterprise",
        f"bound_service_account_names={OPERATOR_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_operator_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup
def test_operator_install_with_vault_backend(operator_vault_secret_backend: Operator):
    operator_vault_secret_backend.assert_is_running()


@mark.e2e_vault_setup
def test_vault_config_map_exists(namespace: str):
    # no exception should be raised
    KubernetesTester.read_configmap(namespace, name="secret-configuration")


@mark.e2e_vault_setup
def test_store_om_credentials_in_vault(vault_namespace: str, vault_name: str, namespace: str):
    credentials = read_secret(namespace, "my-credentials")
    store_secret_in_vault(
        vault_namespace,
        vault_name,
        credentials,
        f"secret/mongodbenterprise/operator/{namespace}/my-credentials",
    )

    cmd = [
        "vault",
        "kv",
        "get",
        f"secret/mongodbenterprise/operator/{namespace}/my-credentials",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd, expected_message=["publicApiKey"])
    delete_secret(namespace, "my-credentials")


@mark.e2e_vault_setup
def test_create_database_policy(vault_name: str, vault_namespace: str):
    KubernetesTester.copy_file_inside_pod(
        f"{vault_name}-0",
        "vaultpolicies/database-policy.hcl",
        "/tmp/database-policy.hcl",
        namespace=vault_namespace,
    )

    cmd = [
        "vault",
        "policy",
        "write",
        "mongodbenterprisedatabase",
        "/tmp/database-policy.hcl",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup
def test_enable_vault_role_for_database_pod(
    vault_name: str,
    vault_namespace: str,
    namespace: str,
    vault_database_policy_name: str,
):
    cmd = [
        "vault",
        "write",
        f"auth/kubernetes/role/{vault_database_policy_name}",
        f"bound_service_account_names={DATABASE_SA_NAME}",
        f"bound_service_account_namespaces={namespace}",
        f"policies={vault_database_policy_name}",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd)


@mark.e2e_vault_setup
def test_mdb_created(replica_set: MongoDB, namespace: str):
    replica_set.assert_reaches_phase(Phase.Running, timeout=500, ignore_errors=True)
    for pod_name in get_pods(MDB_RESOURCE + "-{}", 3):
        pod = client.CoreV1Api().read_namespaced_pod(pod_name, namespace)
        if is_default_architecture_static():
            assert len(pod.spec.containers) == 4
        else:
            assert len(pod.spec.containers) == 2


@mark.e2e_vault_setup
def test_rotate_server_certs(replica_set: MongoDB, vault_namespace: str, vault_name: str, namespace: str, issuer: str):
    replica_set.load()
    old_version = replica_set["metadata"]["annotations"]["agent-certs"]

    create_x509_mongodb_tls_certs(
        issuer,
        namespace,
        MDB_RESOURCE,
        f"{MDB_RESOURCE}-cert",
        secret_backend="Vault",
        vault_subpath="database",
    )

    replica_set.assert_abandons_phase(Phase.Running, timeout=600)

    def wait_for_server_certs() -> bool:
        replica_set.load()
        return old_version != replica_set["metadata"]["annotations"]["my-replica-set-cert"]

    kubetester.wait_until(wait_for_server_certs, timeout=600, sleep_time=10)
    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@mark.e2e_vault_setup
def test_rotate_server_certs_with_sts_restarting(
    replica_set: MongoDB, vault_namespace: str, vault_name: str, namespace: str, issuer: str
):
    create_x509_mongodb_tls_certs(
        issuer,
        namespace,
        MDB_RESOURCE,
        f"{MDB_RESOURCE}-cert",
        secret_backend="Vault",
        vault_subpath="database",
    )

    replica_set.assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)


@mark.e2e_vault_setup
def test_rotate_agent_certs(replica_set: MongoDB, vault_namespace: str, vault_name: str, namespace: str):
    replica_set.load()
    old_ac_version = KubernetesTester.get_automation_config()["version"]
    old_version = replica_set["metadata"]["annotations"]["agent-certs"]
    cmd = [
        "vault",
        "kv",
        "patch",
        f"secret/mongodbenterprise/database/{namespace}/agent-certs",
        "foo=bar",
    ]
    run_command_in_vault(vault_namespace, vault_name, cmd, ["version"])
    replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)

    def wait_for_agent_certs() -> bool:
        replica_set.load()
        return old_version != replica_set["metadata"]["annotations"]["agent-certs"]

    kubetester.wait_until(wait_for_agent_certs, timeout=600, sleep_time=10)

    def check_version_increased() -> bool:
        current_version = KubernetesTester.get_automation_config()["version"]
        version_increased = current_version > old_ac_version

        return version_increased

    kubetester.wait_until(check_version_increased, timeout=600)
    kubetester.kubetester.wait_processes_ready()


@mark.e2e_vault_setup
def test_no_certs_in_kubernetes(namespace: str):
    with pytest.raises(ApiException):
        read_secret(namespace, f"{MDB_RESOURCE}-clusterfile")
    with pytest.raises(ApiException):
        read_secret(namespace, f"{MDB_RESOURCE}-cert")
    with pytest.raises(ApiException):
        read_secret(namespace, "agent-certs")


@mark.e2e_vault_setup
def test_api_key_in_pod(replica_set: MongoDB):
    cmd = ["cat", "/mongodb-automation/agent-api-key/agentApiKey"]

    result = KubernetesTester.run_command_in_pod_container(
        pod_name=f"{replica_set.name}-0",
        namespace=replica_set.namespace,
        cmd=cmd,
        container="mongodb-enterprise-database",
    )

    assert result != ""


@mark.e2e_vault_setup
def test_prometheus_endpoint_on_replica_set(replica_set: MongoDB, namespace: str):
    members = replica_set["spec"]["members"]
    name = replica_set.name

    auth = ("prom-user", "prom-password")

    for idx in range(members):
        member_url = f"https://{name}-{idx}.{name}-svc.{namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(member_url, auth, tls_verify=False)


@mark.e2e_vault_setup
def test_sharded_mdb_created(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@mark.e2e_vault_setup
def test_prometheus_endpoint_works_on_every_pod_on_the_cluster(sharded_cluster: MongoDB, namespace: str):
    auth = ("prom-user", "prom-password")
    name = sharded_cluster.name

    mongos_count = sharded_cluster["spec"]["mongosCount"]
    for idx in range(mongos_count):
        url = f"https://{name}-mongos-{idx}.{name}-svc.{namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)

    shard_count = sharded_cluster["spec"]["shardCount"]
    mongodbs_per_shard_count = sharded_cluster["spec"]["mongodsPerShardCount"]
    for shard in range(shard_count):
        for mongodb in range(mongodbs_per_shard_count):
            url = f"https://{name}-{shard}-{mongodb}.{name}-sh.{namespace}.svc.cluster.local:9216/metrics"
            assert https_endpoint_is_reachable(url, auth, tls_verify=False)

    config_server_count = sharded_cluster["spec"]["configServerCount"]
    for idx in range(config_server_count):
        url = f"https://{name}-config-{idx}.{name}-cs.{namespace}.svc.cluster.local:9216/metrics"
        assert https_endpoint_is_reachable(url, auth, tls_verify=False)


@mark.e2e_vault_setup
def test_create_mongodb_user(mongodb_user: MongoDBUser, vault_name: str, vault_namespace: str, namespace: str):
    data = {"password": USER_PASSWORD}
    store_secret_in_vault(
        vault_namespace,
        vault_name,
        data,
        f"secret/mongodbenterprise/database/{namespace}/{PASSWORD_SECRET_NAME}",
    )

    mongodb_user.assert_reaches_phase(Phase.Updated, timeout=100)
