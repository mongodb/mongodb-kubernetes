import json
import logging
import os
import subprocess

from kubernetes import client
from kubetester import create_or_update_configmap, create_or_update_namespace, try_load
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml as apply_yaml
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as _fixture
from kubetester.kubetester import get_pods
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

MDB_RESOURCE = "replica-set"
PROXY_SVC_NAME = "squid-service"
PROXY_SVC_PORT = 3128
SQUID_NAMESPACE = "squid"

logger = logging.getLogger("operator_proxy")


@fixture(scope="module")
def squid_proxy(namespace: str) -> str:
    create_or_update_namespace(SQUID_NAMESPACE, labels={"pod-security.kubernetes.io/warn": "restricted"})

    with open(_fixture("squid.conf"), "r") as conf_file:
        squid_conf = conf_file.read()
        create_or_update_configmap(namespace=SQUID_NAMESPACE, name="squid-config", data={"squid": squid_conf})

    apply_yaml(client.api_client.ApiClient(), _fixture("squid-proxy.yaml"), namespace=SQUID_NAMESPACE)

    def run_kubectl(args: list[str]):
        cmd = ["kubectl", "--request-timeout=5s"] + args
        try:
            result = subprocess.run(cmd, timeout=8, capture_output=True, text=True)
            logger.info(
                "kubectl %s: rc=%s\nstdout:\n%s\nstderr:\n%s",
                " ".join(args),
                result.returncode,
                result.stdout,
                result.stderr,
            )
            return result
        except subprocess.TimeoutExpired as e:
            logger.info("kubectl %s: timed out after 8s\nstdout:\n%s\nstderr:\n%s", " ".join(args), e.stdout, e.stderr)
        except Exception as e:
            logger.info("kubectl %s: error: %s", " ".join(args), e)
        return None

    def check_svc_endpoints():
        try:
            get_result = run_kubectl(["-n", SQUID_NAMESPACE, "get", "pods,services,endpoints", "-o", "json"])
            if get_result is None or get_result.returncode != 0:
                return False
            items = json.loads(get_result.stdout).get("items", [])
            ready_addresses = [
                addr
                for item in items
                if item.get("kind") == "Endpoints" and item.get("metadata", {}).get("name") == PROXY_SVC_NAME
                for subset in item.get("subsets", [])
                for addr in subset.get("addresses", [])
            ]
            if len(ready_addresses) != 1:
                return False
            return True
        except Exception as e:
            logger.info("check_svc_endpoints not ready: %s", e)
            return False
        finally:
            run_kubectl(["-n", SQUID_NAMESPACE, "describe", "pod", "-l", "app=squid"])
            run_kubectl(["-n", SQUID_NAMESPACE, "logs", "-l", "app=squid", "-c", "squid", "--tail=50", "--prefix=true"])

    KubernetesTester.wait_until(check_svc_endpoints, timeout=30)
    return f"http://{PROXY_SVC_NAME}.{SQUID_NAMESPACE}.svc.cluster.local:{PROXY_SVC_PORT}"


@fixture(scope="module")
def operator_with_proxy(namespace: str, operator_installation_config: dict[str, str], squid_proxy: str) -> Operator:
    os.environ["HTTP_PROXY"] = os.environ["HTTPS_PROXY"] = squid_proxy
    helm_args = operator_installation_config.copy()
    helm_args["customEnvVars"] += (
        f"\&MDB_PROPAGATE_PROXY_ENV=true"
        + f"\&HTTP_PROXY={squid_proxy}"
        + f"\&HTTPS_PROXY={squid_proxy}"
        + "\&NO_PROXY=cloud-qa.mongodb.com"
    )
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(_fixture("replica-set-basic.yaml"), namespace=namespace, name=MDB_RESOURCE)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    try_load(resource)

    return resource


@mark.e2e_operator_proxy
def test_install_operator_with_proxy(
    operator_with_proxy: Operator,
):
    operator_with_proxy.wait_for_operator_ready()


@mark.e2e_operator_proxy
def test_replica_set_reconciles(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_operator_proxy
def test_proxy_logs_requests(namespace: str):
    proxy_pods = client.CoreV1Api().list_namespaced_pod(SQUID_NAMESPACE, label_selector="app=squid").items
    pod_name = proxy_pods[0].metadata.name
    container_name = "squid"
    pod_logs = KubernetesTester.read_pod_logs(SQUID_NAMESPACE, pod_name, container_name)
    assert "cloud-qa.mongodb.com" not in pod_logs
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
            == f"http://{PROXY_SVC_NAME}.{SQUID_NAMESPACE}.svc.cluster.local:{PROXY_SVC_PORT}"
        )
