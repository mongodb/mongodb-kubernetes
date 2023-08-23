#!/usr/bin/env python3

import kubernetes

from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import is_multi_cluster, get_central_cluster_client
from tests.multicluster.conftest import cluster_spec_list


def pytest_runtest_setup(item):
    """This allows to automatically install the Operator and enable AppDB monitoring before running any test"""
    if is_multi_cluster():
        if "multi_cluster_operator_with_monitored_appdb" not in item.fixturenames:
            print("\nAdding operator installation fixture: multi_cluster_operator_with_monitored_appdb")
            item.fixturenames.insert(0, "multi_cluster_operator_with_monitored_appdb")
    else:
        if "operator_with_monitored_appdb" not in item.fixturenames:
            print("\nAdding operator installation fixture: operator_with_monitored_appdb")
            item.fixturenames.insert(0, "operator_with_monitored_appdb")


# TODO move to conftest up the test hierarchy?
def get_appdb_member_cluster_names():
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]


def enable_appdb_multi_cluster_deployment(resource: MongoDBOpsManager):
    resource["spec"]["applicationDatabase"]["topology"] = "MultiCluster"
    resource["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        get_appdb_member_cluster_names(), [1, 2]
    )
    resource.api = kubernetes.client.CustomObjectsApi(api_client=get_central_cluster_client())
