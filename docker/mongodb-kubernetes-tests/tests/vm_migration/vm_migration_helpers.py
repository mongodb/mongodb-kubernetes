"""Shared helpers for VM-to-Kubernetes migration tests that use kubectl-mongodb migrate-to-mck."""

import os
import subprocess
import tempfile
import time
from typing import List, Optional

import yaml
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester, build_mongodb_connection_uri
from kubetester.omtester import OMTester
from kubetester.phase import Phase
from tests import test_logger
from tests.constants import KUBECONFIG_FILEPATH

logger = test_logger.get_test_logger(__name__)

KUBECTL_MONGODB = os.getenv("KUBECTL_MONGODB_PATH", "kubectl-mongodb")
GENERATE_CR_FLAGS = ["--config-map-name", "my-project", "--secret-name", "my-credentials"]
_GENERATE_CR_ENV = {**os.environ, "KUBECONFIG": os.environ.get("KUBECONFIG", KUBECONFIG_FILEPATH)}
MIGRATION_DATA_DB = "migration_data"
MIGRATION_DATA_COLLECTION = "sentinel"
MIGRATION_DATA_ID = "vm-migration"
# minimum k8s StatefulSet members deployed alongside VM external members.
# must exceed 7 when added to the external member count so the voting-limit validation always runs.
MIN_K8S_MEMBERS = 5


