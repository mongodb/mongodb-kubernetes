from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_replica_set(multi_cluster_operator: Operator, mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


def test_authoritative_set_false(mongodb_multi: MongoDBMulti | MongoDB):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authoritative_set(False)


def test_set_ignore_unknown_users_false(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()
    mongodb_multi["spec"]["security"]["authentication"]["ignoreUnknownUsers"] = False
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


def test_authoritative_set_true(mongodb_multi: MongoDBMulti | MongoDB):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    tester.assert_authoritative_set(True)
