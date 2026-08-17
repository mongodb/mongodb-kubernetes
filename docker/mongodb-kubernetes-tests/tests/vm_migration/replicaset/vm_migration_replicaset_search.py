"""
VM migration of a replica set that has a MongoDBSearch resource attached.

Search is first attached to the VM replica set through an external source, proving it works
before migration. The migration then proceeds without waiting for the MongoDBSearch to be
repointed. The search resource keeps that external source for the whole migration: its host seeds
are updated by hand as members move from the VMs to Kubernetes, and search must survive promote
and prune.
"""

from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongotester import MongoDBBackgroundTester, with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.scram import build_sha256_creds
from pytest import fixture, mark
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper, search_resource_names
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
from tests.common.search.search_tester import SearchTester
from tests.vm_migration.vm_migration_common_helper import (
    assert_migration_data_exists,
    generated_mongodb_doc,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_replicaset_helper import (
    apply_generated_mongodb_resource,
    assert_connection_string_after_full_migration,
    assert_k8s_process_names,
    deploy_vm_service,
    deploy_vm_statefulset,
    k8s_hostnames,
    promote_and_prune,
    vm_replica_set_tester,
)

MDB_VERSION = "8.2.0-ent"

# The mongot endpoint hand-written into the automation config below derives from this MongoDBSearch
# resource's own name (<name>-search), which is what names the mongot StatefulSet. The name never
# changes during the migration, so the endpoint stays resolvable throughout. The generated CR's own
# name is independent and need not match.
MDBS_RESOURCE_NAME = "vm-mongodb"

AGENT_PASSWORD = "mms-automation-agent-password"
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

KEYFILE_SECRET_NAME = "vm-mongodb-keyfile"
KEYFILE_CONTENTS = "dm0tbWlncmF0aW9uLXNlYXJjaC1rZXlmaWxlLWNvbnRlbnRzLXBhZGRpbmc="
MONGOT_PASSWORD_SECRET_NAME = f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password"

# The mongot setParameters the operator would write itself. mongotHost and
# searchIndexManagementHostAndPort are added once the endpoint is known. Kept in sync with
# buildSearchSetParameters (controllers/searchcontroller/mongodbsearch_reconcile_helper.go); the same
# keys are carried into the generated CR: they are no longer listed in infrastructureFieldPaths
# in controllers/om/process.go, because Ops Manager does not propagate them onto new processes.
SEARCH_SET_PARAMETER = {
    "skipAuthenticationToSearchIndexManagementServer": False,
    "skipAuthenticationToMongot": False,
    "searchTLSMode": "disabled",
    "useGrpcForSearch": True,
}


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


@fixture(scope="module")
def mongot_host(namespace: str) -> str:
    return search_resource_names.mongot_pod_fqdn(MDBS_RESOURCE_NAME, namespace, 27028)


def _configure_ac_with_search(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mongot_host: str):
    """SCRAM replica set with the mongot sync-source user and the mongot setParameters."""
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["auth"] = {
        "usersWanted": [
            {
                "user": "mms-automation-agent",
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(AGENT_PASSWORD),
                "authenticationRestrictions": [],
            },
            {
                "user": ADMIN_USER_NAME,
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(ADMIN_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
            {
                # Mirrors the roles in the mongodbuser-search-sync-source-user.yaml fixture,
                # which cannot be used here because there is no MongoDB CR yet.
                "user": MONGOT_USER_NAME,
                "db": "admin",
                "roles": [{"role": "searchCoordinator", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(MONGOT_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
        "disabled": False,
        "authoritativeSet": False,
        "deploymentAuthMechanisms": ["SCRAM-SHA-256"],
        "autoAuthMechanisms": ["SCRAM-SHA-256"],
        "autoAuthMechanism": "SCRAM-SHA-256",
        "autoUser": "mms-automation-agent",
        "autoPwd": AGENT_PASSWORD,
        "autoAuthRestrictions": [],
        "key": KEYFILE_CONTENTS,
        "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
        "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
    }

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["replicaSets"] = [{"_id": rs_name, "members": [], "protocolVersion": "1"}]

    for i in range(vm_sts["spec"]["replicas"]):
        hostname = f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local"

        ac["monitoringVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        ac["processes"].append(
            {
                "version": MDB_VERSION,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(MDB_VERSION),
                "processType": "mongod",
                "args2_6": {
                    "net": {"port": 27017, "tls": {"mode": "disabled"}},
                    "storage": {"dbPath": "/data/"},
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                    "replication": {"replSetName": rs_name},
                    "setParameter": dict(
                        SEARCH_SET_PARAMETER,
                        mongotHost=mongot_host,
                        searchIndexManagementHostAndPort=mongot_host,
                    ),
                },
            }
        )

        ac["replicaSets"][0]["members"].append(
            {
                "_id": i + 100,
                "host": f"{sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )

    om_tester.api_put_automation_config(ac)


def _vm_host_seeds(namespace: str, vm_sts: dict, vm_service: dict) -> list[str]:
    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    return [f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local:27017" for i in range(vm_sts["spec"]["replicas"])]


@fixture(scope="module")
def mdbs(namespace: str, vm_sts: dict, vm_service: dict) -> MongoDBSearch:
    """MongoDBSearch pointing at the VM mongods through an external source."""
    seeds = _vm_host_seeds(namespace, vm_sts, vm_service)

    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDBS_RESOURCE_NAME
    )
    if try_load(resource):
        return resource

    resource["spec"]["source"] = {
        "username": MONGOT_USER_NAME,
        "passwordSecretRef": {"name": MONGOT_PASSWORD_SECRET_NAME, "key": "password"},
        # Field name is `keyfileSecretRef` on the wire (api/v1/search/mongodbsearch_types.go),
        # despite the Go struct field being named KeyFileSecretKeyRef.
        "external": {"hostAndPorts": seeds, "keyfileSecretRef": {"name": KEYFILE_SECRET_NAME, "key": "keyfile"}},
    }
    return resource


def _vm_search_tester(namespace: str, vm_sts: dict, vm_service: dict, username: str, password: str) -> SearchTester:
    """Build a SearchTester with credentials embedded in the connection string.

    SearchTester.for_replicaset needs a MongoDB object, which does not exist yet during the VM
    phase; vm_replica_set_tester()'s cnx_string carries no credentials (auth is normally supplied
    via `opts` at query time instead), so a raw connection string is built by hand here, following
    the same shape SearchTester.for_replicaset uses once a MongoDB resource exists.
    """
    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"
    hosts = ",".join(
        f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local:27017" for i in range(vm_sts["spec"]["replicas"])
    )
    cnx_string = f"mongodb://{username}:{password}@{hosts}/?replicaSet={rs_name}&authSource=admin"
    return SearchTester(cnx_string)


@mark.e2e_vm_migration_replicaset_search
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_vm_migration_replicaset_search
def test_deploy_vm_replicaset(namespace: str, om_tester: OMTester, vm_sts, vm_service, mongot_host: str):
    _configure_ac_with_search(namespace, om_tester, vm_sts, vm_service, mongot_host)
    om_tester.wait_agents_ready()


@mark.e2e_vm_migration_replicaset_search
def test_create_search_secrets(namespace: str):
    create_or_update_secret(namespace, name=MONGOT_PASSWORD_SECRET_NAME, data={"password": MONGOT_USER_PASSWORD})
    # Manual equivalent of mirrorKeyfileIntoSecretForMongot, which only runs for MongoDB CRs.
    create_or_update_secret(namespace, name=KEYFILE_SECRET_NAME, data={"keyfile": KEYFILE_CONTENTS})


@mark.e2e_vm_migration_replicaset_search
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_replicaset_search
def test_insert_migration_data(namespace: str):
    insert_migration_data(
        vm_replica_set_tester(namespace),
        opts=[with_scram(ADMIN_USER_NAME, ADMIN_USER_PASSWORD, "SCRAM-SHA-256")],
    )


@fixture(scope="module")
def sample_movies_helper(namespace: str, vm_sts: dict, vm_service: dict) -> SampleMoviesSearchHelper:
    return movies_search_helper.SampleMoviesSearchHelper(
        _vm_search_tester(namespace, vm_sts, vm_service, ADMIN_USER_NAME, ADMIN_USER_PASSWORD),
        tools_pod=mongodb_tools_pod.get_tools_pod(namespace),
    )


@mark.e2e_vm_migration_replicaset_search
def test_search_restore_sample_database(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.restore_sample_database()


@mark.e2e_vm_migration_replicaset_search
def test_search_create_index(sample_movies_helper: SampleMoviesSearchHelper):
    sample_movies_helper.create_search_index()


@mark.e2e_vm_migration_replicaset_search
def test_search_query_works_before_migration(sample_movies_helper: SampleMoviesSearchHelper):
    """Establishes the baseline: without this, the post-migration assertion proves nothing."""
    sample_movies_helper.assert_search_query(retry_timeout=120)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    admin_secret = f"{ADMIN_USER_NAME}-migration-password"
    create_or_update_secret(namespace, name=admin_secret, data={"password": ADMIN_USER_PASSWORD})
    mongot_secret = f"{MONGOT_USER_NAME}-migration-password"
    create_or_update_secret(namespace, name=mongot_secret, data={"password": MONGOT_USER_PASSWORD})
    # user_secrets makes run_generate_cr also emit MongoDBUser CRs for the admin and mongot users,
    # but generated_mongodb_doc() below picks out only the MongoDB document and those user CRs are
    # never applied. That's deliberate, not an oversight: the hand-written automation config sets
    # authoritativeSet: false, which the operator carries over as ignoreUnknownUsers: true, so it
    # won't prune the VM-created users just because no matching MongoDBUser CRs exist in the
    # cluster. Passing user_secrets here only exercises run_generate_cr's user-CR generation path.
    return run_generate_cr(
        namespace,
        user_secrets={
            f"{ADMIN_USER_NAME}:admin": admin_secret,
            f"{MONGOT_USER_NAME}:admin": mongot_secret,
        },
    )


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr: dict) -> MongoDB:
    return apply_generated_mongodb_resource(namespace, generated_cr)


@mark.e2e_vm_migration_replicaset_search
def test_generated_cr_carries_search_set_parameters(generated_cr: dict, mongot_host: str):
    """The generated CR must carry the search setParameters.

    Ops Manager does not propagate them onto the Kubernetes processes added by the migration, and
    the MongoDBSearch here still targets the VM mongods through an external source, so
    applySearchOverrides never runs. The CR is the only carrier; without it the migrated mongods
    come up with no mongot wiring and search breaks.
    """
    expected = dict(SEARCH_SET_PARAMETER, mongotHost=mongot_host, searchIndexManagementHostAndPort=mongot_host)

    set_parameter = generated_cr["spec"]["additionalMongodConfig"]["setParameter"]
    for key, value in expected.items():
        assert set_parameter.get(key) == value, f"setParameter.{key} is {set_parameter.get(key)!r}, expected {value!r}"


@mark.e2e_vm_migration_replicaset_search
def test_dry_run_passes(mdb_migration: MongoDB):
    """run_migration_dry_run_connectivity_passes removes the MIGRATION_DRY_RUN_ANNOTATION itself once
    connectivity verification passes, so the next reconcile mdb_migration goes through is already a
    real (non-dry-run) one.
    """
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_search
def test_migration_proceeds_while_search_still_targets_vms(mdb_migration: MongoDB):
    """The migration is not held back by the MongoDBSearch still pointing at the VM mongods."""
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_replicaset_search
def test_add_kubernetes_host_seeds_to_search_source(
    namespace: str, mdbs: MongoDBSearch, mdb_migration: MongoDB, vm_sts: dict, vm_service: dict
):
    """The search resource keeps its external source for the whole migration; only the seed list
    is maintained by hand.

    The seeds are widened to the union of the VM and Kubernetes hostnames rather than swapped
    outright: both address sets name members of the same replica set, and mongot has to keep at
    least one reachable seed at every point of the promote/prune sequence that follows.
    """
    mdbs.load()
    mdbs["spec"]["source"]["external"]["hostAndPorts"] = _vm_host_seeds(namespace, vm_sts, vm_service) + k8s_hostnames(
        mdb_migration
    )
    mdbs.update()

    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@fixture(scope="module")
def scram_opts() -> list[dict]:
    return [with_scram(ADMIN_USER_NAME, ADMIN_USER_PASSWORD, "SCRAM-SHA-256")]


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB, scram_opts: list[dict]) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mdb_migration.tester(use_ssl=False),
        health_function_params={"attempts": 1, "opts": scram_opts},
    )


@mark.e2e_vm_migration_replicaset_search
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_replicaset_search
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_search
def test_drop_vm_host_seeds_from_search_source(mdbs: MongoDBSearch, mdb_migration: MongoDB):
    """The VM members are gone from the replica set now, so their hostnames come out of the seed
    list. This is the second and last hand-edit of the search resource in the whole flow."""
    mdbs.load()
    mdbs["spec"]["source"]["external"]["hostAndPorts"] = k8s_hostnames(mdb_migration)
    mdbs.update()

    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_replicaset_search
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_replicaset_search
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_search
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_search
def test_search_set_parameters_in_automation_config(om_tester: OMTester, mdb_migration: MongoDB, mongot_host: str):
    """The Kubernetes mongods must carry the same search setParameters the VM processes had.

    The generated CR is what puts them there (see test_generated_cr_carries_search_set_parameters):
    no MongoDBSearch targets this MongoDB resource, so applySearchOverrides never runs, and Ops
    Manager does not propagate the deployment's existing search configuration onto new processes.
    This test is the end-to-end confirmation that the CR-side carrier reaches the automation
    config.
    """
    expected = dict(SEARCH_SET_PARAMETER, mongotHost=mongot_host, searchIndexManagementHostAndPort=mongot_host)

    ac = om_tester.api_get_automation_config()
    k8s_processes = [p for p in ac["processes"] if p["name"].startswith(f"k8s/{mdb_migration.namespace}/")]
    assert len(k8s_processes) == mdb_migration.get_members(), f"unexpected Kubernetes processes: {k8s_processes}"

    for process in k8s_processes:
        set_parameter = process["args2_6"].get("setParameter", {})
        for key, value in expected.items():
            assert (
                set_parameter.get(key) == value
            ), f"process {process['name']}: setParameter.{key} is {set_parameter.get(key)!r}, expected {value!r}"


@mark.e2e_vm_migration_replicaset_search
def test_search_resource_still_running(mdbs: MongoDBSearch):
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_replicaset_search
def test_search_query_works_after_migration(mdb_migration: MongoDB, namespace: str):
    tester = SearchTester.for_replicaset(mdb_migration, ADMIN_USER_NAME, ADMIN_USER_PASSWORD)
    helper = movies_search_helper.SampleMoviesSearchHelper(tester, tools_pod=mongodb_tools_pod.get_tools_pod(namespace))
    helper.assert_search_query(retry_timeout=120)


@mark.e2e_vm_migration_replicaset_search
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(
        mdb_migration.tester(use_ssl=False),
        opts=[with_scram(ADMIN_USER_NAME, ADMIN_USER_PASSWORD, "SCRAM-SHA-256")],
    )
