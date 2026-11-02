import base64
import json

from wt_ctl.domains.registry_auth import decode_ecr_expiry


def ecr_auth(expiration):
    password = base64.b64encode(json.dumps({"payload": "x", "expiration": expiration}).encode()).decode()
    return base64.b64encode(f"AWS:{password}".encode()).decode()


def test_expiry_is_read_from_the_token():
    assert decode_ecr_expiry(ecr_auth(1_800_000_000)) == 1_800_000_000


def test_a_password_that_is_not_a_token_has_no_expiry():
    assert decode_ecr_expiry(base64.b64encode(b"user:plain-password").decode()) is None
