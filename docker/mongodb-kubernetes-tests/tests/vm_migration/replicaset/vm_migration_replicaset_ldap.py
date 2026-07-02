"""
VM migration from a generated MongoDB resource with LDAP (PLAIN) authentication.

This test stands up an OpenLDAP server, configures VM members in Ops Manager with native LDAP
authentication, runs kubectl-mongodb migrate-to-mck, applies the generated resources, and verifies
dry-run validation, migration of an LDAP ($external) application user, data continuity, connection
strings, process names, and the promote and prune flow.

The LDAP agent user is the auth autoUser and is skipped by migrate-to-mck (like the X509 agent);
the LDAP application user is emitted as a MongoDBUser CR with db: $external and no password Secret.
"""

from kubetester import create_or_update_secret, get_statefulset
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.ldap import LDAPUser, OpenLDAP, add_user_to_group, create_user, ensure_group, ensure_organizational_unit
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.authentication.conftest import LDAP_PASSWORD, openldap_install
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_common_helper import (
    apply_user_crs_and_verify_ac,
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_replicaset_helper import (
    apply_generated_mongodb_resource,
    assert_common_generated_cr_shape,
    assert_connection_string_after_full_migration,
    assert_connection_string_contains_current_hosts,
    assert_k8s_process_names,
    deploy_vm_service,
    deploy_vm_statefulset,
    promote_and_prune,
    vm_replica_set_tester,
)

RS_NAME = "vm-mongodb-rs"
LDAP_BASE = "dc=example,dc=org"
AGENT_UID = "mms-automation-agent"
APP_USER_UID = "vm-ldap-app-user"
# The migrate tool wires the generated CR to these fixed Secret names (bind-query + LDAP agent password).
LDAP_BIND_QUERY_SECRET = "ldap-bind-query-password"
LDAP_AGENT_PASSWORD_SECRET = "ldap-agent-password"


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def openldap(namespace: str) -> OpenLDAP:
    return openldap_install(namespace, "openldap")


@fixture(scope="module")
def ldap_agent_user(openldap: OpenLDAP) -> LDAPUser:
    """The automation agent's LDAP identity. Authenticates via simple bind with its full DN."""
    user = LDAPUser(AGENT_UID, LDAP_PASSWORD, ou="groups")
    ensure_organizational_unit(openldap, "groups")
    create_user(openldap, user, ou="groups")
    ensure_group(openldap, cn="agents", ou="groups")
    add_user_to_group(openldap, user=AGENT_UID, group_cn="agents", ou="groups")
    return user


@fixture(scope="module")
def ldap_app_user(openldap: OpenLDAP) -> LDAPUser:
    """The migrated $external application user (readWrite on admin so it can run an auth-gated write)."""
    user = LDAPUser(APP_USER_UID, LDAP_PASSWORD)
    create_user(openldap, user)
    return user


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sts: dict,
    vm_service: dict,
    mdb_version: str,
    openldap: OpenLDAP,
    agent_dn: str,
    app_user_dn: str,
):
    """Configure the VM replica set for native LDAP (PLAIN) authentication.

    Authorization is taken from explicit usersWanted entries (db: $external) rather than LDAP groups,
    so only authentication flows through LDAP. Internal cluster auth uses the keyfile (SCRAM for
    __system@local), so authenticationMechanisms keeps SCRAM-SHA-256 alongside PLAIN.
    """
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["ldap"] = {
        "servers": openldap.servers,
        "bindMethod": "simple",
        "bindQueryUser": f"cn=admin,{LDAP_BASE}",
        "bindQueryPassword": openldap.admin_password,
        "transportSecurity": "none",
        "validateLDAPServerConfig": True,
    }

    ac["auth"] = {
        "disabled": False,
        "authoritativeSet": False,
        "autoAuthMechanism": "PLAIN",
        "autoAuthMechanisms": ["PLAIN"],
        "autoAuthRestrictions": [],
        "deploymentAuthMechanisms": ["SCRAM-SHA-256", "PLAIN"],
        "autoUser": agent_dn,
        "autoPwd": LDAP_PASSWORD,
        "key": "bXlrZXlmaWxlY29udGVudHM=",
        "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
        "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
        "usersWanted": [
            {
                "user": agent_dn,
                "db": "$external",
                "roles": [{"role": "root", "db": "admin"}],
                "authenticationRestrictions": [],
            },
            {
                "user": app_user_dn,
                "db": "$external",
                "roles": [
                    {"role": "readWrite", "db": "admin"},
                    {"role": "readWrite", "db": "migration_data"},
                ],
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
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
                "version": mdb_version,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {"port": 27017, "tls": {"mode": "disabled"}},
                    "storage": {"dbPath": "/data/"},
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                    "replication": {"replSetName": rs_name},
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


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    # LDAP ($external) users carry no password Secret, so no user_secrets mapping is needed.
    return run_generate_cr(namespace)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str, openldap: OpenLDAP) -> MongoDB:
    # apply_generated_mongodb_resource only applies the MongoDB doc, so create the Secrets the generated
    # CR references (the tool emits them, but the helper does not apply them): the bind-query password
    # and the LDAP agent password.
    def create_ldap_secrets(_resource_doc: dict) -> None:
        create_or_update_secret(namespace, LDAP_BIND_QUERY_SECRET, {"password": openldap.admin_password})
        create_or_update_secret(namespace, LDAP_AGENT_PASSWORD_SECRET, {"password": LDAP_PASSWORD})

    return apply_generated_mongodb_resource(
        namespace,
        generated_cr_yaml,
        customer_sets_disabled_tls_mode=True,
        prepare_external_resources=create_ldap_secrets,
    )


def _ldap_opts(app_user_dn: str) -> list[dict]:
    return [{"username": app_user_dn, "password": LDAP_PASSWORD, "authSource": "$external", "authMechanism": "PLAIN"}]


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB, ldap_app_user: LDAPUser) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mdb_migration.tester(use_ssl=False),
        health_function_params={"attempts": 1, "opts": _ldap_opts(ldap_app_user.username)},
    )


