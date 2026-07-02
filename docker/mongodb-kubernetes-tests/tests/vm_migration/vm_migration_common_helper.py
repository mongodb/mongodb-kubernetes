"""Shared helpers for VM-to-Kubernetes migration tests that use kubectl-mongodb migrate-to-mck.

Contains code common to both replica set and sharded cluster migrations: CR generation,
migration-data checks, user CR application, password rotation, and the shared StatefulSet
deploy primitive. Replica-set-specific helpers live in vm_migration_replicaset_helper and
sharded-cluster-specific helpers in vm_migration_sharded_helper.
"""

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
from kubetester.mongotester import MongoTester
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
MIGRATION_DRY_RUN_ANNOTATION = "mongodb.com/migration-dry-run"


def _deploy_vm_statefulset_from_fixture(
    fixture_name: str,
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args: str = "",
    replicas: Optional[int] = None,
) -> dict:
    with open(yaml_fixture(fixture_name), "r") as f:
        sts_body = yaml.safe_load(f.read())
    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]
    if replicas is not None:
        sts_body["spec"]["replicas"] = replicas
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


def assert_max_voting_members_validation(mdb_migration: MongoDB) -> None:
    k8s_members = len(mdb_migration["spec"]["memberConfig"])

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


def assert_migration_dry_run_annotation(generated_cr_yaml: str) -> None:
    """Assert the first document in the generated YAML carries the migration-dry-run annotation."""
    cr = generated_mongodb_doc(generated_cr_yaml)
    annotations = cr.get("metadata", {}).get("annotations", {})
    assert (
        annotations.get(MIGRATION_DRY_RUN_ANNOTATION) == "true"
    ), f"Expected annotation {MIGRATION_DRY_RUN_ANNOTATION}=true in generated CR, got: {annotations}"


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
        user.assert_reaches_phase(Phase.Updated, timeout=600)

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
