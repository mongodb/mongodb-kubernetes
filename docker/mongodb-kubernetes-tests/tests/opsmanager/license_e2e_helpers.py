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
from kubetester import create_or_update_secret, delete_secret, read_secret, run_periodically, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
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

# The license conditions the operator writes on the Ops Manager resource.
LICENSE_READY_CONDITION = "OpsManagerLicenseReady"
LICENSE_RECONCILED_CONDITION = "LicenseReconciled"
LICENSE_STATE_REASONS = {"GRACE": "LicenseNotReady", "UNLICENSED": "LicenseNotReady", "EXPIRED": "LicenseNotReady"}

# The finalizer the operator holds while it may have a license installed in Ops Manager.
LICENSE_FINALIZER = "mongodb.com/v1.opsManagerLicenseRemovalFinalizer"


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


def current_receipt_secret_name() -> str:
    return f"{current_artifact_id()}{RECEIPT_SECRET_SUFFIX}"


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
    condition = om_condition(ops_manager, LICENSE_READY_CONDITION)
    assert condition is not None, "the CR has no LicenseReady condition"
    return condition


def installed_artifact_id(ops_manager: MongoDBOpsManager) -> str:
    """The license from secretRef that LicenseReconciled reports Ops Manager holds, empty otherwise."""
    ops_manager.load()
    condition = om_condition(ops_manager, LICENSE_RECONCILED_CONDITION)
    if condition is None or condition["reason"] != "LicenseInstalled":
        return ""
    match = re.search(r"holds license (\S+) from secretRef", condition["message"])
    return match.group(1) if match else ""


def _reconciled_as(ops_manager: MongoDBOpsManager, generation: int, installed: Optional[str]) -> bool:
    """Whether LicenseReconciled settled this generation as expected. An empty installed expects no
    license from secretRef, right after a release too, an artifact id expects Ops Manager to hold it,
    and None skips the check."""
    condition = _reconciled_condition(ops_manager, generation)
    if condition is None:
        return False
    if installed is None:
        return True
    if installed == "":
        return condition["reason"] == "LicenseNotRequested"
    return condition["reason"] == "LicenseInstalled" and installed in condition["message"]


def _reconciled_condition(ops_manager: MongoDBOpsManager, generation: int) -> Optional[dict]:
    """The LicenseReconciled condition, or None while it still describes an older generation.

    Fails at once when the operator settled this generation with LicenseReconciled False.
    """
    condition = om_condition(ops_manager, LICENSE_RECONCILED_CONDITION)
    if condition is None or condition["observedGeneration"] < generation:
        return None
    if condition["status"] != "True":
        pytest.fail(f"LicenseReconciled is {condition['status']}: {condition['message']}")
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


def expected_ready_condition(om_view: dict, holder_id: str) -> tuple[str, str, str]:
    """The status, reason and message LicenseReady carries for an Ops Manager license view."""
    effective_state = (om_view.get("enforcement") or {}).get("effectiveState", "")
    held = next((entry for entry in om_view.get("licenses", []) if entry["status"] != RELEASED_STATUS), None)
    license = "no license"
    if held is not None:
        license = f"license {held['artifactId']}"
        if held.get("termEnd"):
            license += f", valid until {held['termEnd']}"
    status, reason = "False", LICENSE_STATE_REASONS.get(effective_state, "LicenseStateUnknown")
    state_message = f"License state is {effective_state}."
    if effective_state == "LICENSED":
        status, reason = "True", "LicenseReady"
    if effective_state == "":
        reason, state_message = "LicenseStateUnknown", "Ops Manager has not reported a license state yet."
    message = f"{state_message} Ops Manager holds {license}."
    if holder_id:
        message += f" Holder ID {holder_id}."
    return status, reason, message


def cr_matches_om_license(ops_manager: MongoDBOpsManager, om_view: dict) -> bool:
    """Whether LicenseReady reports the Ops Manager license view for the current generation."""
    condition = om_condition(ops_manager, LICENSE_READY_CONDITION)
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


