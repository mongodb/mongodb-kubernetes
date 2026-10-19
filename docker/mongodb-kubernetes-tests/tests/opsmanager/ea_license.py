"""Mints EA license artifacts per run."""

import base64
import json
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Dict, List, Optional, Tuple, Union

import requests
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

EA_CONTROL_PLANE_URL = "https://cloud-dev.mongodb.com"

# The telemetry exemption suppresses the telemetry requirement only, never enforcement, so the
# enforcement tests still see a verdict. It also gives Ops Manager an active entitlement for the CR
# mirror to match. Do not swap in exemption::enforcement, which would turn every verdict off.
DEFAULT_ENTITLEMENTS = ["exemption::telemetry"]

DEFAULT_VALIDITY_DAYS = 2


def _iso(moment: datetime) -> str:
    """UTC timestamp with a Z suffix."""
    assert moment.tzinfo is not None, f"{moment} has no timezone"
    return moment.astimezone(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


@dataclass(frozen=True)
class GsaCredentials:
    """The EA service account credentials. The repr hides them, since pytest prints locals on failure."""

    client_id: str = field(repr=False)
    client_secret: str = field(repr=False)


def fetch_token(credentials: GsaCredentials) -> str:
    response = requests.post(
        f"{EA_CONTROL_PLANE_URL}/api/oauth/token",
        auth=(credentials.client_id, credentials.client_secret),
        data={"grant_type": "client_credentials"},
        timeout=30,
    )
    response.raise_for_status()
    return response.json()["access_token"]


def mint_license(
    credentials: GsaCredentials,
    principal_id: str,
    holder_ids: List[str],
    expiry: Optional[datetime] = None,
    grant_start: Optional[datetime] = None,
) -> Tuple[str, str]:
    """Returns the artifact JSON and its artifact id.

    Without a principal_id EA mints a customer less artifact, which Ops Manager installs but never claims.
    Every artifact is single_instance, so one Ops Manager at a time can claim it.
    """
    now = datetime.now(timezone.utc)
    body = {
        "type": "license",
        "scope": "single_instance",
        "principal_id": principal_id,
        "external_id": principal_id,
        "holder_ids": holder_ids,
        "spec": {
            "schema_version": "1",
            "grants": [
                {
                    "grant_type": "contract",
                    "grant_id": f"{principal_id}-{uuid.uuid4()}",
                    "grant_start_date": _iso(grant_start or now),
                    "grant_end_date": _iso(expiry or now + timedelta(days=DEFAULT_VALIDITY_DAYS)),
                    "entitlements": [{"entitlement_type": entitlement} for entitlement in DEFAULT_ENTITLEMENTS],
                }
            ],
            "metadata": {"source": "mck-ci"},
        },
    }
    response = requests.post(
        f"{EA_CONTROL_PLANE_URL}/api/private/enterpriseAdvanced/tessera/issue",
        headers={"Authorization": f"Bearer {fetch_token(credentials)}"},
        json=body,
        timeout=30,
    )
    response.raise_for_status()
    artifact = response.json()
    artifact_id = artifact_id_of(artifact)
    logger.info(f"Minted license {artifact_id} for principal {principal_id} from {EA_CONTROL_PLANE_URL}")
    return json.dumps(artifact), artifact_id


def read_artifact_inventory(credentials: GsaCredentials, principal_id: str) -> Dict[str, Dict]:
    """This principal's artifacts in the EA inventory, keyed by artifact id."""
    response = requests.get(
        f"{EA_CONTROL_PLANE_URL}/api/private/enterpriseAdvanced/tessera/artifacts",
        params={"principal_id": principal_id, "limit": 100},
        headers={"Authorization": f"Bearer {fetch_token(credentials)}"},
        timeout=30,
    )
    response.raise_for_status()
    return {row["artifact_id"]: row for row in response.json()["artifacts"]}


def artifact_id_of(raw: Union[str, Dict]) -> str:
    """Reads the artifact id from the signed claims."""
    payload = json.loads(raw)["payload"] if isinstance(raw, str) else raw["payload"]
    claims = json.loads(base64.b64decode(payload + "=" * (-len(payload) % 4)))
    return claims["artifact_id"]
