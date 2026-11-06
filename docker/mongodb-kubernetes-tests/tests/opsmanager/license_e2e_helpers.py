"""Shared helpers, fixtures, and constants for Ops Manager license E2E tests."""

import base64
import json
import re
import time
import uuid
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from typing import Optional

import pytest
import requests
from kubernetes import client as k8s_client
from kubernetes.client.rest import ApiException
from kubetester import create_or_update_secret, delete_secret, read_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests import test_logger
from tests.conftest import is_multi_cluster
from tests.opsmanager.ea_license import (
    DEFAULT_VALIDITY_DAYS,
    GsaCredentials,
    ReleaseReceipt,
    artifact_id_of,
    holder_state,
    mint_license,
    read_artifact_inventory,
    revoke_license,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

logger = test_logger.get_test_logger(__name__)

OM_NAME = "om-license-e2e"
DEFAULT_TIMEOUT = 90
# The Ops Manager EA licensing suite waits up to 10 minutes on cron driven state and fires the cron
# again about once a minute, wait_for polls every 3 seconds.
CRON_TIMEOUT = 600
CRON_RETRIGGER_POLLS = 20

GSA_SECRET_NAME = "om-license-gsa"

ROTATE_IDENTITY_ANNOTATION = "mongodb.com/license-rotate-identity"
RECOMPUTE_ANNOTATION = "mongodb.com/license-recompute"

LICENSE_SECRET_NAME = "om-license-artifact"
LICENSE_SECRET_KEY = "license"
RECEIPT_SECRET_KEY = "release-receipt"
RECEIPT_SECRET_SUFFIX = "-release-receipt"

# Unique per run, so inventory reads only see this run's licenses.
RUN_PRINCIPAL_ID = f"mck-ci-{uuid.uuid4()}"

RELEASED_STATUS = "RELEASED"


# Ops Manager runs these hourly, the tests trigger them instead of waiting.
AUTO_INSTALL_CRON = "tesseraAutoInstall"
ENROLLMENT_RECONCILE_CRON = "tesseraEnrollmentReconcile"
LICENSE_STATE_EVALUATION_CRON = "licenseStateEvaluation"

_current_artifact: Optional[str] = None


@fixture(scope="module", autouse=True)
def hide_failure_values(request):
    """Keeps locals out of failures, since they hold EA credentials and tokens."""
    option = request.config.option
    saved = option.showlocals, option.tbstyle
    option.showlocals, option.tbstyle = False, "short"
    yield
    option.showlocals, option.tbstyle = saved


def build_license_ops_manager(
    namespace: str,
    name: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    configuration: Optional[dict] = None,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_license.yaml"), namespace=namespace, name=name)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["license"] = None
    resource["spec"]["configuration"].update(configuration or {})
    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    return build_license_ops_manager(namespace, OM_NAME, custom_version, custom_appdb_version)


def current_artifact() -> str:
    assert _current_artifact is not None, "no license minted yet"
    return _current_artifact


def current_artifact_id() -> str:
    return artifact_id_of(current_artifact())


def receipt_secret_name(artifact_id: str) -> str:
    return f"{artifact_id}{RECEIPT_SECRET_SUFFIX}"


def current_receipt_secret_name() -> str:
    return receipt_secret_name(current_artifact_id())


def gsa_credentials(namespace: str) -> GsaCredentials:
    data = read_secret(namespace, GSA_SECRET_NAME)
    return GsaCredentials(client_id=data["GSA_CLIENT_ID"], client_secret=data["GSA_CLIENT_SECRET"])


def om_holder_id(ops_manager: MongoDBOpsManager) -> str:
    return ops_manager.get_om_tester().api_read_license_identity()["holderId"]


def _mint(ops_manager: MongoDBOpsManager, **kwargs) -> tuple[str, str]:
    """Mints a license for this run, bound to this Ops Manager."""
    return mint_license(
        gsa_credentials(ops_manager.namespace),
        principal_id=RUN_PRINCIPAL_ID,
        holder_ids=[om_holder_id(ops_manager)],
        **kwargs,
    )


def mint_run_license(
    ops_manager: MongoDBOpsManager,
    *,
    expiry: Optional[datetime] = None,
    grant_start: Optional[datetime] = None,
) -> str:
    """Mints a license and makes it the current artifact."""
    global _current_artifact
    _current_artifact, artifact_id = _mint(ops_manager, expiry=expiry, grant_start=grant_start)
    return artifact_id


def ea_artifact_row(ops_manager: MongoDBOpsManager, artifact_id: str) -> dict:
    return read_artifact_inventory(gsa_credentials(ops_manager.namespace), RUN_PRINCIPAL_ID).get(artifact_id) or {}


# CR status


def om_condition(ops_manager: MongoDBOpsManager, condition_type: str) -> Optional[dict]:
    for condition in ops_manager["status"]["opsManager"]["conditions"]:
        if condition["type"] == condition_type:
            return condition
    return None


def generation_of(ops_manager: MongoDBOpsManager) -> int:
    ops_manager.load()
    return ops_manager.get_generation()


def license_ready_condition(ops_manager: MongoDBOpsManager) -> dict:
    condition = om_condition(ops_manager, "LicenseReady")
    assert condition is not None, "the CR has no LicenseReady condition"
    return condition


def installed_artifact_id(ops_manager: MongoDBOpsManager) -> str:
    """The license from secretRef that LicenseReconciled reports Ops Manager holds, empty otherwise."""
    ops_manager.load()
    condition = om_condition(ops_manager, "LicenseReconciled")
    if condition is None or condition["reason"] != "LicenseInstalled":
        return ""
    match = re.search(r"holds license (\S+) from secretRef", condition["message"])
    return match.group(1) if match else ""


def _reconciled_as(ops_manager: MongoDBOpsManager, generation: int, installed: Optional[str]) -> bool:
    """Whether LicenseReconciled settled this generation as expected. An empty installed expects no
    license from secretRef, an artifact id expects Ops Manager to hold it, and None skips the check."""
    condition = _reconciled_condition(ops_manager, generation)
    if condition is None:
        return False
    if installed is None:
        return True
    if installed == "":
        return condition["reason"] == "LicenseReconciled"
    return condition["reason"] == "LicenseInstalled" and installed in condition["message"]


def _reconciled_condition(ops_manager: MongoDBOpsManager, generation: int, want: str = "True") -> Optional[dict]:
    """The LicenseReconciled condition, or None while it still describes an older generation.

    Fails at once when the operator settled this generation with a status other than want.
    """
    condition = om_condition(ops_manager, "LicenseReconciled")
    if condition is None or condition["observedGeneration"] < generation:
        return None
    if condition["status"] != want:
        pytest.fail(f"LicenseReconciled is {condition['status']}, expected {want}: {condition['message']}")
    return condition


# Ops Manager view


def read_om_license_view(ops_manager: MongoDBOpsManager) -> dict:
    return ops_manager.get_om_tester().api_read_license_state()


def _held_artifact_ids(view: dict) -> list[str]:
    """Every listed artifact that is not released. A DENIED row counts as held, as in the operator."""
    return [entry["artifactId"] for entry in view.get("licenses", []) if entry["status"] != RELEASED_STATUS]


def om_artifact_ids(ops_manager: MongoDBOpsManager) -> list[str]:
    return _held_artifact_ids(read_om_license_view(ops_manager))


def om_artifact_released(ops_manager: MongoDBOpsManager, artifact_id: str) -> bool:
    for entry in read_om_license_view(ops_manager)["licenses"]:
        if entry["artifactId"] == artifact_id:
            return entry["status"] == RELEASED_STATUS
    raise AssertionError(f"Ops Manager lists no row for license {artifact_id}")


def _omitempty(value):
    """Drops the empty values Go omitempty leaves out of the CR, so both sides compare equal."""
    if isinstance(value, dict):
        kept = {key: _omitempty(item) for key, item in value.items()}
        return {key: item for key, item in kept.items() if item not in (None, False, 0, "", [], {})}
    if isinstance(value, list):
        return [_omitempty(item) for item in value]
    return value


def _mirrored(view: dict) -> dict:
    """The fields that tell whether the Ops Manager license view changed."""
    return _omitempty(
        {
            "enforcement": view.get("enforcement"),
            "licenses": sorted(view.get("licenses", []), key=lambda entry: entry["artifactId"]),
            "activeEntitlements": view.get("activeEntitlements"),
        }
    )


def _held_artifact(view: dict) -> Optional[dict]:
    """The first listed artifact that is not released, as the operator picks it for LicenseReady."""
    for entry in view.get("licenses") or []:
        if entry.get("artifactId") and entry.get("status") != RELEASED_STATUS:
            return entry
    return None


def ready_reason_for(effective_state: str) -> str:
    """The LicenseReady reason the operator sets for a license state, like LicenseGrace for GRACE."""
    if effective_state == "LICENSED":
        return "LicenseReady"
    if effective_state == "":
        return "LicenseStateUnknown"
    return "License" + "".join(word.capitalize() for word in effective_state.lower().split("_") if word)


def expected_ready_condition(om_view: dict, holder_id: str) -> tuple[str, str, str]:
    """The status, reason and message LicenseReady carries for an Ops Manager license view."""
    effective_state = (om_view.get("enforcement") or {}).get("effectiveState", "")
    held = _held_artifact(om_view)
    license = "no license"
    if held is not None:
        license = f"license {held['artifactId']}"
        if held.get("termEnd"):
            license += f", valid until {held['termEnd']}"
    state_message = f"License state is {effective_state}."
    if effective_state == "":
        state_message = "Ops Manager has not reported a license state yet."
    message = f"{state_message} Ops Manager holds {license}."
    if holder_id:
        message += f" Holder ID {holder_id}."
    status = "True" if effective_state == "LICENSED" else "False"
    return status, ready_reason_for(effective_state), message


def cr_matches_om_license(ops_manager: MongoDBOpsManager, om_view: dict) -> bool:
    """Whether LicenseReady reports the Ops Manager license view for the current generation."""
    condition = om_condition(ops_manager, "LicenseReady")
    if condition is None or condition["observedGeneration"] < ops_manager.get_generation():
        return False
    expected = expected_ready_condition(om_view, om_holder_id(ops_manager))
    return (condition["status"], condition["reason"], condition["message"]) == expected


# Assertions


def assert_license_installed(ops_manager: MongoDBOpsManager, artifact_id: str) -> None:
    assert installed_artifact_id(ops_manager) == artifact_id
    ready = license_ready_condition(ops_manager)
    assert ready["status"] == "True", ready["message"]
    assert ready["reason"] == "LicenseReady", ready["message"]


def assert_reconcile_failed(condition: dict, reason: str, *fragments: str) -> None:
    assert condition["reason"] == reason, condition["message"]
    for fragment in fragments:
        assert fragment in condition["message"], condition["message"]


def assert_om_license_unchanged(before: dict, after: dict) -> None:
    assert _mirrored(after) == _mirrored(before)


def assert_secret_absent(namespace: str, name: str) -> None:
    with pytest.raises(ApiException) as exc_info:
        read_secret(namespace, name)
    assert exc_info.value.status == 404


def assert_om_and_appdb_running(ops_manager: MongoDBOpsManager) -> None:
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=60)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=60)


