from typing import Dict

from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_create_kube_config_file(cluster_clients: Dict, central_cluster_name: str, member_cluster_names: str):
    clients = cluster_clients

    assert len(clients) == 4
    for member_cluster_name in member_cluster_names:
        assert member_cluster_name in clients
    assert central_cluster_name in clients


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


def test_mongodb_multi_member_options_ac(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()
    config = mongodb_multi.get_automation_config_tester().automation_config
    rs = config["replicaSets"]
    member1 = rs[0]["members"][0]
    member2 = rs[0]["members"][1]
    member3 = rs[0]["members"][2]
    member4 = rs[0]["members"][3]
    member5 = rs[0]["members"][4]

    assert member1["votes"] == 1
    assert member1["priority"] == 0.3
    assert member1["tags"] == {"cluster": "cluster-1", "region": "weur"}

    assert member2["votes"] == 1
    assert member2["priority"] == 0.7
    assert member2["tags"] == {"cluster": "cluster-1", "region": "eeur"}

    assert member3["votes"] == 1
    assert member3["priority"] == 0.2
    assert member3["tags"] == {"cluster": "cluster-2", "region": "apac"}

    assert member4["votes"] == 1
    assert member4["priority"] == 1.3
    assert member4["tags"] == {"cluster": "cluster-3", "region": "nwus"}

    assert member5["votes"] == 1
    assert member5["priority"] == 2.7
    assert member5["tags"] == {"cluster": "cluster-3", "region": "seus"}


def test_mongodb_multi_update_member_options(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()

    mongodb_multi["spec"]["clusterSpecList"][0]["memberConfig"][0] = {
        "votes": 1,
        "priority": "1.3",
        "tags": {
            "cluster": "cluster-1",
            "region": "weur",
            "app": "backend",
        },
    }
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)

    config = mongodb_multi.get_automation_config_tester().automation_config
    rs = config["replicaSets"]

    updated_member = rs[0]["members"][0]
    assert updated_member["votes"] == 1
    assert updated_member["priority"] == 1.3
    assert updated_member["tags"] == {
        "cluster": "cluster-1",
        "region": "weur",
        "app": "backend",
    }


def test_mongodb_multi_set_member_votes_to_0(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()

    mongodb_multi["spec"]["clusterSpecList"][1]["memberConfig"][0]["votes"] = 0
    mongodb_multi["spec"]["clusterSpecList"][1]["memberConfig"][0]["priority"] = "0.0"
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)

    config = mongodb_multi.get_automation_config_tester().automation_config
    rs = config["replicaSets"]

    updated_member = rs[0]["members"][2]
    assert updated_member["votes"] == 0
    assert updated_member["priority"] == 0.0


def test_mongodb_multi_set_invalid_votes_and_priority(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()

    mongodb_multi["spec"]["clusterSpecList"][1]["memberConfig"][0]["votes"] = 0
    mongodb_multi["spec"]["clusterSpecList"][1]["memberConfig"][0]["priority"] = "0.7"
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(
        Phase.Failed,
        msg_regexp=".*cannot have 0 votes when priority is greater than 0",
    )


def test_mongodb_multi_set_recover_valid_member_options(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()
    # A member with priority 0.0 could still be a voting member. It cannot become primary and cannot trigger elections.
    # https://www.mongodb.com/docs/v5.0/core/replica-set-priority-0-member/#priority-0-replica-set-members
    mongodb_multi["spec"]["clusterSpecList"][1]["memberConfig"][0]["votes"] = 1
    mongodb_multi["spec"]["clusterSpecList"][1]["memberConfig"][0]["priority"] = "0.0"
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


def test_mongodb_multi_set_only_one_vote_per_member(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()

    mongodb_multi["spec"]["clusterSpecList"][2]["memberConfig"][1]["votes"] = 3
    mongodb_multi["spec"]["clusterSpecList"][2]["memberConfig"][1]["priority"] = "0.1"
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(
        Phase.Failed,
        msg_regexp=".*cannot have greater than 1 vote",
    )
