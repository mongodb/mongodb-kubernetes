from typing import Callable, List

import kubernetes
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import (
    run_kube_config_creation_tool,
    run_multi_cluster_recovery_tool,
)
from tests.constants import MULTI_CLUSTER_OPERATOR_NAME


def test_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    run_kube_config_creation_tool(member_cluster_names[:-1], namespace, namespace, member_cluster_names)
    # deploy the operator without the final cluster
    operator = install_multi_cluster_operator_set_members_fn(member_cluster_names[:-1])
    operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


def test_recover_operator_add_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    return_code = run_multi_cluster_recovery_tool(member_cluster_names, namespace, namespace)
    assert return_code == 0
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator._wait_for_operator_ready()
    operator.assert_is_running()


def test_mongodb_multi_recovers_adding_cluster(mongodb_multi: MongoDBMulti | MongoDB, member_cluster_names: List[str]):
    mongodb_multi.load()

    mongodb_multi["spec"]["clusterSpecList"].append({"clusterName": member_cluster_names[-1], "members": 2})
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=600)


def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    return_code = run_multi_cluster_recovery_tool(member_cluster_names[1:], namespace, namespace)
    assert return_code == 0
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator._wait_for_operator_ready()
    operator.assert_is_running()


def test_mongodb_multi_recovers_removing_cluster(
    mongodb_multi: MongoDBMulti | MongoDB, member_cluster_names: List[str]
):
    mongodb_multi.load()

    last_transition_time = mongodb_multi.get_status_last_transition_time()

    mongodb_multi["spec"]["clusterSpecList"].pop(0)
    mongodb_multi.update()
    mongodb_multi.assert_state_transition_happens(last_transition_time)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1500)
