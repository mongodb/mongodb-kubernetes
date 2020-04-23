"""
Ensures that validation warnings for ops manager reflect its current state
"""
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark

APPDB_SHARD_COUNT_WARNING = "shardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"


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
      exception: 'podSpec field is not configurable for Ops Manager'
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
      exception: 'podSpec field is not configurable for Ops Manager Backup'
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


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
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
        # ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Reconciling, timeout=200)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_warnings_reset(self, ops_manager: MongoDBOpsManager):
        assert "warnings" not in ops_manager.get_status()