# Test flow


@mark.e2e_vm_migration_replicaset_ldap
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_replicaset_ldap
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sts,
    vm_service,
    custom_mdb_version: str,
    openldap: OpenLDAP,
    ldap_agent_user: LDAPUser,
    ldap_app_user: LDAPUser,
):
    _configure_ac(
        namespace,
        om_tester,
        vm_sts,
        vm_service,
        custom_mdb_version,
        openldap,
        agent_dn=ldap_agent_user.username,
        app_user_dn=ldap_app_user.username,
    )
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_replicaset_ldap
def test_user_connectivity_before_migration(namespace: str, ldap_app_user: LDAPUser):
    """The LDAP application user can authenticate against the VM replica set before migration."""
    vm_replica_set_tester(namespace).assert_ldap_authentication(
        username=ldap_app_user.username, password=LDAP_PASSWORD, attempts=10
    )


@mark.e2e_vm_migration_replicaset_ldap
def test_insert_migration_data(namespace: str, ldap_app_user: LDAPUser):
    insert_migration_data(vm_replica_set_tester(namespace), opts=_ldap_opts(ldap_app_user.username))


@mark.e2e_vm_migration_replicaset_ldap
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# Generated CR checks


@mark.e2e_vm_migration_replicaset_ldap
def test_common_generated_cr_shape(generated_cr_yaml: str, generated_cr: dict, vm_sts: dict):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, vm_sts["spec"]["replicas"])


@mark.e2e_vm_migration_replicaset_ldap
def test_ldap_auth_in_cr(generated_cr: dict):
    auth = generated_cr["spec"]["security"]["authentication"]
    assert "LDAP" in auth["modes"], f"Expected LDAP in auth modes, got: {auth['modes']}"
    ldap_section = auth["ldap"]
    assert ldap_section["bindQueryPasswordSecretRef"]["name"] == LDAP_BIND_QUERY_SECRET
    assert ldap_section["servers"]


@mark.e2e_vm_migration_replicaset_ldap
def test_user_cr_emitted(generated_cr_yaml: str, ldap_app_user: LDAPUser):
    # The $external app user produces a MongoDBUser CR; the LDAP agent auto-user is skipped.
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 1, f"Expected 1 user CR (app user; agent skipped), got {len(user_docs)}"
    assert user_docs[0]["spec"]["db"] == "$external"
    assert user_docs[0]["spec"]["username"] == ldap_app_user.username


# Lifecycle checks


@mark.e2e_vm_migration_replicaset_ldap
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_ldap
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_ldap
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_replicaset_ldap
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_replicaset_ldap
def test_ldap_user_connectivity_after_migration(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    """The migrated $external LDAP user can authenticate via PLAIN after the operator takes over."""
    mdb_migration.tester(use_ssl=False).assert_ldap_authentication(
        username=ldap_app_user.username, password=LDAP_PASSWORD, attempts=10
    )


@mark.e2e_vm_migration_replicaset_ldap
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=_ldap_opts(ldap_app_user.username))


@mark.e2e_vm_migration_replicaset_ldap
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_replicaset_ldap
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_ldap
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_replicaset_ldap
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_ldap
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_ldap
def test_ldap_user_connectivity_after_promote(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    mdb_migration.tester(use_ssl=False).assert_ldap_authentication(
        username=ldap_app_user.username, password=LDAP_PASSWORD, attempts=10
    )


@mark.e2e_vm_migration_replicaset_ldap
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=_ldap_opts(ldap_app_user.username))
