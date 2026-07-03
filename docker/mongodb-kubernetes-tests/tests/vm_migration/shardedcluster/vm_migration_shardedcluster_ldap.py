"""
VM migration for a sharded cluster with LDAP (PLAIN) authentication.

Mirrors vm_migration_replicaset_ldap.py adapted for sharded cluster:
two StatefulSets (mongod and mongos), config server promote/prune, and
mongos-tester for connectivity checks.
"""

from kubetester import create_or_update_secret, get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, skip_if_local
from kubetester.ldap import LDAPUser, OpenLDAP, add_user_to_group, create_user, ensure_group, ensure_organizational_unit
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.authentication.conftest import LDAP_PASSWORD, openldap_install
from tests.vm_migration.vm_migration_common_helper import (
    apply_user_crs_and_verify_ac,
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_sharded_helper import (
    MIN_VM_CONFIGSRV,
    MIN_VM_MONGOS,
    MIN_VM_SHARD,
    apply_generated_sharded_cluster_resource,
    assert_common_generated_sharded_cr_shape,
    assert_connection_string_after_full_sharded_migration,
    assert_k8s_sharded_process_names,
    build_sharded_cluster_ac,
    deploy_vm_sharded_configsrv_service,
    deploy_vm_sharded_configsrv_statefulset,
    deploy_vm_sharded_mongos_service,
    deploy_vm_sharded_mongos_statefulset,
    deploy_vm_sharded_shard_service,
    deploy_vm_sharded_shard_statefulset,
    vm_mongos_tester,
)

CONFIGSRV_STS_NAME = "vm-sharded-configsrv"
SHARD_STS_NAME = "vm-sharded-shard"
MONGOS_STS_NAME = "vm-sharded-mongos"
CONFIGSRV_SVC_NAME = "vm-sharded-configsrv"
SHARD_SVC_NAME = "vm-sharded-shard"
MONGOS_SVC_NAME = "vm-sharded-mongos"
MDB_RESOURCE_NAME = "sharded-migration"
VM_CONFIG_RS_NAME = "vm-config"
VM_SHARD_RS_NAME = "vm-shard-0"
VM_MONGOS_NAME = "vm-mongos"

LDAP_BASE = "dc=example,dc=org"
AGENT_UID = "mms-automation-agent"
APP_USER_UID = "vm-ldap-app-user"
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
    user = LDAPUser(AGENT_UID, LDAP_PASSWORD, ou="groups")
    ensure_organizational_unit(openldap, "groups")
    create_user(openldap, user, ou="groups")
    ensure_group(openldap, cn="agents", ou="groups")
    add_user_to_group(openldap, user=AGENT_UID, group_cn="agents", ou="groups")
    return user


@fixture(scope="module")
def ldap_app_user(openldap: OpenLDAP) -> LDAPUser:
    user = LDAPUser(APP_USER_UID, LDAP_PASSWORD)
    create_user(openldap, user)
    return user


@fixture(scope="module")
def vm_sharded_configsrv_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_configsrv_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_shard_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_shard_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_mongos_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_mongos_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_configsrv_service(namespace: str):
    return deploy_vm_sharded_configsrv_service(namespace)


@fixture(scope="module")
def vm_sharded_shard_service(namespace: str):
    return deploy_vm_sharded_shard_service(namespace)


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    return deploy_vm_sharded_mongos_service(namespace)


def _configure_ac(
    namespace: str,
    om_tester: OMTester,
    mdb_version: str,
    openldap: OpenLDAP,
    agent_dn: str,
    app_user_dn: str,
) -> None:
    ac_existing = om_tester.api_get_automation_config()
    if len(ac_existing.get("processes", [])) > 0:
        return
    ac = build_sharded_cluster_ac(
        om_tester,
        configsrv_sts_name=CONFIGSRV_STS_NAME,
        shard_sts_name=SHARD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        configsrv_service_name=CONFIGSRV_SVC_NAME,
        shard_service_name=SHARD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=mdb_version,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=MIN_VM_CONFIGSRV,
        shard_count=MIN_VM_SHARD,
        mongos_count=MIN_VM_MONGOS,
        cluster_name=VM_MONGOS_NAME,
    )
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
    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_generate_cr(namespace, resource_name_override=MDB_RESOURCE_NAME)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str, openldap: OpenLDAP) -> MongoDB:
    def create_ldap_secrets(_resource_doc: dict) -> None:
        create_or_update_secret(namespace, LDAP_BIND_QUERY_SECRET, {"password": openldap.admin_password})
        create_or_update_secret(namespace, LDAP_AGENT_PASSWORD_SECRET, {"password": LDAP_PASSWORD})

    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
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


