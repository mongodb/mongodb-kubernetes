from typing import Optional

from kubetester import create_or_update_secret, read_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_central_cluster_client

"""
Tests the AppDB headless → online mode switch.

Scenario:
  1. Deploy Primary OM (AppDB in headless mode).
  2. Deploy Meta OM (a secondary Ops Manager instance).
  3. Create a credentials Secret for Meta OM admin API access.
  4. Patch Primary OM to set spec.applicationDatabase.managedByMetaOM.
  5. Assert AppDB pods restart and reach Running phase again.
  6. Assert the AppDB StatefulSet env vars reflect online mode
     (MMS_SERVER present; HEADLESS_AGENT / AUTOMATION_CONFIG_MAP absent).
  7. Assert the AppDB agent command contains online mode params
     (mmsBaseUrl, mmsGroupId, mmsApiKey as explicit flags; no -cluster flag).

Both Ops Manager instances are deployed in the same namespace for simplicity.
"""

PRIMARY_OM_NAME = "om-primary"
META_OM_NAME = "om-meta"
META_OM_CREDS_SECRET = "meta-om-creds"
META_OM_PROJECT_NAME = "primary-appdb"
SAMPLE_MDB_NAME = "mdb-primary-managed"

AGENT_CONTAINER_NAME = "mongodb-agent"


@fixture(scope="module")
def primary_ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_appdb_switch_primary_om.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)
    return resource


@fixture(scope="module")
def meta_ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_appdb_switch_meta_om.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)
    return resource


def _get_agent_container_env_vars(ops_manager: MongoDBOpsManager) -> dict:
    """Returns a name→value dict of env vars for the mongodb-agent container in the AppDB StatefulSet."""
    appdb_sts = ops_manager.read_appdb_statefulset()
    containers_by_name = {c.name: c for c in appdb_sts.spec.template.spec.containers}
    assert AGENT_CONTAINER_NAME in containers_by_name, (
        f"Container '{AGENT_CONTAINER_NAME}' not found in AppDB StatefulSet; "
        f"available: {list(containers_by_name.keys())}"
    )
    return {env.name: env.value for env in (containers_by_name[AGENT_CONTAINER_NAME].env or [])}


@mark.e2e_om_appdb_meta_om_mode_switch
class TestPrimaryOMCreation:
    """Deploy Primary OM with headless AppDB and verify baseline state."""

    def test_primary_om_reaches_running(self, primary_ops_manager: MongoDBOpsManager):
        primary_ops_manager.update()
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_appdb_in_headless_mode(self, primary_ops_manager: MongoDBOpsManager):
        """Before the switch: AppDB agent container must carry headless mode env vars."""
        env = _get_agent_container_env_vars(primary_ops_manager)
        assert "HEADLESS_AGENT" in env, "Expected HEADLESS_AGENT in headless mode"
        assert env.get("HEADLESS_AGENT") == "true"
        assert "AUTOMATION_CONFIG_MAP" in env, "Expected AUTOMATION_CONFIG_MAP in headless mode"
        assert "MMS_SERVER" not in env, "MMS_SERVER must be absent in headless mode"
        assert "MMS_GROUP_ID" not in env, "MMS_GROUP_ID must be absent in headless mode"
        assert "MMS_API_KEY" not in env, "MMS_API_KEY must be absent in headless mode"

    def test_om_healthiness(self, primary_ops_manager: MongoDBOpsManager):
        primary_ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_om_appdb_meta_om_mode_switch
class TestMetaOMCreation:
    """Deploy the secondary (Meta) Ops Manager instance."""

    def test_meta_om_reaches_running(self, meta_ops_manager: MongoDBOpsManager):
        meta_ops_manager.update()
        meta_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        meta_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_meta_om_healthiness(self, meta_ops_manager: MongoDBOpsManager):
        meta_ops_manager.get_om_tester().assert_healthiness()

    def test_create_meta_om_credentials_secret(
        self,
        namespace: str,
        meta_ops_manager: MongoDBOpsManager,
    ):
        """Read Meta OM admin API credentials and store them in the Secret that
        Primary OM's reconciler will use to connect to Meta OM."""
        api_key_secret_name = meta_ops_manager.api_key_secret(namespace)
        api_key_data = read_secret(namespace, api_key_secret_name, get_central_cluster_client())

        # The admin-key secret may use either the legacy (user/publicApiKey) or
        # the current (publicKey/privateKey) format.
        if "publicApiKey" in api_key_data:
            public_key = api_key_data["user"]
            private_key = api_key_data["publicApiKey"]
        else:
            public_key = api_key_data["publicKey"]
            private_key = api_key_data["privateKey"]

        create_or_update_secret(
            namespace,
            META_OM_CREDS_SECRET,
            {"publicKey": public_key, "privateKey": private_key},
            api_client=get_central_cluster_client(),
        )


@mark.e2e_om_appdb_meta_om_mode_switch
class TestModeSwitchToMetaOM:
    """Patch Primary OM to enable managedByMetaOM and verify the transition."""

    def test_patch_primary_om_managed_by_meta_om(
        self,
        primary_ops_manager: MongoDBOpsManager,
        meta_ops_manager: MongoDBOpsManager,
    ):
        """Patch spec.applicationDatabase.managedByMetaOM on Primary OM to trigger the mode switch."""
        primary_ops_manager.load()
        primary_ops_manager["spec"]["applicationDatabase"]["managedByMetaOM"] = {
            "name": META_OM_NAME,
            "projectName": META_OM_PROJECT_NAME,
            "credentialsSecretRef": {"name": META_OM_CREDS_SECRET},
        }
        primary_ops_manager.update()

    def test_appdb_restarts_and_reaches_running(self, primary_ops_manager: MongoDBOpsManager):
        """AppDB pods must restart (leave Running) and then return to Running."""
        primary_ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=300)
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_appdb_in_online_mode(self, primary_ops_manager: MongoDBOpsManager):
        """After the switch: AppDB agent container must carry online mode env vars.
        mmsGroupId and mmsApiKey are passed as explicit command params, not env vars."""
        env = _get_agent_container_env_vars(primary_ops_manager)
        assert "HEADLESS_AGENT" not in env, "HEADLESS_AGENT must be absent after mode switch"
        assert "AUTOMATION_CONFIG_MAP" not in env, "AUTOMATION_CONFIG_MAP must be absent after mode switch"
        # mmsGroupId and mmsApiKey are explicit command params, not env vars
        assert "MMS_GROUP_ID" not in env, "MMS_GROUP_ID must be absent (passed as -mmsGroupId cmd param)"
        assert "MMS_API_KEY" not in env, "MMS_API_KEY must be absent (passed as -mmsApiKey cmd param)"

    def test_primary_om_still_running(self, primary_ops_manager: MongoDBOpsManager):
        """Primary OM itself must remain healthy throughout the AppDB transition."""
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=300)
        primary_ops_manager.get_om_tester().assert_healthiness()

    def test_appdb_registered_in_meta_om(
        self,
        primary_ops_manager: MongoDBOpsManager,
        meta_ops_manager: MongoDBOpsManager,
    ):
        """The AppDB project must now exist inside Meta OM."""
        meta_om_tester = meta_ops_manager.get_om_tester(project_name=META_OM_PROJECT_NAME)
        meta_om_tester.assert_group_exists()
