import pytest
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture

RESOURCE_NAME = "my-replica-set"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["memberConfig"] = [
        {
            "votes": 1,
            "priority": "0.5",
            "tags": {
                "tag1": "value1",
                "environment": "prod",
            },
        },
        {
            "votes": 1,
            "priority": "1.5",
            "tags": {
                "tag2": "value2",
                "environment": "prod",
            },
        },
        {
            "votes": 1,
            "priority": "0.5",
            "tags": {
                "tag2": "value2",
                "environment": "prod",
            },
        },
    ]
    resource.update()

    return resource


@pytest.mark.e2e_replica_set_member_options
def test_replica_set_created(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_replica_set_member_options
def test_replica_set_member_options_ac(replica_set: MongoDB):
    replica_set.load()

    config = replica_set.get_automation_config_tester().automation_config
    rs = config["replicaSets"]

    member1 = rs[0]["members"][0]
    member2 = rs[0]["members"][1]
    member3 = rs[0]["members"][2]

    assert member1["votes"] == 1
    assert member1["priority"] == 0.5
    assert member1["tags"] == {"tag1": "value1", "environment": "prod"}

    assert member2["votes"] == 1
    assert member2["priority"] == 1.5
    assert member2["tags"] == {"tag2": "value2", "environment": "prod"}

    assert member3["votes"] == 1
    assert member3["priority"] == 0.5
    assert member3["tags"] == {"tag2": "value2", "environment": "prod"}


@pytest.mark.e2e_replica_set_member_options
def test_replica_set_update_member_options(replica_set: MongoDB):
    replica_set.load()

    replica_set["spec"]["memberConfig"][0] = {
        "votes": 1,
        "priority": "2.5",
        "tags": {
            "tag1": "value1",
            "tag2": "value2",
            "environment": "prod",
        },
    }
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)

    config = replica_set.get_automation_config_tester().automation_config
    rs = config["replicaSets"]

    updated_member = rs[0]["members"][0]
    assert updated_member["votes"] == 1
    assert updated_member["priority"] == 2.5
    assert updated_member["tags"] == {
        "tag1": "value1",
        "tag2": "value2",
        "environment": "prod",
    }


@pytest.mark.e2e_replica_set_member_options
def test_replica_set_member_votes_to_0(replica_set: MongoDB):
    replica_set.load()

    # A non-voting member must also have priority set to 0
    replica_set["spec"]["memberConfig"][1]["votes"] = 0
    replica_set["spec"]["memberConfig"][1]["priority"] = "0.0"
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)

    config = replica_set.get_automation_config_tester().automation_config
    rs = config["replicaSets"]

    updated_member = rs[0]["members"][1]
    assert updated_member["votes"] == 0
    assert updated_member["priority"] == 0.0


@pytest.mark.e2e_replica_set_member_options
def test_replica_set_invalid_votes_and_priority(replica_set: MongoDB):
    replica_set.load()
    # A member with 0 votes must also have priority 0.0
    replica_set["spec"]["memberConfig"][1]["votes"] = 0
    replica_set["spec"]["memberConfig"][1]["priority"] = "1.2"
    replica_set.update()
    replica_set.assert_reaches_phase(
        Phase.Failed,
        msg_regexp="Failed to create/update \(Ops Manager reconciliation phase\).*cannot have 0 votes when priority is greater than 0",
    )


@pytest.mark.e2e_replica_set_member_options
def test_replica_set_recover_valid_member_options(replica_set: MongoDB):
    replica_set.load()
    # A member with priority 0.0 could still be a voting member. It cannot become primary and cannot trigger elections.
    # https://www.mongodb.com/docs/v5.0/core/replica-set-priority-0-member/#priority-0-replica-set-members
    replica_set["spec"]["memberConfig"][1]["votes"] = 1
    replica_set["spec"]["memberConfig"][1]["priority"] = "0.0"
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)


@pytest.mark.e2e_replica_set_member_options
def test_replica_set_only_one_vote_per_member(replica_set: MongoDB):
    replica_set.load()
    # A single voting member can only have 1 vote
    replica_set["spec"]["memberConfig"][2]["votes"] = 5
    replica_set["spec"]["memberConfig"][2]["priority"] = "5.8"
    replica_set.update()
    replica_set.assert_reaches_phase(
        Phase.Failed,
        msg_regexp="Failed to create/update \(Ops Manager reconciliation phase\).*cannot have greater than 1 vote",
    )
