import os

import yaml
from kubernetes import client
from kubetester import create_or_update_configmap
from kubetester.create_or_replace_from_yaml import (
    create_or_replace_from_yaml as apply_yaml,
)
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as _fixture
from kubetester.kubetester import get_pods
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from oauthlib.oauth1.rfc5849.endpoints import resource
from pytest import fixture, mark

MDB_RESOURCE = "replica-set"
PROXY_SVC_NAME = "squid-service"
PROXY_SVC_PORT = 3128


@fixture(scope="module")
def squid_proxy(namespace: str) -> str:
    with open(_fixture("squid.conf"), "r") as conf_file:
        squid_conf = conf_file.read()
        create_or_update_configmap(namespace=namespace, name="squid-config", data={"squid": squid_conf})

    apply_yaml(client.api_client.ApiClient(), _fixture("squid-proxy.yaml"), namespace=namespace)

    def check_svc_endpoints():
        try:
            endpoint = client.CoreV1Api().read_namespaced_endpoints("squid-service", namespace)
            assert len(endpoint.subsets[0].addresses) == 1
            return True
        except:
            return False

    KubernetesTester.wait_until(check_svc_endpoints, timeout=30)
    return f"http://{PROXY_SVC_NAME}:{PROXY_SVC_PORT}"


@fixture(scope="module")
def operator_with_proxy(namespace: str, operator_installation_config: dict[str, str], squid_proxy: str) -> Operator:
    os.environ["HTTP_PROXY"] = os.environ["HTTPS_PROXY"] = squid_proxy
    helm_args = operator_installation_config.copy()
    helm_args["customEnvVars"] += (
        f"\&MDB_PROPAGATE_PROXY_ENV=true" + f"\&HTTP_PROXY={squid_proxy}" + f"\&HTTPS_PROXY={squid_proxy}"
    )
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(_fixture("replica-set-basic.yaml"), namespace=namespace, name=MDB_RESOURCE)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    resource.update()

    return resource


@mark.e2e_operator_proxy
def test_install_operator_with_proxy(
    operator_with_proxy: Operator,
):
    operator_with_proxy.assert_is_running()


@mark.e2e_operator_proxy
def test_replica_set_reconciles(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_operator_proxy
def test_proxy_logs_requests(namespace: str):
    proxy_pods = client.CoreV1Api().list_namespaced_pod(namespace, label_selector="app=squid").items
    pod_name = proxy_pods[0].metadata.name
    container_name = "squid"
    pod_logs = KubernetesTester.read_pod_logs(namespace, pod_name, container_name)
    assert "cloud-qa.mongodb.com" in pod_logs
    assert "api-agents-qa.mongodb.com" in pod_logs
    assert "api-backup-qa.mongodb.com" in pod_logs


@mark.e2e_operator_proxy
def test_proxy_env_vars_set_in_pod(namespace: str):
    for pod_name in get_pods(MDB_RESOURCE + "-{}", 3):
        pod = client.CoreV1Api().read_namespaced_pod(pod_name, namespace)
        env_vars = {var.name: var.value for var in pod.spec.containers[0].env}
        assert "http_proxy" in env_vars
        assert "HTTP_PROXY" in env_vars
        assert "https_proxy" in env_vars
        assert "HTTPS_PROXY" in env_vars
        assert (
            env_vars["HTTP_PROXY"]
            == env_vars["http_proxy"]
            == env_vars["HTTPS_PROXY"]
            == env_vars["https_proxy"]
            == f"http://{PROXY_SVC_NAME}:{PROXY_SVC_PORT}"
        )
