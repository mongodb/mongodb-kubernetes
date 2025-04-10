#!/usr/bin/env python3

from itertools import zip_longest
from typing import Optional

import kubernetes
from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import get_central_cluster_client, is_multi_cluster
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


def get_om_member_cluster_names():
    return ["kind-e2e-cluster-1", "kind-e2e-cluster-2", "kind-e2e-cluster-3"]


def enable_multi_cluster_deployment(
    resource: MongoDBOpsManager,
    om_cluster_spec_list: Optional[list[int]] = None,
    appdb_cluster_spec_list: Optional[list[int]] = None,
    appdb_member_configs: Optional[list[list[dict]]] = None,
):
    resource["spec"]["topology"] = "MultiCluster"
    backup_configs = None

    if om_cluster_spec_list is None:
        om_cluster_spec_list = [1, 1, 1]

    if appdb_cluster_spec_list is None:
        appdb_cluster_spec_list = [1, 2]

    # The operator defaults to enabling backup with 1 member if not specified in the CR so we simulate
    # the behavior in the test here
    backup = resource["spec"].get("backup")
    if backup is None:
        resource["spec"]["backup"] = {"enabled": True, "members": 1}

    if resource["spec"].get("backup", {}).get("enabled", False):
        desired_members = resource["spec"].get("backup", {}).get("members", 1)
        # Here we divide the desired backup members evenly on the member clusters.
        # We add 1 extra backup member per cluster for the first *remainder* member clusters
        # so that we get exactly as many members were requested in the single cluster case.
        # Example (5 total members and 3 member clusters):
        # 5 // 3 = 1 members per member cluster
        # 5 % 3 = 2 extra members that get assigned to the first and second cluster
        members_per_cluster = desired_members // len(get_om_member_cluster_names())
        remainder_members = [1] * (desired_members % len(get_om_member_cluster_names()))
        # Using the above example, here we are zipping [1, 1] and [1, 1, 1] with a fill value of 0,
        # so we end up with the pairs [(1, 1), (1, 1), (1, 0)] and the backup configs:
        # [{"members": 2}, {"members": 2}, {"members": 1}]
        backup_configs = [
            {"members": sum(count)}
            for count in zip_longest(
                remainder_members, [members_per_cluster] * len(get_om_member_cluster_names()), fillvalue=0
            )
        ]

    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        get_om_member_cluster_names(), om_cluster_spec_list, backup_configs=backup_configs
    )
    resource["spec"]["applicationDatabase"]["topology"] = "MultiCluster"
    resource["spec"]["applicationDatabase"]["clusterSpecList"] = cluster_spec_list(
        get_appdb_member_cluster_names(), appdb_cluster_spec_list, appdb_member_configs
    )
    resource.api = kubernetes.client.CustomObjectsApi(api_client=get_central_cluster_client())
