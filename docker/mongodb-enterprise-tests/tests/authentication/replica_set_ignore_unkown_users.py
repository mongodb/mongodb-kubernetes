from kubetester import find_fixture
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(
    namespace: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("replica-set-scram-sha-256.yaml"), namespace=namespace)

    resource["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = True

    return resource.create()


@mark.e2e_replica_set_ignore_unknown_users
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ignore_unknown_users
def test_authoritative_set_false(replica_set: MongoDB):
    tester = replica_set.get_automation_config_tester()
    tester.assert_authoritative_set(False)


@mark.e2e_replica_set_ignore_unknown_users
def test_set_ignore_unknown_users_false(replica_set: MongoDB):
    replica_set.reload()
    replica_set["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = False
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ignore_unknown_users
def test_authoritative_set_true(replica_set: MongoDB):
    tester = replica_set.get_automation_config_tester()
    tester.assert_authoritative_set(True)


@mark.e2e_replica_set_ignore_unknown_users
def test_set_ignore_unknown_users_true(replica_set: MongoDB):
    replica_set.reload()
    replica_set["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = True
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ignore_unknown_users
def test_authoritative_set_false_again(replica_set: MongoDB):
    tester = replica_set.get_automation_config_tester()
    tester.assert_authoritative_set(False)