# Secrets


def ensure_license_artifact_secret(namespace: str, artifact: str, name: str = LICENSE_SECRET_NAME) -> None:
    create_or_update_secret(namespace=namespace, name=name, data={LICENSE_SECRET_KEY: artifact})


def delete_secret_if_exists(namespace: str, name: str) -> None:
    try:
        delete_secret(namespace, name)
    except ApiException as exc:
        if exc.status != 404:
            raise


def delete_receipt_secrets(namespace: str) -> None:
    for secret in k8s_client.CoreV1Api().list_namespaced_secret(namespace).items:
        if secret.metadata.name.endswith(RECEIPT_SECRET_SUFFIX):
            delete_secret_if_exists(namespace, secret.metadata.name)


def tampered_receipt(receipt: ReleaseReceipt) -> ReleaseReceipt:
    """The receipt with one bit of its attestation signature flipped."""
    document = receipt.document()
    signature = bytearray(base64.b64decode(document["attestation_signature"]))
    signature[0] ^= 1
    document["attestation_signature"] = base64.b64encode(bytes(signature)).decode()
    return ReleaseReceipt(json.dumps(document))


def same_release_receipt(first: ReleaseReceipt, second: ReleaseReceipt) -> bool:
    """Compares receipts by artifact only, since Ops Manager signs the attestation again on every read."""
    return first.document()["artifact"] == second.document()["artifact"] and bool(
        second.document()["attestation_signature"]
    )


