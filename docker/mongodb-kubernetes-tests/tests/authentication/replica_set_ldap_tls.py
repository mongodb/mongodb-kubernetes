import re
import time
from typing import Dict

from kubetester import create_or_update_secret, find_fixture, try_load, wait_until
from kubetester.kubetester import KubernetesTester, automation_process_name, get_default_architecture
from kubetester.ldap import LDAP_AUTHENTICATION_MECHANISM, LDAPUser, OpenLDAP
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser, Role, generic_user
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.replicaset.replica_set_mongodb_proxy import (
    AGENT_CONTAINER,
    DATABASE_CONTAINER,
    mongod_identity,
    pod_members,
    pod_uids,
    query_proxy,
    restart_agent_container,
    restart_counts,
)


@fixture(scope="module")
def operator_installation_config_quick_recovery(operator_installation_config: Dict[str, str]) -> Dict[str, str]:
    """
    This sets the recovery backoff time to 10s for the replicaset reconciliation. It seems like setting it higher
    ensures that the automatic recovery doesn't get triggered at all since the `lastTransitionTime` in the status gets updated
    too often.
    TODO: investigate why and when this mechanism was broken.
    """
    operator_installation_config["customEnvVars"] = (
        operator_installation_config["customEnvVars"] + r"\&MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S=10"
    )
    return operator_installation_config


@fixture(scope="module")
def operator_installation_config(operator_installation_config_quick_recovery: Dict[str, str]) -> Dict[str, str]:
    return operator_installation_config_quick_recovery


@fixture(scope="module")
def replica_set(
    openldap_tls: OpenLDAP,
    issuer_ca_configmap: str,
    namespace: str,
) -> MongoDB:
    """
    This function sets up ReplicaSet resource for testing. The testing procedure includes CLOUDP-189433, that requires
    putting the resource into "Pending" state and the automatically recovering it.
    """
    resource = MongoDB.from_yaml(find_fixture("ldap/ldap-replica-set.yaml"), namespace=namespace)

    secret_name = "bind-query-password"
    create_or_update_secret(namespace, secret_name, {"password": openldap_tls.admin_password})

    resource["spec"]["security"]["authentication"]["ldap"] = {
        "servers": [openldap_tls.servers],
        "bindQueryPasswordSecretRef": {"name": secret_name},
        "transportSecurity": "none",  # For testing CLOUDP-189433
        "validateLDAPServerConfig": True,
        "caConfigMapRef": {"name": issuer_ca_configmap, "key": "ca-pem"},
    }

    resource.update()
    return resource


@fixture(scope="module")
def ldap_user_mongodb(replica_set: MongoDB, namespace: str, ldap_mongodb_user_tls: LDAPUser) -> MongoDBUser:
    """Returns a list of MongoDBUsers (already created) and their corresponding passwords."""
    user = generic_user(
        namespace,
        username=ldap_mongodb_user_tls.username,
        db="$external",
        mongodb_resource=replica_set,
        password=ldap_mongodb_user_tls.password,
    )
    user.add_roles(
        [
            Role(db="admin", role="clusterAdmin"),
            Role(db="admin", role="readWriteAnyDatabase"),
            Role(db="admin", role="dbAdminAnyDatabase"),
        ]
    )

    try_load(user)
    return user


@mark.e2e_replica_set_ldap_tls
def test_replica_set_pending_CLOUDP_189433(replica_set: MongoDB):
    """
    This function tests CLOUDP-189433. The resource needs to enter the "Pending" state and without the automatic
    recovery, it would stay like this forever (since we wouldn't push the new AC with a fix).
    """
    replica_set.assert_reaches_phase(Phase.Pending, timeout=100)


@mark.e2e_replica_set_ldap_tls
def test_turn_tls_on_CLOUDP_189433(replica_set: MongoDB):
    """
    This function tests CLOUDP-189433. The user attempts to fix the AutomationConfig.
    Before updating the AutomationConfig, we need to ensure the operator pushed the wrong one to Ops Manager.
    """

    def wait_for_ac_pushed() -> bool:
        ac = replica_set.get_automation_config_tester().automation_config
        try:
            transport_security = ac["ldap"]["transportSecurity"]
            version = ac["version"]
            if version < 4:
                return False
            if transport_security != "none":
                return False
            return True
        except KeyError:
            return False

    wait_until(wait_for_ac_pushed, timeout=200)

    resource = replica_set.load()
    resource["spec"]["security"]["authentication"]["ldap"]["transportSecurity"] = "tls"
    resource.update()


