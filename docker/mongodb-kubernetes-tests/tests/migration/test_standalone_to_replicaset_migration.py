import base64
import time
from dataclasses import dataclass, field

from kubernetes import client
from kubetester import create_or_update_configmap, create_or_update_secret, read_configmap, try_load, wait_until
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod.mongodb_tools_pod import ToolsPod, get_tools_pod
from tests.conftest import get_default_operator, install_official_operator, log_deployments_info
from tests.constants import MCK_HELM_CHART, OFFICIAL_OPERATOR_IMAGE_NAME, OPERATOR_NAME

logger = test_logger.get_test_logger(__name__)

STANDALONE_NAME = "my-standalone"
REPLICA_SET_NAME = "my-replica-set"
DEST_PROJECT_CM = "my-project-rs"

MIGRATION_USER = "migration-user"
MIGRATION_USER_PASSWORD = "my-migration-password"
MIGRATION_USER_PASSWORD_SECRET = "migration-user-password"
SHOP_USER = "shop-user"
SHOP_USER_PASSWORD = "my-shop-password"
SHOP_USER_PASSWORD_SECRET = "shop-user-password"
SHOP_USER_ROLES = [Role("shop", "readWrite"), Role("inventory", "readWrite")]

# Both deployments run SCRAM.
AUTHENTICATION = {"enabled": True, "modes": ["SCRAM"]}

# The source dump user and the destination restore user, the guide's SCRAM recommendations.
# The destination user also carries clusterMonitor and backup, the rs.status() and oplog
# checks of the verification step need them.
SOURCE_USER_ROLES = [
    Role("admin", "backup"),
    Role("admin", "readAnyDatabase"),
    Role("admin", "userAdminAnyDatabase"),
]
DEST_USER_ROLES = [
    Role("admin", "restore"),
    Role("admin", "readWriteAnyDatabase"),
    Role("admin", "dbAdminAnyDatabase"),
    Role("admin", "clusterMonitor"),
    Role("admin", "backup"),
]

# The guide was verified against MCK 1.12.0, the oldest release that publishes the
# connection string Secret. Customers on this vintage are the ones the migration targets.
SOURCE_OPERATOR_VERSION = "1.12.0"

# The deprecation warning the operator must log when it reconciles a Standalone resource.
# This must match StandaloneDeprecationMessage in the api/mongodb/v1/mdb package.
DEPRECATION_MESSAGE = (
    "Standalone is deprecated and will not be supported in the next major version, "
    "migrate to a one member ReplicaSet"
)

"""
e2e_standalone_to_replicaset_migration installs an older released MCK operator version (the
mongodb/mongodb-kubernetes chart at SOURCE_OPERATOR_VERSION), deploys a Standalone, upgrades to
the operator built from this branch and asserts the Standalone deprecation warning is logged,
then performs the migration from the guide (docs/migration/standalone-to-replica-set.md).

The cutover is offline: a standalone has no oplog, so there is no catch up of writes that land
during the dump. The steps mirror the guide: give the replica set its own Ops
Manager project through a new ConfigMap, deploy a ReplicaSet with members 1, dump and restore
excluding admin.* and config.*, verify counts and dbHashes on every user collection, then delete
the standalone and its PVC.

Both deployments run SCRAM, with a pinned password Secret so one credential works for
both connection strings, and the users are recreated as MongoDBUser
resources on the replica set.
"""

# One line per user collection: database.collection, document count, dbHash.
FINGERPRINT_JS = """
const SYSTEM_DBS = ["admin", "local", "config"];
db.adminCommand({listDatabases: 1}).databases
  .map(d => d.name)
  .filter(name => !SYSTEM_DBS.includes(name))
  .sort()
  .forEach(name => {
    const mdb = db.getSiblingDB(name);
    const hash = mdb.runCommand({dbHash: 1});
    mdb.getCollectionNames().sort().forEach(coll =>
      print(name + "." + coll, mdb.getCollection(coll).countDocuments({}), hash.collections[coll] || "-"));
  });
"""