# CR spec


def set_license_spec(ops_manager: MongoDBOpsManager, license_spec: Optional[dict]) -> None:
    ops_manager.load()
    ops_manager["spec"]["license"] = license_spec
    ops_manager.update()


def _patch_license_spec(ops_manager: MongoDBOpsManager, **fields) -> None:
    ops_manager.load()
    ops_manager["spec"]["license"] = {**(ops_manager["spec"].get("license") or {}), **fields}
    ops_manager.update()


def set_observe_only_license_spec(ops_manager: MongoDBOpsManager) -> None:
    set_license_spec(ops_manager, None)


def set_license_install_spec(
    ops_manager: MongoDBOpsManager, auto_release_receipts: bool = False, secret_name: str = LICENSE_SECRET_NAME
) -> None:
    license_spec = {"secretRef": {"name": secret_name}}
    if auto_release_receipts:
        license_spec["releaseReceiptsPolicy"] = "Automatic"
    set_license_spec(ops_manager, license_spec)


def set_license_release_spec(ops_manager: MongoDBOpsManager) -> None:
    _patch_license_spec(ops_manager, secretRef=None)


def set_release_receipts_spec(ops_manager: MongoDBOpsManager, secret_name: str = LICENSE_SECRET_NAME) -> None:
    """Requests the receipt of the license held in the Secret, which the operator reads the artifact id
    from. The Secret cannot also be the install secretRef, so the install intent is removed."""
    _patch_license_spec(ops_manager, secretRef=None, releaseReceipts=[{"secretRef": {"name": secret_name}}])


