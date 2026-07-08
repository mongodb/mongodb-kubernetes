"""
VM migration for a sharded cluster with OIDC workload GroupMembership authentication.

The VM automation config enables MONGODB-OIDC alongside SCRAM. OIDC uses workload identity with
group-based authorization (useAuthorizationClaim=true, supportsHumanFlows=false). The Cognito group
"test" is mapped to the $external user OIDC-test-group/test. This verifies that the migrate tool
carries the OIDC provider config into the generated CR with authorizationMethod=WorkloadIdentityFederation
and authorizationType=GroupMembership.

Requires the Cognito test environment (the cognito_* expansions). The test skips when they are absent.
"""

import json

import kubetester.oidc as oidc
from kubetester import create_or_update_secret, get_statefulset, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongotester import with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.scram import build_sha256_creds
from pytest import fixture, mark, skip
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
    promote_and_prune_shard,
    vm_mongos_tester,
)

AGENT_USER = "mms-automation-agent"
AGENT_PASSWORD = "agent-oidc-group-password"
APP_USER = "app-user"
APP_USER_PASSWORD = "appUserOidcGroup!"
SCRAM_MECHANISM = "SCRAM-SHA-256"
OIDC_MECHANISM = "MONGODB-OIDC"
MDB_VERSION = "8.0.4-ent"
OIDC_CONFIG_NAME = "OIDC-test-group"
COGNITO_GROUP = "test"

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