SEED_JS = """
const docs = [];
for (let i = 0; i < 1000; i++) {
  docs.push({orderId: NumberInt(i), item: "item-" + i, qty: NumberInt(i % 10)});
}
const shop = db.getSiblingDB("shop");
shop.orders.drop();
shop.createCollection("orders", {validator: {
  $jsonSchema: {bsonType: "object", required: ["orderId"], properties: {orderId: {bsonType: "int"}}}
}});
shop.orders.insertMany(docs);
shop.orders.createIndex({orderId: 1}, {unique: true});
const inventory = db.getSiblingDB("inventory");
inventory.items.drop();
inventory.items.insertMany([{name: "first"}, {name: "second"}]);
"""

# Validator survived, unique index survived, oplog has entries, set name and member count.
VERIFY_JS = f"""
const shop = db.getSiblingDB("shop");
const validator = shop.getCollectionInfos({{name: "orders"}})[0].options.validator !== undefined;
const unique = shop.orders.getIndexes().some(idx => idx.unique === true);
const oplog = db.getSiblingDB("local").oplog.rs.estimatedDocumentCount() > 0;
const rs = rs.status();
print(validator, unique, oplog, rs.set, rs.members.length);
"""

VERIFY_EXPECTED = f"true true true {REPLICA_SET_NAME} 1"


@dataclass
class MigrationState:
    """Values recorded on the source deployment and consumed by later migration steps."""

    # Usernames from the standalone, recreated on the replica set in step 9.
    source_users: list = field(default_factory=list)
    # Fingerprint taken twice with no writes in between, the expectation of the verification step.
    source_baseline: str = ""


def fingerprint(tools_pod: ToolsPod, uri: str) -> str:
    return tools_pod.run_command(["mongosh", uri, "--quiet", "--eval", FINGERPRINT_JS])


def mongosh_eval(tools_pod: ToolsPod, uri: str, script: str) -> str:
    return tools_pod.run_command(["mongosh", uri, "--quiet", "--eval", script])


def wait_resource_gone(name: str, namespace: str, timeout: int = 300):
    """Waits for the MongoDB resource to be deleted, the API answers 404 once it is gone."""

    def is_gone():
        try:
            client.CustomObjectsApi().get_namespaced_custom_object("mongodb.com", "v1", namespace, "mongodb", name)
            return False
        except client.exceptions.ApiException as e:
            if e.status == 404:
                return True
            raise

    assert wait_until(is_gone, timeout=timeout), f"mongodb/{name} still exists after {timeout}s"


@fixture(scope="module")
def standalone(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("standalone.yaml"), namespace=namespace, name=STANDALONE_NAME)
    resource.set_version(custom_mdb_version)

    # Persistent so that the PVC cleanup step of the guide applies.
    resource["spec"]["persistent"] = True
    resource["spec"]["security"] = {"authentication": dict(AUTHENTICATION)}

    try_load(resource)
    return resource


@fixture(scope="module")
def tools_pod(namespace: str, custom_mdb_version: str) -> ToolsPod:
    """The guide's tools pod, the database pods ship mongodump and mongorestore but not mongosh."""
    return get_tools_pod(namespace, custom_mdb_version)


@fixture(scope="module")
def migration_state() -> MigrationState:
    return MigrationState()


@fixture(scope="module")
def source_operator(
    namespace: str,
    managed_security_context: str,
    operator_installation_config: dict[str, str],
) -> Operator:
    """The released MCK operator version the migration targets."""
    return install_official_operator(
        namespace,
        managed_security_context,
        operator_installation_config,
        central_cluster_name=None,
        central_cluster_client=None,
        member_cluster_clients=None,
        member_cluster_names=None,
        custom_operator_version=SOURCE_OPERATOR_VERSION,
        helm_chart_path=MCK_HELM_CHART,
        operator_name=OPERATOR_NAME,
        operator_image=OFFICIAL_OPERATOR_IMAGE_NAME,
    )


@fixture(scope="module")
def upgraded_operator(namespace: str, operator_installation_config: dict[str, str]) -> Operator:
    """The operator built from this branch, installed over the source operator."""
    return get_default_operator(
        namespace, operator_installation_config=operator_installation_config, apply_crds_first=True
    )