def nudge_reconcile(ops_manager: MongoDBOpsManager) -> None:
    ops_manager.load()
    ops_manager["metadata"].setdefault("annotations", {})["reconcile"] = str(int(time.time()))
    ops_manager.update()


def set_annotation_and_wait_cleared(ops_manager: MongoDBOpsManager, annotation: str) -> None:
    """Sets a one shot annotation and waits for the operator to remove it."""
    ops_manager.load()
    ops_manager["metadata"].setdefault("annotations", {})[annotation] = "true"
    ops_manager.update()

    ops_manager.wait_for(
        lambda om: annotation not in om["metadata"].get("annotations", {}),
        timeout=DEFAULT_TIMEOUT,
    )


# Ops Manager admin API


def api_install_license(ops_manager: MongoDBOpsManager) -> dict:
    return ops_manager.get_om_tester().api_install_license(json.loads(current_artifact()))


def api_release_all_licenses(ops_manager: MongoDBOpsManager) -> None:
    for artifact_id in om_artifact_ids(ops_manager):
        try:
            ops_manager.get_om_tester().api_release_license(artifact_id)
        except requests.HTTPError as exc:
            # Ops Manager drops a license revoked at EA, it can go between the read and the release.
            if exc.response is None or exc.response.status_code != 404:
                raise


def om_artifact_usable(ops_manager: MongoDBOpsManager, artifact_id: str) -> bool:
    for entry in read_om_license_view(ops_manager).get("licenses", []):
        if entry["artifactId"] == artifact_id:
            return bool(entry.get("usable"))
    # A revoked artifact can drop out of the Ops Manager view, it is unusable then too.
    return False


def ea_holder_states(ops_manager: MongoDBOpsManager, artifact_id: str) -> dict[str, str]:
    """Every holder EA lists on the artifact, with its state."""
    return {
        holder["holder_id"]: holder["status"] for holder in ea_artifact_row(ops_manager, artifact_id).get("holders", [])
    }


def _cron(ops_manager: MongoDBOpsManager, action: str, cron_name: str) -> None:
    admin = read_secret(ops_manager.namespace, ops_manager.get_admin_secret_name())
    ops_manager.get_om_tester().ui_cron(admin["Username"], admin["Password"], action, cron_name)


def trigger_cron(ops_manager: MongoDBOpsManager, cron_name: str) -> None:
    _cron(ops_manager, "trigger", cron_name)


@contextmanager
def paused_cron(ops_manager: MongoDBOpsManager, cron_name: str):
    """Pauses an Ops Manager cron for the block. The pause persists in Ops Manager, so it is always resumed."""
    _cron(ops_manager, "manualDisable", cron_name)
    try:
        yield
    finally:
        _cron(ops_manager, "manualEnable", cron_name)


