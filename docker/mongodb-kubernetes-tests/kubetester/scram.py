import base64
import hashlib
import hmac
import os
from typing import List, Optional

_SCRAM_SHA256_ITERATIONS = 15000
_SCRAM_SHA256_SALT_SIZE = 28
_SCRAM_SHA1_ITERATIONS = 10000
_SCRAM_SHA1_SALT_SIZE = 16


def build_sha256_creds(password: str) -> dict:
    salt = os.urandom(_SCRAM_SHA256_SALT_SIZE)
    salted_password = hashlib.pbkdf2_hmac("sha256", password.encode("utf-8"), salt, _SCRAM_SHA256_ITERATIONS)
    client_key = hmac.new(salted_password, b"Client Key", hashlib.sha256).digest()
    stored_key = hashlib.sha256(client_key).digest()
    server_key = hmac.new(salted_password, b"Server Key", hashlib.sha256).digest()
    return {
        "iterationCount": _SCRAM_SHA256_ITERATIONS,
        "salt": base64.b64encode(salt).decode("utf-8"),
        "storedKey": base64.b64encode(stored_key).decode("utf-8"),
        "serverKey": base64.b64encode(server_key).decode("utf-8"),
    }


def build_sha1_creds(username: str, password: str) -> dict:
    password_hash = hashlib.md5(f"{username}:mongo:{password}".encode()).hexdigest()
    salt = os.urandom(_SCRAM_SHA1_SALT_SIZE)
    salted_password = hashlib.pbkdf2_hmac("sha1", password_hash.encode("utf-8"), salt, _SCRAM_SHA1_ITERATIONS)
    client_key = hmac.new(salted_password, b"Client Key", hashlib.sha1).digest()
    stored_key = hashlib.sha1(client_key).digest()
    server_key = hmac.new(salted_password, b"Server Key", hashlib.sha1).digest()
    return {
        "iterationCount": _SCRAM_SHA1_ITERATIONS,
        "salt": base64.b64encode(salt).decode("utf-8"),
        "storedKey": base64.b64encode(stored_key).decode("utf-8"),
        "serverKey": base64.b64encode(server_key).decode("utf-8"),
    }


def seed_user_in_ac(
    om_tester,
    username: str,
    db: str,
    roles: list,
    mechanisms: list,
    sha256_creds: Optional[dict] = None,
    sha1_creds: Optional[dict] = None,
) -> None:
    ac = om_tester.om_request("get", f"/groups/{om_tester.context.project_id}/automationConfig").json()
    ac["auth"].setdefault("usersWanted", [])
    ac["auth"]["usersWanted"] = [u for u in ac["auth"]["usersWanted"] if u.get("user") != username]
    entry = {"user": username, "db": db, "roles": roles, "mechanisms": mechanisms}
    if sha256_creds:
        entry["scramSha256Creds"] = sha256_creds
    if sha1_creds:
        entry["scramSha1Creds"] = sha1_creds
    ac["auth"]["usersWanted"].append(entry)
    om_tester.om_request("put", f"/groups/{om_tester.context.project_id}/automationConfig", json_object=ac)


def get_ac_user(ac_tester, username: str) -> dict:
    users = ac_tester.automation_config["auth"]["usersWanted"]
    matches = [u for u in users if u["user"] == username]
    assert matches, f"User {username!r} not found in usersWanted"
    return matches[0]


def assert_user_mechanisms(ac_tester, username: str, expected: List[str]) -> None:
    user = get_ac_user(ac_tester, username)
    assert user.get("mechanisms", []) == expected, (
        f"User {username!r} mechanisms: expected {expected}, got {user.get('mechanisms', [])}"
    )


def assert_creds_preserved(
    ac_tester,
    username: str,
    sha256_creds: Optional[dict] = None,
    sha1_creds: Optional[dict] = None,
) -> None:
    user = get_ac_user(ac_tester, username)
    if sha256_creds is not None:
        assert user.get("scramSha256Creds") == sha256_creds, (
            f"User {username!r} SHA-256 creds changed.\n"
            f"  expected: {sha256_creds}\n"
            f"  got:      {user.get('scramSha256Creds')}"
        )
    if sha1_creds is not None:
        assert user.get("scramSha1Creds") == sha1_creds, (
            f"User {username!r} SHA-1 creds changed.\n"
            f"  expected: {sha1_creds}\n"
            f"  got:      {user.get('scramSha1Creds')}"
        )