def assert_om_license_unchanged(before: dict, after: dict) -> None:
    assert after.get("enforcement") == before.get("enforcement")
    assert sorted(after.get("licenses", []), key=lambda entry: entry["artifactId"]) == sorted(
        before.get("licenses", []), key=lambda entry: entry["artifactId"]
    )


def assert_foreign_license_kept(ops_manager: MongoDBOpsManager, artifact_id: str) -> None:
    """Checks the operator left a license it did not install alone: Ops Manager still holds it, EA
    still lists the claim and no receipt was written for it."""
    assert artifact_id in om_artifact_ids(ops_manager)
    assert_secret_absent(ops_manager.namespace, f"{artifact_id}{RECEIPT_SECRET_SUFFIX}")
    wait_for_ea_artifact_state(ops_manager, artifact_id, "ACTIVE")


def assert_secret_absent(namespace: str, name: str) -> None:
    with pytest.raises(ApiException) as exc_info:
        read_secret(namespace, name)
    assert exc_info.value.status == 404


# Secrets


def ensure_license_artifact_secret(namespace: str, artifact: str) -> None:
    create_or_update_secret(namespace=namespace, name=LICENSE_SECRET_NAME, data={LICENSE_SECRET_KEY: artifact})


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


def set_license_install_spec(ops_manager: MongoDBOpsManager, auto_release_receipts: bool = False) -> None:
    license_spec = {"secretRef": {"name": LICENSE_SECRET_NAME}}
    if auto_release_receipts:
        license_spec["releaseReceiptsPolicy"] = "Automatic"
    set_license_spec(ops_manager, license_spec)


def set_license_release_spec(ops_manager: MongoDBOpsManager) -> None:
    _patch_license_spec(ops_manager, secretRef=None)


def set_release_receipts_spec(ops_manager: MongoDBOpsManager) -> None:
    """Requests the receipt of the license held in the license Secret, which the operator reads the
    artifact id from. The Secret cannot also be the install secretRef, so the install intent is removed."""
    _patch_license_spec(ops_manager, secretRef=None, releaseReceipts=[{"secretRef": {"name": LICENSE_SECRET_NAME}}])


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
        holders = ea_artifact_row(ops_manager, artifact_id).get("holders", [])
        return any(holder["holder_id"] == holder_id and holder["status"] == "RELEASED" for holder in holders)

    wait_with_cron(ops_manager, ENROLLMENT_RECONCILE_CRON, released)


def wait_for_om_artifact_unusable(ops_manager: MongoDBOpsManager, artifact_id: str) -> None:
    """Waits until Ops Manager stops using the artifact, triggering the enrollment cron."""

    def unusable() -> bool:
        return not om_artifact_usable(ops_manager, artifact_id)

    wait_with_cron(ops_manager, ENROLLMENT_RECONCILE_CRON, unusable)


# Scenarios


def reset_license_e2e_state(ops_manager: MongoDBOpsManager) -> None:
    """Releases everything and returns the CR to observe only."""
    api_release_all_licenses(ops_manager)
    set_license_spec(ops_manager, None)
    try:
        wait_for_cr_license_sync(ops_manager, installed="")
    finally:
        delete_receipt_secrets(ops_manager.namespace)


def rotate_identity_and_recompute(ops_manager: MongoDBOpsManager) -> None:
    """Runs the rotate identity and recompute annotations and waits for the reconcile after them."""
    set_annotation_and_wait_cleared(ops_manager, ROTATE_IDENTITY_ANNOTATION)
    set_annotation_and_wait_cleared(ops_manager, RECOMPUTE_ANNOTATION)
    wait_for_cr_license_sync(ops_manager, installed="")


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


