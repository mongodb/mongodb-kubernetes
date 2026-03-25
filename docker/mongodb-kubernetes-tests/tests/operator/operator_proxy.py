from kubernetes import client
from kubetester import create_or_update_configmap, try_load
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml as apply_yaml
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as _fixture
from kubetester.kubetester import get_pods
from kubetester.mongodb import MongoDB
from kubetester.omtester import skip_if_cloud_manager
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.search.om_deployment import get_ops_manager

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
    helm_args = operator_installation_config.copy()
    helm_args["customEnvVars"] += (
        "\\&MDB_PROPAGATE_PROXY_ENV=true" + f"\\&HTTP_PROXY={squid_proxy}" + f"\\&HTTPS_PROXY={squid_proxy}"
    )
    return Operator(namespace=namespace, helm_args=helm_args).install()


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str, squid_proxy: str) -> MongoDB:
    resource = MongoDB.from_yaml(_fixture("replica-set-basic.yaml"), namespace=namespace, name=MDB_RESOURCE)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    resource.configure(om=get_ops_manager(namespace), project_name=resource.name)
    # Uncomment the following to pass the -httpProxy flag to the agent at startup
    if "agent" not in resource["spec"] or resource["spec"]["agent"] is None:
        resource["spec"]["agent"] = {}
    resource["spec"]["agent"]["startupOptions"] = {"httpProxy": squid_proxy}
    try_load(resource)

    return resource


@mark.e2e_operator_proxy
def test_install_operator_with_proxy(
    operator_with_proxy: Operator,
):
    operator_with_proxy.assert_is_running()


@mark.e2e_operator_proxy
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_operator_proxy
def test_deploy_network_policy(namespace: str):
    policy = client.V1NetworkPolicy(
        metadata=client.V1ObjectMeta(name="block-external-egress", namespace=namespace),
        spec=client.V1NetworkPolicySpec(
            pod_selector=client.V1LabelSelector(
                # match the replicaset pods
                match_labels={"app": "replica-set-svc"},
            ),
            policy_types=["Egress"],
            egress=[
                # Allow traffic to the squid proxy
                client.V1NetworkPolicyEgressRule(
                    to=[client.V1NetworkPolicyPeer(pod_selector=client.V1LabelSelector(match_labels={"app": "squid"}))]
                ),
                # Allow intra-replica-set MongoDB traffic on port 27017
                client.V1NetworkPolicyEgressRule(
                    ports=[
                        client.V1NetworkPolicyPort(port=27017),
                    ],
                    to=[
                        client.V1NetworkPolicyPeer(
                            pod_selector=client.V1LabelSelector(match_labels={"app": "replica-set-svc"})
                        )
                    ],
                ),
                # Allow DNS traffic to resolve hostnames
                client.V1NetworkPolicyEgressRule(
                    ports=[
                        client.V1NetworkPolicyPort(port=53, protocol="UDP"),
                        client.V1NetworkPolicyPort(port=53, protocol="TCP"),
                    ]
                ),
            ],
        ),
    )
    client.NetworkingV1Api().create_namespaced_network_policy(namespace, policy)


@mark.e2e_operator_proxy
def test_replica_set_reconciles(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)


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
