from typing import Optional

import pytest
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import is_multi_cluster


# Important - you need to ensure that OM and Appdb images are build and pushed into your current docker registry before
# running tests locally - use "make om-image" and "make appdb" to do this


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_appdb_scale_up_down.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    resource.update()
    return resource


@pytest.mark.e2e_om_appdb_scale_up_down
class TestOpsManagerCreation:
    """
    Creates an Ops Manager instance with AppDB of size 3. Note, that the initial creation usually takes ~500 seconds
    """

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_gen_key_secret(self, ops_manager: MongoDBOpsManager):
        secret = ops_manager.read_gen_key_secret()
        data = secret.data
        assert "gen.key" in data

    def test_admin_key_secret(self, ops_manager: MongoDBOpsManager):
        secret = ops_manager.read_api_key_secret()
        data = secret.data
        assert "publicKey" in data
        assert "privateKey" in data

    def test_appdb_connection_url_secret(self, ops_manager: MongoDBOpsManager):
        assert len(ops_manager.read_appdb_members_from_connection_url_secret()) == 3

    def test_appdb(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        assert ops_manager.appdb_status().get_version() == custom_appdb_version

        assert ops_manager.appdb_status().get_members() == 3
        if not is_multi_cluster():
            for _, cluster_spec_item in ops_manager.get_appdb_indexed_cluster_spec_items():
                member_cluster_name = cluster_spec_item["clusterName"]
                statefulset = ops_manager.read_appdb_statefulset(member_cluster_name)
                assert statefulset.status.ready_replicas == 3
                assert statefulset.status.current_replicas == 3

    def test_appdb_monitoring_group_was_created(self, ops_manager: MongoDBOpsManager):
        ops_manager.assert_appdb_monitoring_group_was_created()

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_automation_config_tester().reached_version(1)

    @skip_if_local
    def test_om_connectivity(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()
        # todo check the backing db group, automation config and data integrity


@pytest.mark.e2e_om_appdb_scale_up_down
class TestOpsManagerAppDbScaleUp:
    """
    Scales appdb up to 5 members
    """

    def test_scale_app_db_up(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["members"] = 5
        ops_manager.update()

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_keys_not_touched(
        self,
        ops_manager: MongoDBOpsManager,
        gen_key_resource_version: str,
        admin_key_resource_version: str,
    ):
        """Making sure that the new reconciliation hasn't tried to generate new gen and api keys"""
        gen_key_secret = ops_manager.read_gen_key_secret()
        api_key_secret = ops_manager.read_api_key_secret()

        assert gen_key_secret.metadata.resource_version == gen_key_resource_version
        assert api_key_secret.metadata.resource_version == admin_key_resource_version

    def test_appdb_connection_url_secret(self, ops_manager: MongoDBOpsManager):
        assert len(ops_manager.read_appdb_members_from_connection_url_secret()) == 5

    def test_appdb(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        assert ops_manager.appdb_status().get_members() == 5
        assert ops_manager.appdb_status().get_version() == custom_appdb_version

        statefulset = ops_manager.read_appdb_statefulset()
        assert statefulset.status.ready_replicas == 5
        assert statefulset.status.current_replicas == 5

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_automation_config_tester().reached_version(2)

    @skip_if_local
    def test_om_connectivity(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()


@pytest.mark.e2e_om_appdb_scale_up_down
class TestOpsManagerAppDbScaleDown:
    """
    name: Ops Manager successful appdb scale down
    """

    def test_scale_app_db_down(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["members"] = 3
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)

    def test_appdb(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.appdb_status().get_members() == 3
        if not is_multi_cluster():
            statefulset = ops_manager.read_appdb_statefulset()
            assert statefulset.status.ready_replicas == 3
            assert statefulset.status.current_replicas == 3

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_automation_config_tester().reached_version(3)

    @skip_if_local
    def test_om_connectivity(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()