def revoke_at_ea(ops_manager: MongoDBOpsManager, artifact_id: str) -> str:
    """Revokes the artifact at EA and returns the status EA answers with."""
    return revoke_license(gsa_credentials(ops_manager.namespace), artifact_id, "mck-ci revoke")["status"]


def wait_with_cron(ops_manager: MongoDBOpsManager, cron_name: str, condition) -> None:
    """Triggers the cron, then again about once a minute until the condition holds, as EA can lag the
    first trigger. Matches the Ops Manager EA licensing suite."""
    polls = 0

    def met(_: MongoDBOpsManager) -> bool:
        nonlocal polls
        if condition():
            return True
        if polls % CRON_RETRIGGER_POLLS == 0:
            try:
                trigger_cron(ops_manager, cron_name)
            except requests.RequestException:
                pass
        polls += 1
        return False

    ops_manager.wait_for(met, timeout=CRON_TIMEOUT)


def wait_for_ea_holder_released(ops_manager: MongoDBOpsManager, artifact_id: str, holder_id: str) -> None:
    """Waits until EA reports the holder released the artifact, triggering the enrollment cron."""

    def released() -> bool:
        return ea_holder_states(ops_manager, artifact_id).get(holder_id) == "RELEASED"

    wait_with_cron(ops_manager, ENROLLMENT_RECONCILE_CRON, released)


def wait_for_om_artifact_unusable(ops_manager: MongoDBOpsManager, artifact_id: str) -> None:
    """Waits until Ops Manager stops using the artifact, triggering the enrollment cron."""

    def unusable() -> bool:
        return not om_artifact_usable(ops_manager, artifact_id)

    wait_with_cron(ops_manager, ENROLLMENT_RECONCILE_CRON, unusable)


# Scenarios


def reset_license_e2e_state(ops_manager: MongoDBOpsManager) -> int:
    """Releases everything, returns the CR to observe only, and returns the settled generation."""
    api_release_all_licenses(ops_manager)
    set_observe_only_license_spec(ops_manager)
    try:
        wait_for_cr_license_sync(ops_manager, installed="")
    finally:
        delete_receipt_secrets(ops_manager.namespace)
    return generation_of(ops_manager)


def prepare_for_license_install(ops_manager: MongoDBOpsManager) -> None:
    api_release_all_licenses(ops_manager)
    mint_run_license(ops_manager)


def install_license(ops_manager: MongoDBOpsManager, namespace: str, *, auto_release_receipts: bool = False) -> str:
    """Mints a fresh license and installs it through the CR."""
    prepare_for_license_install(ops_manager)
    ensure_license_artifact_secret(namespace, current_artifact())
    set_license_install_spec(ops_manager, auto_release_receipts=auto_release_receipts)
    wait_for_installed_license(ops_manager, current_artifact_id())
    wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "ACTIVE")
    return current_artifact_id()


def release_license(ops_manager: MongoDBOpsManager, *, request_receipt: bool = False) -> None:
    if request_receipt:
        set_release_receipts_spec(ops_manager)
    else:
        set_license_release_spec(ops_manager)
    wait_for_released_artifact(ops_manager, current_artifact_id())
    wait_for_cr_license_sync(ops_manager, installed="")
    wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "RELEASED")
    wait_for_ea_artifact_status(ops_manager, current_artifact_id(), "RELEASED")


def install_and_release_license(
    ops_manager: MongoDBOpsManager, namespace: str, *, auto_release_receipts: bool = False
) -> None:
    install_license(ops_manager, namespace, auto_release_receipts=auto_release_receipts)
    release_license(ops_manager)


def install_license_out_of_band(ops_manager: MongoDBOpsManager) -> int:
    """Mints a fresh license, installs it through the admin API with the CR in observe only, and
    returns the CR generation. Ops Manager rejects an artifact older than one it already saw."""
    generation = reset_license_e2e_state(ops_manager)
    mint_run_license(ops_manager)
    api_install_license(ops_manager)
    wait_for_observed_api_state(
        ops_manager, effective_state="LICENSED", artifact_id=current_artifact_id(), installed=""
    )
    wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "ACTIVE")
    return generation


def renew_license(ops_manager: MongoDBOpsManager, namespace: str) -> str:
    """Swaps the Secret for a license that ends later than the installed one."""
    raw, artifact_id = _mint(ops_manager, expiry=datetime.now(timezone.utc) + timedelta(days=2 * DEFAULT_VALIDITY_DAYS))
    ensure_license_artifact_secret(namespace, raw)
    wait_for_installed_license(ops_manager, artifact_id)
    return artifact_id


