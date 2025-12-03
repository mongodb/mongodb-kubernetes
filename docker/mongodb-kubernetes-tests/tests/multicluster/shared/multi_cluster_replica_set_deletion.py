from typing import List

import kubernetes
from kubetester import wait_until
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


def test_automation_config_has_been_updated(mongodb_multi: MongoDBMulti | MongoDB):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    processes = tester.get_replica_set_processes(mongodb_multi.name)
    assert len(processes) == 5


def test_delete_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.delete()

    def wait_for_deleted() -> bool:
        try:
            mongodb_multi.load()
            return False
        except kubernetes.client.ApiException as e:
            if e.status == 404:
                return True
            else:
                logger.error(e)
                return False

    wait_until(wait_for_deleted, timeout=60)


def test_deployment_has_been_removed_from_automation_config():
    def wait_until_automation_config_is_clean() -> bool:
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        try:
            tester.assert_empty()
            return True
        except AssertionError as e:
            logger.error(e)
            return False

    wait_until(wait_until_automation_config_is_clean, timeout=60)


def test_kubernetes_resources_have_been_cleaned_up(
    mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]
):
    def wait_until_secrets_are_removed() -> bool:
        try:
            mongodb_multi.read_services(member_cluster_clients)
            return False
        except kubernetes.client.ApiException as e:
            logger.error(e)
            return True

    def wait_until_statefulsets_are_removed() -> bool:
        try:
            mongodb_multi.read_statefulsets(member_cluster_clients)
            return False
        except kubernetes.client.ApiException as e:
            logger.error(e)
            return True

    def wait_until_configmaps_are_removed() -> bool:
        try:
            mongodb_multi.read_configmaps(member_cluster_clients)
            return False
        except kubernetes.client.ApiException as e:
            logger.error(e)
            return True

    wait_until(wait_until_secrets_are_removed, timeout=60)
    wait_until(wait_until_statefulsets_are_removed, timeout=60)
    wait_until(wait_until_configmaps_are_removed, timeout=60)