def standalone_uri(namespace: str, username: str, password: str) -> str:
    return (
        f"mongodb://{username}:{password}@{STANDALONE_NAME}-0.{STANDALONE_NAME}-svc.{namespace}.svc.cluster.local"
        f":27017/?directConnection=true&authSource=admin"
    )


@fixture(scope="module")
def source_uri(namespace: str) -> str:
    """The standalone as the migration user, for the fingerprint and the dump."""
    return standalone_uri(namespace, MIGRATION_USER, MIGRATION_USER_PASSWORD)


def mongo_user(namespace: str, parent: MongoDB, username: str, password_secret: str, roles: list) -> MongoDBUser:
    """Builds a MongoDBUser on parent whose password is pinned through a Secret."""
    user = generic_user(namespace, username, mongodb_resource=parent)
    user["spec"]["passwordSecretKeyRef"] = {"name": password_secret, "key": "password"}
    user.add_roles(roles)
    try_load(user)
    return user


@fixture(scope="module")
def standalone_users(namespace: str, standalone: MongoDB) -> list:
    """The users on the standalone, the dump user and the application user."""
    create_or_update_secret(namespace, MIGRATION_USER_PASSWORD_SECRET, {"password": MIGRATION_USER_PASSWORD})
    create_or_update_secret(namespace, SHOP_USER_PASSWORD_SECRET, {"password": SHOP_USER_PASSWORD})
    return [
        mongo_user(namespace, standalone, MIGRATION_USER, MIGRATION_USER_PASSWORD_SECRET, SOURCE_USER_ROLES),
        mongo_user(namespace, standalone, SHOP_USER, SHOP_USER_PASSWORD_SECRET, SHOP_USER_ROLES),
    ]


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace, name=REPLICA_SET_NAME)
    resource.set_version(custom_mdb_version)

    # Same version and security settings as the standalone, one member, own project ConfigMap.
    resource["spec"]["members"] = 1
    resource["spec"]["persistent"] = True
    resource["spec"]["security"] = {"authentication": dict(AUTHENTICATION)}
    resource["spec"]["opsManager"]["configMapRef"]["name"] = DEST_PROJECT_CM
    resource["spec"]["credentials"] = "my-credentials"

    try_load(resource)
    return resource


@fixture(scope="module")
def destination_uri(namespace: str, replica_set: MongoDB) -> str:
    # Requesting replica_set orders this fixture after the resource is Running.
    secret_name = f"{REPLICA_SET_NAME}-cluster-connection-string"
    try:
        secret = client.CoreV1Api().read_namespaced_secret(secret_name, namespace)
        uri = base64.b64decode(secret.data["connectionString.standard"]).decode("utf-8")
    except client.exceptions.ApiException as e:
        if e.status != 404:
            raise
        # Released operators before the merged chart do not publish the Secret,
        # the fixture then builds the same URI by hand.
        uri = (
            f"mongodb://{REPLICA_SET_NAME}-0.{REPLICA_SET_NAME}-svc.{namespace}.svc.cluster.local:27017/"
            f"?replicaSet={REPLICA_SET_NAME}"
        )
    return uri


@fixture(scope="module")
def destination_user(namespace: str, replica_set: MongoDB) -> MongoDBUser:
    """The restore credential, same username and pinned password as the source dump user,
    created before the restore so one credential works for both connection strings."""
    create_or_update_secret(namespace, MIGRATION_USER_PASSWORD_SECRET, {"password": MIGRATION_USER_PASSWORD})
    return mongo_user(namespace, replica_set, MIGRATION_USER, MIGRATION_USER_PASSWORD_SECRET, DEST_USER_ROLES)


@fixture(scope="module")
def destination_auth_uri(namespace: str, destination_user: MongoDBUser) -> str:
    """The replica set as the migration user, for the restore and the verification."""
    return (
        f"mongodb://{MIGRATION_USER}:{MIGRATION_USER_PASSWORD}@{REPLICA_SET_NAME}-0.{REPLICA_SET_NAME}-svc.{namespace}"
        f".svc.cluster.local:27017/?replicaSet={REPLICA_SET_NAME}&authSource=admin"
    )


