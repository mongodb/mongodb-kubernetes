from typing import Dict

import kubernetes
import pytest
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_replica_set_member_options as testhelper

MDB_RESOURCE = "multi-replica-set"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names,
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("mongodb-multi.yaml"),
        MDB_RESOURCE,
        namespace,
    )
    resource.set_version(custom_mdb_version)
    member_options = [
        [
            {
                "votes": 1,
                "priority": "0.3",
                "tags": {
                    "cluster": "cluster-1",
                    "region": "weur",
                },
            },
            {
                "votes": 1,
                "priority": "0.7",
                "tags": {
                    "cluster": "cluster-1",
                    "region": "eeur",
                },
            },
        ],
        [
            {
                "votes": 1,
                "priority": "0.2",
                "tags": {
                    "cluster": "cluster-2",
                    "region": "apac",
                },
            },
        ],
        [
            {
                "votes": 1,
                "priority": "1.3",
                "tags": {
                    "cluster": "cluster-3",
                    "region": "nwus",
                },
            },
            {
                "votes": 1,
                "priority": "2.7",
                "tags": {
                    "cluster": "cluster-3",
                    "region": "seus",
                },
            },
        ],
    ]
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2], member_options)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    resource.update()
    return resource


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_create_kube_config_file(cluster_clients: Dict, central_cluster_name: str, member_cluster_names: str):
    testhelper.test_create_kube_config_file(cluster_clients, central_cluster_name, member_cluster_names)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_create_mongodb_multi(mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_mongodb_multi_member_options_ac(mongodb_multi: MongoDB):
    testhelper.test_mongodb_multi_member_options_ac(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_mongodb_multi_update_member_options(mongodb_multi: MongoDB):
    testhelper.test_mongodb_multi_update_member_options(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_mongodb_multi_set_member_votes_to_0(mongodb_multi: MongoDB):
    testhelper.test_mongodb_multi_set_member_votes_to_0(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_mongodb_multi_set_invalid_votes_and_priority(mongodb_multi: MongoDB):
    testhelper.test_mongodb_multi_set_invalid_votes_and_priority(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_mongodb_multi_set_recover_valid_member_options(mongodb_multi: MongoDB):
    testhelper.test_mongodb_multi_set_recover_valid_member_options(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_member_options
def test_mongodb_multi_set_only_one_vote_per_member(mongodb_multi: MongoDB):
    testhelper.test_mongodb_multi_set_only_one_vote_per_member(mongodb_multi)