@mark.e2e_replica_set_ldap_tls
def test_replica_set_CLOUDP_189433(replica_set: MongoDB):
    """
    This function tests CLOUDP-189433. The recovery mechanism kicks in and pushes Automation Config. The ReplicaSet
    goes into running state.
    """
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replica_set_ldap_tls
def test_create_ldap_user(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    ldap_user_mongodb.update()
    ldap_user_mongodb.assert_reaches_phase(Phase.Updated)

    ac = replica_set.get_automation_config_tester()
    ac.assert_authentication_mechanism_enabled(LDAP_AUTHENTICATION_MECHANISM, active_auth_mechanism=False)
    ac.assert_expected_users(1)


@mark.e2e_replica_set_ldap_tls
def test_new_ldap_users_can_authenticate(replica_set: MongoDB, ldap_user_mongodb: MongoDBUser):
    tester = replica_set.tester()

    tester.assert_ldap_authentication(ldap_user_mongodb["spec"]["username"], ldap_user_mongodb.password, attempts=10)


def assert_agent_observation_logs(replica_set: MongoDB, namespace: str):
    for pod in pod_members(replica_set):
        for name in ("automation-agent.log", "automation-agent-verbose.log"):
            raw = KubernetesTester.run_command_in_pod_container(
                pod,
                namespace,
                ["cat", f"/var/log/mongodb-mms-automation/{name}"],
                container=AGENT_CONTAINER,
            )
            assert raw.strip(), f"{pod}: {name} is empty"
            assert "Error reading environment for pid" not in raw, f"{pod}: {name} contains an environment-read error"
            assert "Error getting LDAP CA path for pid" not in raw, f"{pod}: {name} contains an LDAP observation error"
            failures = re.findall(
                r"Postcondition failed for step Start(?:Fresh)? because[^\n]*\n"
                r"(?:(?!^\[\d{4}-)[\s\S])*?\['currentState\.LdapCaPath' != 'desiredState\.LdapCaPath'",
                raw,
                re.MULTILINE,
            )
            assert len(failures) == 0, f"{pod}: {name} contains {len(failures)} LDAP Start postcondition failures"


@mark.e2e_replica_set_ldap_tls
@mark.skipif(get_default_architecture() != "static-ga", reason="Agent-only restart requires the static-ga proxy")
def test_agent_restart_retains_ldap_launch_environment(replica_set: MongoDB, namespace: str):
    """Restarting the agent must not restart mongod in static-ga.

    The proxy owns the mongod child in the database container, and the agent reaches it over HTTP.
    The restart must keep the mongod PID and its launch environment, including the LDAP CA path.
    Legacy static shares the process namespace, so the agent restart would restart the child there.
    """
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)
    pods = pod_members(replica_set)
    assert pods, "the LDAP replica set has no members"
    identities = {pod: mongod_identity(pod, namespace) for pod in pods}
    assert all(identities.values()), "a database child is not running"
    uids = pod_uids(replica_set, namespace)
    restarts = {pod: restart_counts(pod, namespace) for pod in pods}
    snapshots = {pod: query_proxy(pod, namespace) for pod in pods}
    for pod, status in snapshots.items():
        assert status["state"] == "running", f"{pod}: proxy does not report running"
        assert status["pid"] == int(identities[pod].split(":")[0]), f"{pod}: child PID does not match proxy status"
        assert status["launchEnvironment"].get("ldapCaPath"), f"{pod}: proxy did not retain the LDAP CA path"
        assert set(status["launchEnvironment"]) <= {"ldapCaPath", "kerberosKeytab"}
    assert_agent_observation_logs(replica_set, namespace)

    restarted_pod = pods[0]
    restart_agent_container(restarted_pod, namespace)
    time.sleep(60)

    def agents_converged() -> bool:
        status = KubernetesTester.get_automation_status(group_id=replica_set.get_om_tester().context.project_id)
        goal = replica_set.get_automation_config_tester().automation_config["version"]
        # Ops Manager names a process "k8s/<namespace>/<pod>", not by the pod name alone.
        processes = {process["name"]: process for process in status["processes"]}
        return status["goalVersion"] == goal and all(
            automation_process_name(namespace, pod) in processes
            and processes[automation_process_name(namespace, pod)]["lastGoalVersionAchieved"] == goal
            for pod in pods
        )

    wait_until(agents_converged, timeout=600)
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)
    assert pod_uids(replica_set, namespace) == uids, "a pod was replaced"
    for pod in pods:
        after = restart_counts(pod, namespace)
        assert after[DATABASE_CONTAINER] == restarts[pod][DATABASE_CONTAINER], f"{pod}: database container restarted"
        if pod == restarted_pod:
            assert after[AGENT_CONTAINER] > restarts[pod][AGENT_CONTAINER]
        assert mongod_identity(pod, namespace) == identities[pod], f"{pod}: database child restarted"
        current = query_proxy(pod, namespace)
        assert current["state"] == "running", f"{pod}: proxy does not report running"
        assert current["pid"] == snapshots[pod]["pid"], f"{pod}: proxy child PID changed"
        assert current["launchEnvironment"] == snapshots[pod]["launchEnvironment"], f"{pod}: launch environment changed"
    assert_agent_observation_logs(replica_set, namespace)


@mark.e2e_replica_set_ldap_tls
def test_remove_ldap_settings(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    replica_set.load()
    replica_set["spec"]["security"]["authentication"]["ldap"] = None
    replica_set["spec"]["security"]["authentication"]["modes"] = ["SCRAM"]
    replica_set.update()

    def wait_for_ac_pushed() -> bool:
        ac = replica_set.get_automation_config_tester()
        try:
            ac.assert_authentication_mechanism_disabled(LDAP_AUTHENTICATION_MECHANISM, check_auth_mechanism=False)
            return True
        except AssertionError:
            return False

    wait_until(wait_for_ac_pushed, timeout=400)

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_ldap_tls
@mark.skipif(get_default_architecture() != "static-ga", reason="Proxy observation checks require static-ga")
def test_agent_logs_have_no_ldap_observation_failures(replica_set: MongoDB, namespace: str):
    assert_agent_observation_logs(replica_set, namespace)