@mark.e2e_standalone_to_replicaset_migration
class TestStandaloneToReplicaSetMigration:
    def test_install_old_operator(self, namespace: str, source_operator: Operator):
        """The migration targets customers on older released MCK operators."""
        source_operator.wait_for_operator_ready()
        log_deployments_info(namespace)

    def test_create_standalone(self, standalone: MongoDB):
        standalone.update()
        standalone.assert_reaches_phase(Phase.Running, timeout=600)

    def test_create_source_users(self, standalone_users: list):
        """The dump user and the application user."""
        for user in standalone_users:
            user.update()
            user.assert_reaches_phase(Phase.Updated, timeout=600)

    def test_record_source_state(self, standalone: MongoDB, migration_state: MigrationState):
        """The source users and the resource type, recorded before the migration
        changes anything."""
        items = (
            client.CustomObjectsApi()
            .list_namespaced_custom_object("mongodb.com", "v1", standalone.namespace, "mongodbusers")
            .get("items", [])
        )
        migration_state.source_users = [u["spec"]["username"] for u in items]
        assert set(migration_state.source_users) == {MIGRATION_USER, SHOP_USER}
        assert standalone["spec"]["type"] == "Standalone"

    def test_seed_data(self, namespace: str, tools_pod: ToolsPod):
        """Shop orders with a unique index and a validator, plus a second database."""
        mongosh_eval(tools_pod, standalone_uri(namespace, SHOP_USER, SHOP_USER_PASSWORD), SEED_JS)

    def test_upgrade_operator(self, upgraded_operator: Operator):
        """Upgrade to the operator built from this branch, same release name so helm
        replaces the old deployment."""
        upgraded_operator.wait_for_operator_ready()
        log_deployments_info(upgraded_operator.namespace)

    def test_standalone_reconciled(self, standalone: MongoDB):
        """The upgraded operator reconciles the existing Standalone."""
        standalone.assert_abandons_phase(phase=Phase.Running, timeout=300)
        standalone.assert_reaches_phase(phase=Phase.Running, timeout=800)

    def test_standalone_deprecation_logged(self, namespace: str, upgraded_operator: Operator):
        """The upgraded operator logs the deprecation warning for the Standalone."""
        pods = upgraded_operator.list_operator_pods()
        assert len(pods) == 1
        logs = KubernetesTester.read_pod_logs(namespace, pods[0].metadata.name)
        assert DEPRECATION_MESSAGE in logs

    def test_create_project_config_map(self, standalone: MongoDB):
        """Guide step 1, copy the project ConfigMap under a new name and projectName,
        keeping baseUrl and orgId. The operator creates the project on first reconcile."""
        src_cm = read_configmap(standalone.namespace, standalone.config_map_name)
        assert "orgId" in src_cm

        new_cm = dict(src_cm)
        new_cm["projectName"] = KubernetesTester.random_om_project_name()
        create_or_update_configmap(standalone.namespace, DEST_PROJECT_CM, new_cm)

    def test_deploy_replica_set(self, standalone: MongoDB, replica_set: MongoDB):
        """Guide step 1, one member replica set on the same MongoDB version."""
        assert replica_set["spec"]["version"] == standalone["spec"]["version"]
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=800)

    def test_replica_set_connection_string(self, destination_uri: str):
        """The published URI carries replicaSet=."""
        assert f"replicaSet={REPLICA_SET_NAME}" in destination_uri

    def test_create_destination_user(self, destination_user: MongoDBUser):
        """The restore credential, created before the restore so one credential
        works for both connection strings."""
        destination_user.update()
        destination_user.assert_reaches_phase(Phase.Updated, timeout=600)

    def test_source_baseline(self, tools_pod: ToolsPod, source_uri: str, migration_state: MigrationState):
        """Guide step 3. There are no application writers in the test, so the
        write freeze is inherent, and the fingerprint twice confirms nothing is writing.
        The second fingerprint is the baseline for the verification step."""
        first = fingerprint(tools_pod, source_uri)
        time.sleep(5)
        second = fingerprint(tools_pod, source_uri)
        assert first == second
        migration_state.source_baseline = second

    def test_dump(self, tools_pod: ToolsPod, source_uri: str):
        """Guide step 3, the dump inside the tools pod."""
        tools_pod.run_command(
            [
                "mongodump",
                "--uri",
                source_uri,
                "--archive=/tmp/standalone.archive.gz",
                "--gzip",
            ]
        )

    def test_restore(self, tools_pod: ToolsPod, destination_auth_uri: str):
        """Guide step 3, restore the dump excluding admin.* and config.*."""
        tools_pod.run_command(
            [
                "mongorestore",
                "--uri",
                destination_auth_uri,
                "--archive=/tmp/standalone.archive.gz",
                "--gzip",
                "--nsExclude=admin.*",
                "--nsExclude=config.*",
                "--stopOnError",
            ]
        )

    def test_verify_copy(self, tools_pod: ToolsPod, destination_auth_uri: str, migration_state: MigrationState):
        """Guide step 3, counts and dbHashes match on every user collection, and any
        difference would also report a write that landed during the dump."""
        assert fingerprint(tools_pod, destination_auth_uri) == migration_state.source_baseline
        assert mongosh_eval(tools_pod, destination_auth_uri, VERIFY_JS).strip() == VERIFY_EXPECTED

    def test_switch_applications(
        self, namespace: str, replica_set: MongoDB, tools_pod: ToolsPod, migration_state: MigrationState
    ):
        """The recorded users are recreated as MongoDBUser resources on the
        replica set. migration-user already exists there as the restore credential, so
        shop-user is the one to recreate, and the application writes with its credentials."""
        assert set(migration_state.source_users) == {MIGRATION_USER, SHOP_USER}

        shop_user = mongo_user(namespace, replica_set, SHOP_USER, SHOP_USER_PASSWORD_SECRET, SHOP_USER_ROLES)
        shop_user.update()
        shop_user.assert_reaches_phase(Phase.Updated, timeout=600)

        app_uri = (
            f"mongodb://{SHOP_USER}:{SHOP_USER_PASSWORD}@{REPLICA_SET_NAME}-0.{REPLICA_SET_NAME}-svc.{namespace}"
            f".svc.cluster.local:27017/shop?replicaSet={REPLICA_SET_NAME}&authSource=admin"
        )
        mongosh_eval(
            tools_pod,
            app_uri,
            'db.getSiblingDB("shop").orders.insertOne({orderId: NumberInt(1000), item: "post-migration", qty: NumberInt(1)})',
        )
        count = mongosh_eval(tools_pod, app_uri, 'print(db.getSiblingDB("shop").orders.countDocuments({}))')
        assert count.strip() == "1001"

    def test_cleanup(self, standalone: MongoDB, replica_set: MongoDB, namespace: str, tools_pod: ToolsPod):
        """Guide step 4, the operator does not delete the PVC, it must be deleted manually."""
        core = client.CoreV1Api()
        pvc_name = f"data-{STANDALONE_NAME}-0"

        core.read_namespaced_persistent_volume_claim(pvc_name, namespace)

        standalone.delete()
        wait_resource_gone(STANDALONE_NAME, namespace)

        # The operator leaves the PVC behind, the guide deletes it manually.
        core.read_namespaced_persistent_volume_claim(pvc_name, namespace)
        core.delete_namespaced_persistent_volume_claim(pvc_name, namespace)

        # Optional in the guide, the standalone project ConfigMap has no other consumers.
        core.delete_namespaced_config_map(standalone.config_map_name, namespace)

        tools_pod.core_v1.delete_namespaced_pod(tools_pod.pod_name, namespace)

        # Best effort removal of the migration project in Ops Manager.
        try:
            replica_set.get_om_tester().api_remove_group()
        except Exception as e:
            logger.warning(f"Could not remove the Ops Manager project: {e}")