def install_expired_license(ops_manager: MongoDBOpsManager) -> None:
    """Installs a license that ended a year ago.

    It has to end before Ops Manager's 90 day grace window, or the state stays GRACE and is not enforced.
    """
    now = datetime.now(timezone.utc)
    mint_run_license(ops_manager, expiry=now - timedelta(days=365), grant_start=now - timedelta(days=730))
    api_install_license(ops_manager)
    ops_manager.get_om_tester().api_recompute_license()


def issue_top_up_and_auto_install(ops_manager: MongoDBOpsManager) -> str:
    """Mints a top up for this Ops Manager at EA and lets the auto install cron pull it in, so it reaches
    Ops Manager without the CR or the admin API."""
    _, artifact_id = _mint(ops_manager, expiry=datetime.now(timezone.utc) + timedelta(days=2 * DEFAULT_VALIDITY_DAYS))

    wait_with_cron(ops_manager, AUTO_INSTALL_CRON, lambda: artifact_id in om_artifact_ids(ops_manager))
    wait_for_ea_artifact_state(ops_manager, artifact_id, "ACTIVE")
    return artifact_id


# Waits


def wait_for_license_reconciled(ops_manager: MongoDBOpsManager, *, expected: bool) -> dict:
    want = "True" if expected else "False"
    generation = generation_of(ops_manager)
    ops_manager.wait_for(
        lambda _: _reconciled_condition(ops_manager, generation, want) is not None, timeout=DEFAULT_TIMEOUT
    )
    condition = _reconciled_condition(ops_manager, generation, want)
    assert condition is not None
    return condition


def wait_for_license_reconciled_reason(ops_manager: MongoDBOpsManager, reason: str) -> dict:
    """Waits for LicenseReconciled to report the reason after a change that keeps the generation, like
    a Secret update."""

    def reported(_: MongoDBOpsManager) -> bool:
        condition = om_condition(ops_manager, "LicenseReconciled")
        if condition is None or condition["reason"] != reason:
            nudge_reconcile(ops_manager)
            return False
        return True

    ops_manager.wait_for(reported, timeout=DEFAULT_TIMEOUT)
    condition = om_condition(ops_manager, "LicenseReconciled")
    assert condition is not None
    return condition


def assert_license_reconciled_reason_stays(ops_manager: MongoDBOpsManager, reason: str, polls: int = 10) -> None:
    """Triggers reconciles and checks LicenseReconciled keeps the reason across them."""
    for _ in range(polls):
        nudge_reconcile(ops_manager)
        time.sleep(3)
        ops_manager.load()
        condition = om_condition(ops_manager, "LicenseReconciled")
        assert condition is not None and condition["reason"] == reason, condition


def remove_install_intent_without_identity(ops_manager: MongoDBOpsManager, artifact_id: str) -> dict:
    """Removes secretRef once the license Secret no longer identifies the installed license, and checks
    the operator refuses to release a license it cannot identify and keeps refusing."""
    set_license_release_spec(ops_manager)
    reconciled = wait_for_license_reconciled_reason(ops_manager, "LicenseReleaseFailed")
    assert reconciled["status"] == "False", reconciled["message"]
    assert "cannot release the license Ops Manager holds" in reconciled["message"], reconciled["message"]
    assert_license_reconciled_reason_stays(ops_manager, "LicenseReleaseFailed")
    assert artifact_id in om_artifact_ids(ops_manager)
    return reconciled


def wait_for_license_ready_matches_om(ops_manager: MongoDBOpsManager) -> dict:
    """Waits until LicenseReady reports the Ops Manager view, whatever LicenseReconciled says, and
    returns the view."""

    def matches(_: MongoDBOpsManager) -> bool:
        if cr_matches_om_license(ops_manager, read_om_license_view(ops_manager)):
            return True
        nudge_reconcile(ops_manager)
        return False

    ops_manager.wait_for(matches, timeout=DEFAULT_TIMEOUT)
    return read_om_license_view(ops_manager)