@mark.e2e_vm_migration_shardedcluster_ldap
def test_deploy_vm_sharded(
    namespace: str,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
):
    def configsrv_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_configsrv_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_configsrv_sts["spec"]["replicas"]

    def shard_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_shard_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_shard_sts["spec"]["replicas"]

    def mongos_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_mongos_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongos_sts["spec"]["replicas"]

    KubernetesTester.wait_until(configsrv_sts_is_ready, timeout=300)
    KubernetesTester.wait_until(shard_sts_is_ready, timeout=300)
    KubernetesTester.wait_until(mongos_sts_is_ready, timeout=300)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
    custom_mdb_version: str,
    openldap: OpenLDAP,
    ldap_agent_user: LDAPUser,
    ldap_app_user: LDAPUser,
):
    _configure_ac(
        namespace,
        om_tester,
        ensure_ent_version(custom_mdb_version),
        openldap,
        agent_dn=ldap_agent_user.username,
        app_user_dn=ldap_app_user.username,
    )
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_user_connectivity_before_migration(namespace: str, ldap_app_user: LDAPUser):
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_ldap_authentication(
        username=ldap_app_user.username, password=LDAP_PASSWORD, attempts=10
    )


@mark.e2e_vm_migration_shardedcluster_ldap
def test_insert_migration_data(namespace: str, ldap_app_user: LDAPUser):
    insert_migration_data(
        vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace),
        opts=_ldap_opts(ldap_app_user.username),
    )


@mark.e2e_vm_migration_shardedcluster_ldap
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_shardedcluster_ldap
def test_common_generated_cr_shape(generated_cr: dict):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=MIN_VM_CONFIGSRV,
        expected_shard_count=MIN_VM_SHARD,
        expected_mongos_count=MIN_VM_MONGOS,
    )


@mark.e2e_vm_migration_shardedcluster_ldap
def test_ldap_auth_in_cr(generated_cr: dict):
    auth = generated_cr["spec"]["security"]["authentication"]
    assert "LDAP" in auth["modes"], f"Expected LDAP in auth modes, got: {auth['modes']}"
    ldap_section = auth["ldap"]
    assert ldap_section["bindQueryPasswordSecretRef"]["name"] == LDAP_BIND_QUERY_SECRET
    assert ldap_section["servers"]


@mark.e2e_vm_migration_shardedcluster_ldap
def test_user_cr_emitted(generated_cr_yaml: str, ldap_app_user: LDAPUser):
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 1, f"Expected 1 user CR (app user; agent skipped), got {len(user_docs)}"
    assert user_docs[0]["spec"]["db"] == "$external"
    assert user_docs[0]["spec"]["username"] == ldap_app_user.username


@mark.e2e_vm_migration_shardedcluster_ldap
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_ldap_user_connectivity_after_migration(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    mdb_migration.tester(use_ssl=False).assert_ldap_authentication(
        username=ldap_app_user.username, password=LDAP_PASSWORD, attempts=10
    )


@mark.e2e_vm_migration_shardedcluster_ldap
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=_ldap_opts(ldap_app_user.username))


@mark.e2e_vm_migration_shardedcluster_ldap
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_ldap
def test_promote_and_prune_config_server(mdb_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_migration)
    for i in range(MIN_VM_CONFIGSRV):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        config_external = [
            m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == VM_CONFIG_RS_NAME
        ]
        if config_external:
            mdb_migration["spec"]["externalMembers"].remove(config_external[-1])
            mdb_migration.update()
            mdb_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_migration)
    shard_external = [
        m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == VM_SHARD_RS_NAME
    ]
    for _ in range(len(shard_external)):
        current = [m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == VM_SHARD_RS_NAME]
        if not current:
            break
        mdb_migration["spec"]["externalMembers"].remove(current[-1])
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)
        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_ldap
@skip_if_local()
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_ldap
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_ldap
def test_ldap_user_connectivity_after_promote(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    mdb_migration.tester(use_ssl=False).assert_ldap_authentication(
        username=ldap_app_user.username, password=LDAP_PASSWORD, attempts=10
    )


@mark.e2e_vm_migration_shardedcluster_ldap
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, ldap_app_user: LDAPUser):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=_ldap_opts(ldap_app_user.username))
