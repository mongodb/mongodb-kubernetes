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
    password_hash = hashlib.md5(
        f"{username}:mongo:{password}".encode()
    ).hexdigest()  # nosec B324  # codeql[py/weak-cryptographic-algorithm] - MD5 is mandated by the MongoDB SCRAM-SHA-1 protocol spec (RFC 5802), not used for general password hashing
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
    mechanisms: Optional[list],
    sha256_creds: Optional[dict] = None,
    sha1_creds: Optional[dict] = None,
) -> None:
    ac = om_tester.api_get_automation_config()
    ac["auth"].setdefault("usersWanted", [])
    ac["auth"]["usersWanted"] = [
        u for u in ac["auth"]["usersWanted"] if not (u.get("user") == username and u.get("db") == db)
    ]
    entry = {"user": username, "db": db, "roles": roles}
    if mechanisms is not None:
        entry["mechanisms"] = mechanisms
    if sha256_creds:
        entry["scramSha256Creds"] = sha256_creds
    if sha1_creds:
        entry["scramSha1Creds"] = sha1_creds
    ac["auth"]["usersWanted"].append(entry)
    om_tester.api_put_automation_config(ac)


def build_scram_user_resource(namespace: str, username: str, password: str, secret_name: str, mdb_resource_name: str):
    """Creates the password secret and returns a MongoDBUser resource for the given user."""
    from kubetester import create_or_update_secret, find_fixture, try_load
    from kubetester.mongodb_user import MongoDBUser

    create_or_update_secret(namespace, secret_name, {"password": password})
    resource = MongoDBUser.from_yaml(find_fixture("scram-sha-user.yaml"), namespace=namespace, name=username)
    resource["spec"]["username"] = username
    resource["spec"]["passwordSecretKeyRef"] = {"name": secret_name, "key": "password"}
    resource["spec"]["mongodbResourceRef"]["name"] = mdb_resource_name
    try_load(resource)
    return resource


def get_ac_user(ac_tester, username: str) -> dict:
    users = ac_tester.automation_config["auth"]["usersWanted"]
    matches = [u for u in users if u["user"] == username]
    assert matches, f"User {username!r} not found in usersWanted"
    return matches[0]


def assert_user_mechanisms(ac_tester, username: str, expected: List[str]) -> None:
    user = get_ac_user(ac_tester, username)
    assert (
        user.get("mechanisms", []) == expected
    ), f"User {username!r} mechanisms: expected {expected}, got {user.get('mechanisms', [])}"


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