def wait_for_cr_license_sync(ops_manager: MongoDBOpsManager, *, installed: Optional[str] = None) -> tuple[dict, dict]:
    """Waits until the current generation is reconciled and LicenseReady reports the Ops Manager view.

    installed is the license from secretRef LicenseReconciled must report Ops Manager holds, empty for
    none. None skips the check. Returns the LicenseReady condition and the Ops Manager view.
    """
    generation = generation_of(ops_manager)

    def synced(_: MongoDBOpsManager) -> bool:
        om_view = read_om_license_view(ops_manager)
        in_sync = _reconciled_as(ops_manager, generation, installed) and cr_matches_om_license(ops_manager, om_view)
        if not in_sync:
            logger.info(
                f"License conditions not in sync yet, CR: {ops_manager['status']['opsManager'].get('conditions')}, "
                f"Ops Manager: {om_view}"
            )
            nudge_reconcile(ops_manager)
        return in_sync

    ops_manager.wait_for(synced, timeout=DEFAULT_TIMEOUT)
    return license_ready_condition(ops_manager), read_om_license_view(ops_manager)


def wait_for_observed_api_state(
    ops_manager: MongoDBOpsManager,
    *,
    effective_state: Optional[str] = None,
    enforced: Optional[bool] = None,
    artifact_id: Optional[str] = None,
    installed: Optional[str] = None,
) -> tuple[dict, dict]:
    """Waits until Ops Manager reports the expected state, then until the CR reports it."""

    def matches(_: MongoDBOpsManager) -> bool:
        view = read_om_license_view(ops_manager)
        enforcement = view["enforcement"]
        if effective_state is not None and enforcement["effectiveState"] != effective_state:
            return False
        if enforced is not None and enforcement.get("enforced", False) != enforced:
            return False
        return artifact_id is None or artifact_id in _held_artifact_ids(view)

    ops_manager.wait_for(matches, timeout=DEFAULT_TIMEOUT)
    return wait_for_cr_license_sync(ops_manager, installed=installed)


def wait_for_installed_license(ops_manager: MongoDBOpsManager, artifact_id: str) -> None:
    wait_for_observed_api_state(ops_manager, effective_state="LICENSED", artifact_id=artifact_id, installed=artifact_id)


def wait_for_released_artifact(ops_manager: MongoDBOpsManager, artifact_id: str) -> None:
    ops_manager.wait_for(lambda _: om_artifact_released(ops_manager, artifact_id), timeout=DEFAULT_TIMEOUT)


def wait_for_license_receipt(ops_manager: MongoDBOpsManager, secret_name: str) -> ReleaseReceipt:
    generation = generation_of(ops_manager)

    def written(_: MongoDBOpsManager) -> bool:
        _reconciled_condition(ops_manager, generation)
        try:
            return RECEIPT_SECRET_KEY in read_secret(ops_manager.namespace, secret_name)
        except ApiException as exc:
            if exc.status != 404:
                raise
            return False

    ops_manager.wait_for(written, timeout=DEFAULT_TIMEOUT)
    return ReleaseReceipt(read_secret(ops_manager.namespace, secret_name)[RECEIPT_SECRET_KEY])


def wait_for_ea_artifact_state(ops_manager: MongoDBOpsManager, artifact_id: str, expected: str) -> None:
    """Waits until EA reports this Ops Manager as a holder of the artifact in the expected state.

    Ops Manager reports the same local state even when it cannot reach EA, so this is the only
    check that the claim or release reached the control plane.
    """
    credentials = gsa_credentials(ops_manager.namespace)
    holder_id = om_holder_id(ops_manager)

    def holder_is() -> bool:
        row = read_artifact_inventory(credentials, RUN_PRINCIPAL_ID).get(artifact_id)
        state = holder_state(row, holder_id) if row else "absent"
        logger.info(f"EA reports holder {holder_id} on license {artifact_id} as {state}, waiting for {expected}")
        return state == expected

    wait_with_cron(ops_manager, ENROLLMENT_RECONCILE_CRON, holder_is)


def wait_for_ea_artifact_status(ops_manager: MongoDBOpsManager, artifact_id: str, expected: str) -> None:
    """Waits for the artifact's own status at EA, which leaves ISSUED once it is retired or revoked."""
    ops_manager.wait_for(
        lambda _: ea_artifact_row(ops_manager, artifact_id).get("status") == expected, timeout=DEFAULT_TIMEOUT
    )