def deploy_vm_statefulset(
    namespace: str, om_tester: OMTester, extra_volumes=None, extra_volume_mounts=None, extra_command_args=""
):
    """Create or update the VM agent StatefulSet with OM credentials.

    Returns the StatefulSet body dict.
    """
    with open(yaml_fixture("vm_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())
        sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
            {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
            {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
            {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
        ]

    if extra_command_args:
        cmd = sts_body["spec"]["template"]["spec"]["containers"][0]["command"]
        if isinstance(cmd, list) and len(cmd) >= 3 and "-mmsApiKey=" in cmd[2]:
            cmd[2] = cmd[2] + " " + extra_command_args

    if extra_volume_mounts:
        container = sts_body["spec"]["template"]["spec"]["containers"][0]
        container["volumeMounts"] = container.get("volumeMounts", []) + extra_volume_mounts

    if extra_volumes:
        sts_body["spec"]["template"]["spec"].setdefault("volumes", [])
        sts_body["spec"]["template"]["spec"]["volumes"] = (
            sts_body["spec"]["template"]["spec"]["volumes"] + extra_volumes
        )

    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


def deploy_vm_service(namespace: str):
    """Create or update the VM headless service. Returns the Service body dict."""
    with open(yaml_fixture("vm_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def _run_generate_cr_subcommand(
    subcommand: str,
    extra_flags: list[str],
    stdin_text: str | None,
) -> str:
    """Run a kubectl-mongodb migrate-to-mck subcommand and return stdout."""
    proc = subprocess.run(
        [KUBECTL_MONGODB, "migrate-to-mck", subcommand, *GENERATE_CR_FLAGS, *extra_flags],
        input=stdin_text,
        capture_output=True,
        text=True,
        env=_GENERATE_CR_ENV,
    )
    if proc.returncode != 0:
        logger.error("migrate-to-mck %s stderr:\n%s", subcommand, proc.stderr)
        raise subprocess.CalledProcessError(proc.returncode, proc.args, proc.stdout, proc.stderr)
    if proc.stderr:
        logger.info("migrate-to-mck %s stderr:\n%s", subcommand, proc.stderr)
    return proc.stdout


def run_generate_cr(
    namespace: str,
    user_secrets: dict[str, str] | None = None,
    certs_secret_prefix: str | None = None,
    resource_name_override: str | None = None,
    prometheus_secret_name: str | None = None,
) -> str:
    """Run migrate-to-mck mongodb and migrate-to-mck users and return the combined CR YAML bundle.

    certs_secret_prefix is passed as a flag to suppress the migrate-to-mck mongodb TLS prompt.
    prometheus_secret_name is passed to suppress the Prometheus password-Secret prompt; the Secret
    must already exist in the namespace (the tool validates it).
    user_secrets maps "username:database" to a pre-created Secret name. Create each
    Secret before calling this function:
      kubectl create secret generic <name> --from-literal=password=<password> -n <namespace>
    """
    mongodb_flags = ["--namespace", namespace]
    if certs_secret_prefix is not None:
        mongodb_flags += ["--certs-secret-prefix", certs_secret_prefix]
    if resource_name_override is not None:
        mongodb_flags += ["--resource-name-override", resource_name_override]
    if prometheus_secret_name is not None:
        mongodb_flags += ["--prometheus-secret-name", prometheus_secret_name]
    mongodb_yaml = _run_generate_cr_subcommand("mongodb", mongodb_flags, stdin_text=None)

    users_flags = ["--namespace", namespace]
    if resource_name_override is not None:
        users_flags += ["--resource-name-override", resource_name_override]
    tmpfile = None
    if user_secrets:
        with tempfile.NamedTemporaryFile(mode="w", suffix=".csv", delete=False) as f:
            for user_db, secret_name in user_secrets.items():
                f.write(f"{user_db},{secret_name}\n")
            tmpfile = f.name
        users_flags += ["--users-secrets-file", tmpfile]
    try:
        users_yaml = _run_generate_cr_subcommand("users", users_flags, stdin_text=None)
    finally:
        if tmpfile:
            os.unlink(tmpfile)

    parts = [p for p in (mongodb_yaml.strip(), users_yaml.strip()) if p]
    return "\n---\n".join(parts) + "\n" if parts else ""


def generated_docs(generated_cr_yaml: str) -> List[dict]:
    return [doc for doc in yaml.safe_load_all(generated_cr_yaml) if doc]


def generated_mongodb_doc(generated_cr_yaml: str) -> dict:
    return next(doc for doc in generated_docs(generated_cr_yaml) if doc.get("kind") == "MongoDB")


def generated_user_docs(generated_cr_yaml: str) -> List[dict]:
    return [doc for doc in generated_docs(generated_cr_yaml) if doc.get("kind") == "MongoDBUser"]


def apply_generated_mongodb_resource(
    namespace: str,
    generated_cr_yaml: str | dict,
    *,
    resource_name: str | None = None,
    customer_sets_disabled_tls_mode: bool = False,
    prepare_external_resources=None,
) -> MongoDB:
    resource_doc = (
        generated_cr_yaml if isinstance(generated_cr_yaml, dict) else generated_mongodb_doc(generated_cr_yaml)
    )
    resource = MongoDB(resource_name or resource_doc["metadata"]["name"], namespace)
    if try_load(resource):
        return resource

    if customer_sets_disabled_tls_mode:
        # The import tool warns about this but does not own changing no-TLS deployments.
        resource_doc.setdefault("spec", {}).setdefault("additionalMongodConfig", {}).setdefault("net", {}).setdefault(
            "tls", {}
        )["mode"] = "disabled"

    external_count = len(resource_doc["spec"].get("externalMembers", []))
    num_members = max(external_count, MIN_K8S_MEMBERS)
    resource_doc["spec"]["members"] = num_members
    resource_doc["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(num_members)]

    if prepare_external_resources is not None:
        prepare_external_resources(resource_doc)

    resource.backing_obj = resource_doc
    resource.update()
    return resource


def migration_connection_strings(mdb_migration: MongoDB) -> tuple[str, str]:
    secret = KubernetesTester.read_secret(mdb_migration.namespace, f"{mdb_migration.name}-connection-string")
    return secret.get("connectionString.standard", ""), secret.get("connectionString.standardSrv", "")


def k8s_hostnames(mdb_migration: MongoDB) -> list[str]:
    service_name = f"{mdb_migration.name}-svc"
    return [
        f"{mdb_migration.name}-{i}.{service_name}.{mdb_migration.namespace}.svc.cluster.local:27017"
        for i in range(mdb_migration.get_members())
    ]


def assert_connection_string_contains_current_hosts(mdb_migration: MongoDB) -> None:
    conn_str, _ = migration_connection_strings(mdb_migration)
    for hostname in k8s_hostnames(mdb_migration):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from connection string secret"
    for external_member in mdb_migration["spec"].get("externalMembers", []):
        assert (
            external_member["hostname"] in conn_str
        ), f"external member {external_member['hostname']!r} missing from connection string secret"


def assert_connection_string_after_full_migration(mdb_migration: MongoDB) -> None:
    assert not mdb_migration["spec"].get("externalMembers"), "expected all external members to be pruned by now"
    conn_str, conn_srv = migration_connection_strings(mdb_migration)
    replica_set_name = mdb_migration["spec"].get("replicaSetNameOverride", mdb_migration.name)
    assert conn_str.startswith("mongodb://"), "connection string must use mongodb:// scheme"
    for hostname in k8s_hostnames(mdb_migration):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from final connection string"
    assert f"replicaSet={replica_set_name}" in conn_str

    assert conn_srv.startswith("mongodb+srv://"), "SRV connection string must use mongodb+srv:// scheme"
    assert f"{mdb_migration.get_service()}.{mdb_migration.namespace}.svc.cluster.local" in conn_srv
    assert f"replicaSet={replica_set_name}" in conn_srv


def insert_migration_data(mongo_tester: MongoTester, opts: list[dict] | None = None) -> None:
    options = mongo_tester._merge_options(opts or [])
    client = mongo_tester._init_client(**options)
    client[MIGRATION_DATA_DB][MIGRATION_DATA_COLLECTION].replace_one(
        {"_id": MIGRATION_DATA_ID},
        {"_id": MIGRATION_DATA_ID, "source": "vm"},
        upsert=True,
    )


def assert_migration_data_exists(mongo_tester: MongoTester, opts: list[dict] | None = None) -> None:
    options = mongo_tester._merge_options(opts or [])
    client = mongo_tester._init_client(**options)
    assert (
        client[MIGRATION_DATA_DB][MIGRATION_DATA_COLLECTION].count_documents({"_id": MIGRATION_DATA_ID}) == 1
    ), "migration sentinel document is missing"


def assert_k8s_process_names(om_tester: OMTester, mdb_migration: MongoDB) -> None:
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [process["name"] for process in ac_tester.get_all_processes()]
    for i in range(mdb_migration.get_members()):
        assert f"k8s/{mdb_migration.namespace}/{mdb_migration.name}-{i}" in process_names


def assert_max_voting_members_validation(mdb_migration: MongoDB) -> None:
    k8s_members = mdb_migration.get_members()

    for i in range(k8s_members):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1

    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Failed, timeout=300)
    assert "voting members" in mdb_migration.get_status_message()

    for i in range(k8s_members):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "0"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 0

    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=300)


def promote_and_prune(mdb_migration, vm_sts):
    """Promote each VM member and prune it from externalMembers one at a time."""
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
        assert_connection_string_contains_current_hosts(mdb_migration)

        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
        assert_connection_string_contains_current_hosts(mdb_migration)


def vm_replica_set_tester(namespace: str, use_ssl: bool = False, ca_path: Optional[str] = None) -> MongoTester:
    """Return a MongoTester pointed at the VM StatefulSet replica set (vm-mongodb service)."""
    cnx_string = build_mongodb_connection_uri(
        mdb_resource="vm-mongodb",
        namespace=namespace,
        members=3,
        port="27017",
        servicename="vm-mongodb",
    )
    return MongoTester(cnx_string, use_ssl, ca_path)


MIGRATION_IMPORT_TOOL_VERSION_ANNOTATION = "mongodb.com/migrate-tool-version"
MIGRATION_DRY_RUN_ANNOTATION = "mongodb.com/migration-dry-run"


def assert_migration_tool_version_annotation(generated_cr: dict, version: str) -> None:
    assert MIGRATION_IMPORT_TOOL_VERSION_ANNOTATION in generated_cr["metadata"]["annotations"]
    assert generated_cr["metadata"]["annotations"][MIGRATION_IMPORT_TOOL_VERSION_ANNOTATION] == version


def assert_migration_dry_run_annotation(generated_cr_yaml: str) -> None:
    """Assert the first document in the generated YAML carries the migration-dry-run annotation."""
    cr = generated_mongodb_doc(generated_cr_yaml)
    annotations = cr.get("metadata", {}).get("annotations", {})
    assert (
        annotations.get(MIGRATION_DRY_RUN_ANNOTATION) == "true"
    ), f"Expected annotation {MIGRATION_DRY_RUN_ANNOTATION}=true in generated CR, got: {annotations}"


def assert_generated_external_members(generated_cr: dict, expected_count: int = 3) -> None:
    external_members = generated_cr["spec"]["externalMembers"]
    assert (
        len(external_members) == expected_count
    ), f"Expected {expected_count} external members, got {len(external_members)}"
    for external_member in external_members:
        assert isinstance(external_member, dict), f"externalMember should be a dict, got {type(external_member)}"
        for key in ("processName", "hostname", "type", "replicaSetName"):
            assert key in external_member, f"Missing key {key!r} in externalMember: {external_member}"
        assert external_member["type"] == "mongod"


def assert_generated_member_config_omitted(generated_cr: dict) -> None:
    assert (
        "memberConfig" not in generated_cr["spec"]
    ), "Generated CR should not contain memberConfig. Customers set it when expanding."


def assert_common_generated_cr_shape(
    generated_cr_yaml: str, generated_cr: dict, version: str, expected_external_members: int = 3
) -> None:
    assert_migration_dry_run_annotation(generated_cr_yaml)
    assert_migration_tool_version_annotation(generated_cr, version)
    assert_generated_external_members(generated_cr, expected_count=expected_external_members)
    assert_generated_member_config_omitted(generated_cr)


def get_user_docs(generated_cr_yaml: str) -> List[dict]:
    return generated_user_docs(generated_cr_yaml)


def apply_user_crs_and_verify_ac(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    """Apply MongoDBUser CRs from the generated YAML, then assert correct AC state."""
    user_docs = get_user_docs(generated_cr_yaml)
    for doc in user_docs:
        user = MongoDBUser(name=doc["metadata"]["name"], namespace=namespace)
        if not try_load(user):
            user.backing_obj = doc
            user.update()

        try_load(user)
        user.assert_reaches_phase(Phase.Updated, timeout=120)

    ac = om_tester.api_get_automation_config()
    ac_users = {u["user"]: u for u in ac.get("auth", {}).get("usersWanted", []) if u is not None}
    for doc in user_docs:
        username = doc["spec"]["username"]
        ac_user = ac_users.get(username)
        assert ac_user is not None, f"{username} not found in automation config"
        # External (X.509/LDAP) users authenticate via $external and carry no SCRAM credentials.
        if doc["spec"].get("db") == "$external":
            continue
        # The operator manages SCRAM users with both credential sets regardless of the deployment's modes.
        assert ac_user.get("scramSha256Creds") is not None, f"{username}: missing scramSha256Creds"
        assert ac_user.get("scramSha1Creds") is not None, f"{username}: missing scramSha1Creds"


def _wait_for_salt_change(om_tester: OMTester, username: str, old_salt: str, timeout: int = 180):
    """Poll the automation config until the user's scramSha256 salt differs from old_salt."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        ac = om_tester.api_get_automation_config()
        ac_user = next((u for u in ac["auth"]["usersWanted"] if u["user"] == username), None)
        if ac_user and ac_user.get("scramSha256Creds", {}).get("salt") != old_salt:
            return ac_user
        time.sleep(10)
    raise AssertionError(f"Timed out ({timeout}s) waiting for scramSha256 salt to change for user {username!r}")


def rotate_password_and_verify(
    generated_cr_yaml: str,
    namespace: str,
    om_tester: OMTester,
    target_username: Optional[str] = None,
):
    """Rotate the password of a migrated user and verify flag + mechanisms are preserved."""
    user_docs = get_user_docs(generated_cr_yaml)
    assert user_docs, "No user CRs found in generated yaml"

    if target_username:
        user_doc = next(d for d in user_docs if d["spec"]["username"] == target_username)
    else:
        user_doc = user_docs[0]

    username = user_doc["spec"]["username"]
    user = MongoDBUser(name=user_doc["metadata"]["name"], namespace=namespace)
    user.reload()

    ac_before = om_tester.api_get_automation_config()
    ac_user_before = next(u for u in ac_before["auth"]["usersWanted"] if u["user"] == username)
    old_sha256_salt = ac_user_before["scramSha256Creds"]["salt"]

    secret_name = user["spec"]["passwordSecretKeyRef"]["name"]
    secret_key = user["spec"]["passwordSecretKeyRef"].get("key", "password")
    create_or_update_secret(namespace, secret_name, {secret_key: "newRotatedPassword1!"})

    # Secret change doesn't bump the MongoDBUser generation, so
    # assert_reaches_phase would return immediately. Poll the AC instead.
    ac_user = _wait_for_salt_change(om_tester, username, old_sha256_salt, timeout=180)

    user.reload()

    assert ac_user.get("scramSha256Creds") is not None, "scramSha256Creds missing"
    assert ac_user.get("scramSha1Creds") is not None, "scramSha1Creds missing"
