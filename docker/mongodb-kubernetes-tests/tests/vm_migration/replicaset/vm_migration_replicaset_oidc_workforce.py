"""
VM migration from a generated MongoDB resource with OIDC WorkforceIdentityFederation authentication.

The VM automation config enables MONGODB-OIDC alongside SCRAM. OIDC uses workforce identity with
group-based authorization (useAuthorizationClaim=true, supportsHumanFlows=true). The Cognito group
"test" is mapped to the $external user OIDC-test-workforce/test. This verifies that the migrate tool
carries the OIDC provider config into the generated CR with authorizationMethod=WorkforceIdentityFederation
and authorizationType=GroupMembership.

Requires the Cognito test environment (the cognito_* expansions). The test skips when they are absent.
"""

import kubetester.oidc as oidc
from kubetester import create_or_update_secret, get_statefulset
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.mongotester import with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.scram import build_sha256_creds
from pytest import fixture, mark, skip
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_helpers import (
    apply_generated_mongodb_resource,
    apply_user_crs_and_verify_ac,
    assert_common_generated_cr_shape,
    assert_connection_string_after_full_migration,
    assert_connection_string_contains_current_hosts,
    assert_k8s_process_names,
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    deploy_vm_service,
    deploy_vm_statefulset,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    promote_and_prune,
    run_generate_cr,
    vm_replica_set_tester,
)

AGENT_USER = "mms-automation-agent"
AGENT_PASSWORD = "agent-oidc-workforce-password"
APP_USER = "app-user"
APP_USER_PASSWORD = "appUserOidcWorkforce!"
SCRAM_MECHANISM = "SCRAM-SHA-256"
OIDC_MECHANISM = "MONGODB-OIDC"
MDB_VERSION = "8.0.4-ent"
OIDC_CONFIG_NAME = "OIDC-test-workforce"
COGNITO_GROUP = "test"


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
    cognito: dict,
):
    """SCRAM agent plus a workforce OIDC provider using group-based authorization with human flows enabled.

    WorkforceIdentityFederation + GroupMembership uses a role in admin named <prefix>/<group>.
    MongoDB resolves the cognito:groups claim from the JWT and grants the matching admin role.
    """
    mdb_version = MDB_VERSION
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

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
            "supportsHumanFlows": True,
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
    create_or_update_secret(namespace, "app-user-secret", {"password": APP_USER_PASSWORD})
    return run_generate_cr(namespace, user_secrets={f"{APP_USER}:admin": "app-user-secret"})


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    return apply_generated_mongodb_resource(namespace, generated_cr_yaml, customer_sets_disabled_tls_mode=True)


@fixture(scope="module")
def scram_opts() -> list[dict]:
    return [with_scram(APP_USER, APP_USER_PASSWORD, SCRAM_MECHANISM)]


# Test flow


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=600)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sts,
    vm_service,
    cognito: dict,
):
    _configure_ac(namespace, om_tester, vm_sts, vm_service, cognito)
    om_tester.wait_agents_ready(timeout=1200)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_user_connectivity_before_migration(namespace: str):
    vm_replica_set_tester(namespace).assert_scram_sha_authentication(
        username=APP_USER, password=APP_USER_PASSWORD, auth_mechanism=SCRAM_MECHANISM
    )


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_oidc_authentication_before_migration(namespace: str):
    vm_replica_set_tester(namespace, use_ssl=False).assert_oidc_authentication()


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_insert_migration_data(namespace: str, scram_opts: list[dict]):
    insert_migration_data(vm_replica_set_tester(namespace), opts=scram_opts)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# Generated CR checks


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_common_generated_cr_shape(generated_cr_yaml: str, generated_cr: dict, vm_sts: dict):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, vm_sts["spec"]["replicas"])


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_oidc_provider_in_cr(generated_cr: dict):
    auth = generated_cr["spec"]["security"]["authentication"]
    assert "OIDC" in auth["modes"]
    configs = auth["oidcProviderConfigs"]
    names = {c["configurationName"] for c in configs}
    assert names == {OIDC_CONFIG_NAME}, f"Unexpected OIDC provider configs: {configs}"
    provider = configs[0]
    assert provider["authorizationMethod"] == "WorkforceIdentityFederation"
    assert provider["authorizationType"] == "GroupMembership"


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_user_crs_emitted(generated_cr_yaml: str):
    user_docs = generated_user_docs(generated_cr_yaml)
    usernames = {d["spec"]["username"] for d in user_docs}
    assert usernames == {APP_USER}, f"Unexpected user CRs: {usernames}"


# Lifecycle checks


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=2400)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_scram_user_connectivity_after_migration(mdb_migration: MongoDB):
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username=APP_USER, password=APP_USER_PASSWORD, auth_mechanism=SCRAM_MECHANISM
    )


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_oidc_authentication_after_migration(mdb_migration: MongoDB):
    mdb_migration.tester(use_ssl=False).assert_oidc_authentication()


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_oidc_workforce
def test_oidc_authentication_after_promote(mdb_migration: MongoDB):
    mdb_migration.tester(use_ssl=False).assert_oidc_authentication()