def install_and_release_license(ops_manager: MongoDBOpsManager, namespace: str) -> None:
    """Installs a fresh license through the CR, then removes secretRef and waits for EA to retire it."""
    install_license(ops_manager, namespace)
    set_license_release_spec(ops_manager)
    wait_for_released_artifact(ops_manager, current_artifact_id())
    wait_for_cr_license_sync(ops_manager, installed="")
    wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "RELEASED")
    wait_for_ea_artifact_status(ops_manager, current_artifact_id(), "RELEASED")


def install_license_out_of_band(ops_manager: MongoDBOpsManager) -> None:
    """Mints a fresh license and installs it through the admin API with the CR in observe only. Ops
    Manager rejects an artifact older than one it already saw."""
    mint_run_license(ops_manager)
    api_install_license(ops_manager)
    wait_for_observed_api_state(
        ops_manager, effective_state="LICENSED", artifact_id=current_artifact_id(), installed=""
    )
    wait_for_ea_artifact_state(ops_manager, current_artifact_id(), "ACTIVE")


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


def wait_for_license_reconciled_reason(ops_manager: MongoDBOpsManager, reason: str) -> dict:
    """Waits for LicenseReconciled to report the reason after a change that keeps the generation, like
    a Secret update."""

    def reported(_: MongoDBOpsManager) -> bool:
        condition = om_condition(ops_manager, LICENSE_RECONCILED_CONDITION)
        if condition is None or condition["reason"] != reason:
            nudge_reconcile(ops_manager)
            return False
        return True

    ops_manager.wait_for(reported, timeout=DEFAULT_TIMEOUT)
    condition = om_condition(ops_manager, LICENSE_RECONCILED_CONDITION)
    assert condition is not None
    return condition


def remove_install_intent_without_identity(ops_manager: MongoDBOpsManager, artifact_id: str) -> dict:
    """Removes secretRef once the license Secret no longer identifies the installed license. The
    operator refuses to release a license it cannot identify and reports it once. The next reconcile
    records the spec without secretRef, so the license stays with Ops Manager and is released from
    its UI. Returns the failed LicenseReconciled condition."""
    set_license_release_spec(ops_manager)
    generation = generation_of(ops_manager)
    failure: dict = {}

    def release_failed(_: MongoDBOpsManager) -> bool:
        # No nudge here: the failure only lasts until the retry of the failed reconcile.
        condition = om_condition(ops_manager, LICENSE_RECONCILED_CONDITION)
        if condition is None or condition["observedGeneration"] < generation:
            return False
        if condition["status"] == "True":
            pytest.fail(f"LicenseReconciled turned True before the release failure was seen: {condition['message']}")
        failure.update(condition)
        return True

    ops_manager.wait_for(release_failed, timeout=DEFAULT_TIMEOUT)
    assert failure["reason"] == "LicenseReconcileFailed", failure["message"]
    assert "cannot release the license Ops Manager holds" in failure["message"], failure["message"]

    # The retry of the failed reconcile records the spec without secretRef and reports it reconciled.
    def reconciled() -> bool:
        condition = om_condition(ops_manager, LICENSE_RECONCILED_CONDITION)
        return condition is not None and condition["status"] == "True"

    ops_manager.wait_for(reconciled, timeout=DEFAULT_TIMEOUT)
    wait_for_cr_license_sync(ops_manager, installed="")
    assert artifact_id in om_artifact_ids(ops_manager)
    return failure


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


def wait_for_receipt_secret(namespace: str, secret_name: str) -> ReleaseReceipt:
    """Waits for a receipt Secret without the resource, which outlives the resource it was written for."""

    def written() -> bool:
        try:
            return RECEIPT_SECRET_KEY in read_secret(namespace, secret_name)
        except ApiException as exc:
            if exc.status != 404:
                raise
            return False

    run_periodically(written, timeout=DEFAULT_TIMEOUT)
    return ReleaseReceipt(read_secret(namespace, secret_name)[RECEIPT_SECRET_KEY])


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
