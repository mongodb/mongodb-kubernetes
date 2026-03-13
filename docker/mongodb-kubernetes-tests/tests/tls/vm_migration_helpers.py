"""Shared helpers for VM-to-Kubernetes migration tests that use kubectl-mongodb migrate generate."""

import difflib
import json
import os
import subprocess

import yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.omtester import OMTester
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

MIGRATE_TOOL = os.getenv("KUBECTL_MONGODB_PATH", "kubectl-mongodb")
MIGRATE_FLAGS = ["--config-map-name", "my-project", "--secret-name", "my-credentials"]


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


def run_migrate_generate(
    namespace: str,
    passwords: list[str] | None = None,
    certs_secret_prefix: str | None = None,
) -> str:
    """Run kubectl-mongodb migrate generate and return stdout (the CR YAML).

    If certs_secret_prefix is provided (e.g. "mdb"), it is sent first to stdin
    for the TLS certsSecretPrefix prompt when the deployment has TLS enabled.
    If passwords is provided, they are fed to stdin (after the cert prefix when
    present) one per line for SCRAM user prompts.
    """
    stdin_lines = []
    if certs_secret_prefix is not None:
        stdin_lines.append(certs_secret_prefix)
    if passwords:
        stdin_lines.extend(passwords)
    stdin_text = "\n".join(stdin_lines) + "\n" if stdin_lines else None

    proc = subprocess.run(
        [MIGRATE_TOOL, "migrate", "generate", *MIGRATE_FLAGS, "--namespace", namespace],
        input=stdin_text,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        logger.error("migrate generate stderr:\n%s", proc.stderr)
        raise subprocess.CalledProcessError(proc.returncode, proc.args, proc.stdout, proc.stderr)
    if proc.stderr:
        logger.info("migrate generate stderr:\n%s", proc.stderr)
    return proc.stdout


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
