import pytest
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture

MDB_RESOURCE_NAME = "replica-set-headless-mode"
AC_SECRET_KEY = "cluster-config.json"


def _ac_secret_name(mdb: MongoDB) -> str:
    return f"{mdb.name}-config"


def _has_headless_agent_env(mdb: MongoDB) -> bool:
    sts = mdb.read_statefulset()
    for container in sts.spec.template.spec.containers:
        for env in container.env or []:
            if env.name == "HEADLESS_AGENT" and env.value == "true":
                return True
    return False


@fixture(scope="module")
def mdb(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-headless.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    try_load(resource)
    return resource


@pytest.mark.e2e_replica_set_headless_mode
class TestReplicaSetHeadlessMode:

    def test_create_headless(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_headless_agent_env_is_set(self, mdb: MongoDB):
        assert _has_headless_agent_env(mdb)

    def test_automation_config_secret_exists(self, mdb: MongoDB, namespace: str):
        secret = KubernetesTester.read_secret(namespace, _ac_secret_name(mdb))
        assert AC_SECRET_KEY in secret

    def test_migrate_headless_to_ops_manager(self, mdb: MongoDB):
        mdb.load()
        mdb["spec"]["mode"] = "OpsManager"
        mdb["spec"]["credentials"] = "my-credentials"
        mdb["spec"]["opsManager"] = {"configMapRef": {"name": "my-project"}}
        mdb.update()
        mdb.assert_abandons_phase(Phase.Running, timeout=120)
        mdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_headless_agent_env_removed_after_migration(self, mdb: MongoDB):
        assert not _has_headless_agent_env(mdb)

    def test_migrate_ops_manager_back_to_headless(self, mdb: MongoDB):
        mdb.load()
        mdb["spec"]["mode"] = "Headless"
        mdb["spec"]["credentials"] = None
        mdb["spec"]["opsManager"] = None
        mdb.update()
        mdb.assert_abandons_phase(Phase.Running, timeout=120)
        mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_headless_agent_env_restored_after_migration_back(self, mdb: MongoDB):
        assert _has_headless_agent_env(mdb)

    def test_automation_config_secret_persists_after_round_trip(self, mdb: MongoDB, namespace: str):
        secret = KubernetesTester.read_secret(namespace, _ac_secret_name(mdb))
        assert AC_SECRET_KEY in secret