@fixture(scope="module")
def cognito() -> dict:
    client_id = oidc.get_cognito_workload_client_id()
    issuer_uri = oidc.get_cognito_workload_url()
    user_id = oidc.get_cognito_workload_user_id()
    if not client_id or not issuer_uri or not user_id:
        skip("Cognito OIDC test environment is not configured (cognito_* expansions missing)")
    return {"client_id": client_id, "issuer_uri": issuer_uri, "user_id": user_id}


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


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
    vm_sharded_configsrv_sts: dict,
    vm_sharded_shard_sts: dict,
    vm_sharded_mongos_sts: dict,
    vm_sharded_configsrv_service: dict,
    vm_sharded_shard_service: dict,
    vm_sharded_mongos_service: dict,
    mdb_version: str,
    cognito: dict,
):
    """SCRAM agent plus a workload OIDC provider using group-based authorization.

    GroupMembership OIDC uses a role in admin named <prefix>/<group> rather than a
    user in $external. MongoDB resolves the cognito:groups claim from the JWT, then
    grants the matching admin role to the connection.
    """
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

    ac["auth"] = {
        "usersWanted": [
            {
                "user": AGENT_USER,
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": [SCRAM_MECHANISM],
                "scramSha256Creds": build_sha256_creds(AGENT_PASSWORD),
                "authenticationRestrictions": [],
            },
            {
                "user": APP_USER,
                "db": "admin",
                "roles": [
                    {"role": "readWrite", "db": "admin"},
                    {"role": "readWrite", "db": "migration_data"},
                ],
                "mechanisms": [SCRAM_MECHANISM],
                "scramSha256Creds": build_sha256_creds(APP_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
        "disabled": False,
        "authoritativeSet": False,
        "deploymentAuthMechanisms": [SCRAM_MECHANISM, OIDC_MECHANISM],
        "autoAuthMechanisms": [SCRAM_MECHANISM],
        "autoAuthMechanism": SCRAM_MECHANISM,
        "autoUser": AGENT_USER,
        "autoAuthRestrictions": [],
        "autoPwd": AGENT_PASSWORD,
        "key": "bXlrZXlmaWxlY29udGVudHM=",
        "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
        "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
    }

    ac["oidcProviderConfigs"] = [
        {
            "authNamePrefix": OIDC_CONFIG_NAME,
            "audience": cognito["client_id"],
            "issuerUri": cognito["issuer_uri"],
            "clientId": cognito["client_id"],
            "requestedScopes": [],
            "userClaim": "sub",
            "groupsClaim": "cognito:groups",
            "supportsHumanFlows": False,
            "useAuthorizationClaim": True,
        }
    ]

    ac["roles"] = [
        {
            "role": f"{OIDC_CONFIG_NAME}/{COGNITO_GROUP}",
            "db": "admin",
            "privileges": [],
            "roles": [{"role": "readWriteAnyDatabase", "db": "admin"}],
        }
    ]
    oidc_providers = json.dumps(
        [
            {
                "issuer": cognito["issuer_uri"],
                "audience": cognito["client_id"],
                "authNamePrefix": OIDC_CONFIG_NAME,
                "authorizationClaim": "cognito:groups",
                "useAuthorizationClaim": True,
                "supportsHumanFlows": False,
            }
        ]
    )
    for process in ac["processes"]:
        process["args2_6"]["setParameter"] = {
            "authenticationMechanisms": f"{SCRAM_MECHANISM},{OIDC_MECHANISM}",
            "oidcIdentityProviders": oidc_providers,
        }
    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    create_or_update_secret(namespace, "app-user-secret", {"password": APP_USER_PASSWORD})
    return run_generate_cr(
        namespace, resource_name_override=MDB_RESOURCE_NAME, user_secrets={f"{APP_USER}:admin": "app-user-secret"}
    )


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
        customer_sets_disabled_tls_mode=True,
    )


@fixture(scope="module")
def scram_opts() -> list[dict]:
    return [with_scram(APP_USER, APP_USER_PASSWORD, SCRAM_MECHANISM)]


# Test flow


@mark.e2e_vm_migration_shardedcluster_oidc_group
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


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
    cognito: dict,
):
    _configure_ac(
        namespace,
        om_tester,
        vm_sharded_configsrv_sts,
        vm_sharded_shard_sts,
        vm_sharded_mongos_sts,
        vm_sharded_configsrv_service,
        vm_sharded_shard_service,
        vm_sharded_mongos_service,
        MDB_VERSION,
        cognito,
    )
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_user_connectivity_before_migration(namespace: str):
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_scram_sha_authentication(
        username=APP_USER, password=APP_USER_PASSWORD, auth_mechanism=SCRAM_MECHANISM
    )


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_oidc_authentication_before_migration(namespace: str):
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_oidc_authentication()


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_insert_migration_data(namespace: str, scram_opts: list[dict]):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# Generated CR checks


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_common_generated_cr_shape(generated_cr: dict, version_id: str):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=MIN_VM_CONFIGSRV,
        expected_shard_count=MIN_VM_SHARD,
        expected_mongos_count=MIN_VM_MONGOS,
        version_id=version_id,
    )


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_oidc_provider_in_cr(generated_cr: dict):
    auth = generated_cr["spec"]["security"]["authentication"]
    assert "OIDC" in auth["modes"]
    configs = auth["oidcProviderConfigs"]
    names = {c["configurationName"] for c in configs}
    assert names == {OIDC_CONFIG_NAME}, f"Unexpected OIDC provider configs: {configs}"
    provider = configs[0]
    assert provider["authorizationMethod"] == "WorkloadIdentityFederation"
    assert provider["authorizationType"] == "GroupMembership"


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_user_crs_emitted(generated_cr_yaml: str):
    user_docs = generated_user_docs(generated_cr_yaml)
    usernames = {d["spec"]["username"] for d in user_docs}
    assert usernames == {APP_USER}, f"Unexpected user CRs: {usernames}"


# Lifecycle checks


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_scram_user_connectivity_after_migration(mdb_migration: MongoDB):
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username=APP_USER, password=APP_USER_PASSWORD, auth_mechanism=SCRAM_MECHANISM
    )


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_oidc_authentication_after_migration(mdb_migration: MongoDB):
    mdb_migration.tester(use_ssl=False).assert_oidc_authentication()


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_oidc_group
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


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    promote_and_prune_shard(mdb_migration, om_tester, VM_SHARD_RS_NAME, VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_oidc_group
def test_oidc_authentication_after_promote(mdb_migration: MongoDB):
    mdb_migration.tester(use_ssl=False).assert_oidc_authentication()
