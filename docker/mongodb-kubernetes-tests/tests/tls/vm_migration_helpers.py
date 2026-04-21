"""Shared helpers for VM-to-Kubernetes migration tests that use kubectl-mongodb migrate."""

import difflib
import json
import os
import subprocess
import time
from typing import List, Optional

import yaml
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester, build_mongodb_connection_uri
from kubetester.omtester import OMTester
from kubetester.phase import Phase
from tests import test_logger
from tests.constants import KUBECONFIG_FILEPATH

logger = test_logger.get_test_logger(__name__)

MIGRATE_TOOL = os.getenv("KUBECTL_MONGODB_PATH", "kubectl-mongodb")
MIGRATE_FLAGS = ["--config-map-name", "my-project", "--secret-name", "my-credentials"]
_MIGRATE_ENV = {**os.environ, "KUBECONFIG": os.environ.get("KUBECONFIG", KUBECONFIG_FILEPATH)}


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


def _run_migrate_subcommand(
    subcommand: str,
    extra_flags: list[str],
    stdin_text: str | None,
) -> str:
    """Run a kubectl-mongodb migrate subcommand and return stdout."""
    proc = subprocess.run(
        [MIGRATE_TOOL, "migrate", subcommand, *MIGRATE_FLAGS, *extra_flags],
        input=stdin_text,
        capture_output=True,
        text=True,
        env=_MIGRATE_ENV,
    )
    if proc.returncode != 0:
        logger.error("migrate %s stderr:\n%s", subcommand, proc.stderr)
        raise subprocess.CalledProcessError(proc.returncode, proc.args, proc.stdout, proc.stderr)
    if proc.stderr:
        logger.info("migrate %s stderr:\n%s", subcommand, proc.stderr)
    return proc.stdout


def run_migrate_generate(
    namespace: str,
    passwords: list[str] | None = None,
    certs_secret_prefix: str | None = None,
) -> str:
    """Run migrate mongodb + migrate users and return the combined CR YAML bundle.

    certs_secret_prefix is fed to stdin for the migrate mongodb TLS prompt.
    passwords are fed to stdin for migrate users SCRAM prompts.
    """
    mongodb_stdin = (certs_secret_prefix + "\n") if certs_secret_prefix is not None else None
    mongodb_yaml = _run_migrate_subcommand("mongodb", ["--namespace", namespace], stdin_text=mongodb_stdin)

    users_stdin = "\n".join(passwords) + "\n" if passwords else None
    users_yaml = _run_migrate_subcommand("users", ["--namespace", namespace], stdin_text=users_stdin)

    parts = [p for p in (mongodb_yaml.strip(), users_yaml.strip()) if p]
    return "\n---\n".join(parts) + "\n" if parts else ""


NOISY_AC_KEYS = ("mongoDbVersions",)


def _strip_noisy_fields(ac: dict) -> dict:
    """Return a shallow copy of the AC with large, low-value keys removed for logging."""
    return {k: v for k, v in ac.items() if k not in NOISY_AC_KEYS}


def log_automation_config(ac: dict, label: str = "current"):
    """Log the automation config as pretty-printed JSON, omitting mongoDbVersions."""
    cleaned = _strip_noisy_fields(ac)
    logger.info("Automation config [%s]:\n%s", label, json.dumps(cleaned, indent=2, sort_keys=True))


def log_automation_config_diff(ac_before: dict, ac_after: dict):
    """Log a unified diff of two automation config snapshots, omitting mongoDbVersions."""
    before_lines = json.dumps(_strip_noisy_fields(ac_before), indent=2, sort_keys=True).splitlines(keepends=True)
    after_lines = json.dumps(_strip_noisy_fields(ac_after), indent=2, sort_keys=True).splitlines(keepends=True)
    diff = list(difflib.unified_diff(before_lines, after_lines, fromfile="ac_before", tofile="ac_after"))
    if diff:
        logger.info("Automation config diff:\n%s", "".join(diff))
    else:
        logger.info("No changes in automation config.")


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


def get_user_docs(generated_cr_yaml: str) -> List[dict]:
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    return [d for d in docs if d and d.get("kind") == "MongoDBUser"]


def _apply_secrets(generated_cr_yaml: str, namespace: str):
    """Apply any Secrets from the generated YAML output to the cluster."""
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    for doc in docs:
        if not doc or doc.get("kind") != "Secret":
            continue
        secret_name = doc["metadata"]["name"]
        string_data = doc.get("stringData", {})
        if string_data:
            create_or_update_secret(namespace, secret_name, string_data)


def apply_user_crs_and_verify_ac(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    """Apply every password Secret and MongoDBUser CR, then assert correct AC state."""
    _apply_secrets(generated_cr_yaml, namespace)
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
