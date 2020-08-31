"""
Ensures that validation warnings for ops manager reflect its current state
"""
import time
from typing import Optional

from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

APPDB_SHARD_COUNT_WARNING = "shardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"


@mark.e2e_om_validation_webhook
def test_wait_for_webhook(namespace: str):
    """Now that the operator is installed from the testing Pod, we might get to start the
    tests without the validating webhook being already in place, which could potentially
    make the first CR creation to validate. At the same time, the webhook has a
    FailurePolicyType of Ignore, which means that if the webhook didn't respond to the
    request, then the webhook is ignored.

    Unfortunatelly, the operator Pod might be in Running phase, as confirmed by
    _wait_for_operator_ready, while the webhook still can't respond to requests.
    Maybe Service is not ready yet.

    Next to try: check the service installed "operator-webhook" to verify state
    of webhook?

    """
    print("Waiting 20 seconds for webhook to reach running phase.")
    time.sleep(20)
    webhook_api = client.AdmissionregistrationV1beta1Api()
    client.CoreV1Api().read_namespaced_service("operator-webhook", namespace)

    # make sure the validating_webhook is installed.
    webhook_api.read_validating_webhook_configuration("mdbpolicy.mongodb.com")


@mark.e2e_om_validation_webhook
class TestOpsManagerAppDbWrongVersionShardedCluster(KubernetesTester):
    """
    name: ShardedCluster Fields for AppDB
    description: |
      sharCount field for AppDB should be rejected
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/applicationDatabase/shardCount","value":2}]'
      exception: 'shardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets'
    """

    def test_om_appdb_version_validation(self):
        assert True


@mark.e2e_om_validation_webhook
class TestOpsManagerPodSpecIsRejected(KubernetesTester):
    """
    name: PodSpec for Ops Manager
    description: |
      podSpec field for Ops Manager should be rejected
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/podSpec","value":{}}]'
      exception: 'podSpec field is not configurable for Ops Manager, use the statefulSet field instead'
    """

    def test_om_podspec_validation(self):
        assert True


@mark.e2e_om_validation_webhook
class TestOpsManagerPodSpecIsRejected(KubernetesTester):
    """
    name: PodSpec for Ops Manager
    description: |
      podSpec field for Ops Manager should be rejected
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/backup/podSpec","value":{}}]'
      exception: 'podSpec field is not configurable for Ops Manager Backup, use the backup.statefulSet field instead'
    """

    def test_backup_podspec_validation(self):
        assert True


@mark.e2e_om_validation_webhook
class TestOpsManagerAppDbWrongVersionConnectivity(KubernetesTester):
    """
    name: Inappropriate Fields for AppDB
    description: |
      connectivity field for AppDB should be rejected
    create:
      file: om_validation.yaml
      patch: '[{"op":"add","path":"/spec/applicationDatabase/connectivity", "value": {"replicaSetHorizons": [{"test-horizon": "dfdfdf"}]}}]'
      exception: 'connectivity field is not configurable for application databases'
    """

    def test_om_appdb_version_validation(self):
        assert True

@mark.e2e_om_validation_webhook
class TestOpsManagerVersion(KubernetesTester):
    """
    name: Wrong version for Ops Manager
    create:
      file: om_validation.yaml
      patch: '[{"op":"replace","path":"/spec/version","value": "4.4.4.4" }]'
      exception: 'is an invalid value for spec.version'
    """

    def test_om_version_validation(self):
        assert True


@fixture(scope="module")
def ops_manager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om["spec"]["applicationDatabase"]["shardCount"] = 3
    return om.create()


@mark.e2e_om_validation_webhook
class TestOpsManagerValidationWarnings:
    def test_disable_webhooks(self):
        webhook_api = client.AdmissionregistrationV1beta1Api()

        # break the existing webhook
        webhook = webhook_api.read_validating_webhook_configuration(
            "mdbpolicy.mongodb.com"
        )

        # First webhook is for mongodb validations, second is for ops manager
        webhook.webhooks[1].client_config.service.name = "a-non-existent-service"
        webhook.metadata.uid = ""
        webhook_api.replace_validating_webhook_configuration(
            "mdbpolicy.mongodb.com", webhook
        )

    def test_create_om_pending_with_warnings(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Pending, timeout=300)
        assert APPDB_SHARD_COUNT_WARNING in ops_manager.get_status()["warnings"]

    def test_om_running_with_warnings(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

        # Should wait for appdb to finish its restart before making new changes to it.
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=300)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)

        assert APPDB_SHARD_COUNT_WARNING in ops_manager.get_status()["warnings"]

    def test_update_om_with_corrections(self, ops_manager: MongoDBOpsManager):
        del ops_manager["spec"]["applicationDatabase"]["shardCount"]
        # TODO add replace() method to kubeobject
        client.CustomObjectsApi().replace_namespaced_custom_object(
            ops_manager.group,
            ops_manager.version,
            ops_manager.namespace,
            ops_manager.plural,
            ops_manager.name,
            ops_manager.backing_obj,
        )

        ops_manager.om_status().assert_reaches_phase(Phase.Reconciling, timeout=300)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_warnings_reset(self, ops_manager: MongoDBOpsManager):
        assert "warnings" not in ops_manager.get_status()
