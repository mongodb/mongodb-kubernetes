#!/usr/bin/env python3
import os
import random
import re
from datetime import datetime
from typing import List, Tuple, Dict

import requests
import sys
import time
from requests.auth import HTTPDigestAuth

ALLOWED_OPS_MANAGER_VERSION = "cloud_qa"

APIKEY_OWNER = "e2e_cloud_qa_apikey_owner"
BASE_URL = "e2e_cloud_qa_baseurl"
ENV_FILE = "ENV_FILE"
NAMESPACE_FILE = "NAMESPACE_FILE"
OPS_MANAGER_VERSION = "ops_manager_version"
ORG_ID = "e2e_cloud_qa_orgid_owner"
USER_OWNER = "e2e_cloud_qa_user_owner"

REQUIRED_ENV_VARIABLES = (
    APIKEY_OWNER,
    BASE_URL,
    ENV_FILE,
    NAMESPACE_FILE,
    OPS_MANAGER_VERSION,
    ORG_ID,
    USER_OWNER,
)


def _get_auth(api_key: str, user: str) -> HTTPDigestAuth:
    """Builds a HTTPDigestAuth from user and api_key"""
    return HTTPDigestAuth(user, api_key)


def get_auth(auth_type: str = "org_owner") -> HTTPDigestAuth:
    """Builds an Authentication object depending on the type required."""
    if auth_type == "org_owner":
        api_key = os.getenv(APIKEY_OWNER)
        assert api_key is not None, "no e2e_cloud_qa_apikey_owner env variable defined"
        user = os.getenv(USER_OWNER)
        assert user is not None, "no e2e_cloud_qa_user_owner env variable defined"
        return _get_auth(api_key, user)
    if auth_type == "project_owner":
        env = read_env_file()
        return _get_auth(env["OM_API_KEY"], env["OM_USER"])
    assert False, "wrong auth_type"


def create_api_key(org: str, description: str, roles: List[str] = None):
    """Creates an Organization level API Key object."""
    if roles is None:
        roles = ["ORG_GROUP_CREATOR"]
    base_url = os.getenv(BASE_URL)
    url = "{}/api/public/v1.0/orgs/{}/apiKeys".format(base_url, org)
    data = {"roles": roles, "desc": description}
    response = requests.post(url, auth=get_auth(), json=data)
    if response.status_code != 200:
        raise Exception("Could not create Programmatic API Key", response.text)

    return response.json()


def create_group(org: str, name: str):
    """Creates a group in an organization.

    note: this is not needed for now, I use it for local testing.
    """
    auth = get_auth("project_owner")
    base_url = os.getenv(BASE_URL)
    url = "{}/api/public/v1.0/groups".format(base_url)
    data = {"orgId": org, "name": name}
    response = requests.post(url, auth=auth, json=data)

    print(response.text)


def delete_api_key(org: str, key_id: str):
    """Deletes an Organization level API Key object."""
    base_url = os.getenv(BASE_URL)
    url = "{}/api/public/v1.0/orgs/{}/apiKeys/{}".format(base_url, org, key_id)
    response = requests.delete(url, auth=get_auth())
    if response.status_code != 204:
        raise Exception("Could not remove the Programmatic API Key", response.text)

    return response


def whitelist_key(org: str, key_id: str, whitelist: List[str] = None):
    """Whitelists an Organization level API Key object."""
    if whitelist is None:
        whitelist = ["0.0.0.0/1", "128.0.0.0/1"]
    base_url = os.getenv(BASE_URL)
    url = "{}/api/public/v1.0/orgs/{}/apiKeys/{}/whitelist".format(
        base_url, org, key_id
    )
    data = [{"cidrBlock": cb} for cb in whitelist]
    response = requests.post(url, auth=get_auth(), json=data)
    if response.status_code != 200:
        raise Exception("Could not add whitelist", response.text)

    return response


def get_group_id_by_name(name: str, retry=3) -> str:
    """Returns the 'id' that corresponds to this Project name."""
    base_url = os.getenv(BASE_URL)
    url = "{}/api/public/v1.0/groups/byName/{}".format(base_url, name)

    while retry > 0:
        groups = requests.get(url, auth=get_auth("project_owner"))

        response = groups.json()
        if "id" not in response:
            print("Id not in the response, this is what we got")
            print(response)
            retry -= 1
            time.sleep(3 + random.random())
            continue

        break

    return groups.json()["id"]


def was_created_before(group_name: str, age: int) -> bool:
    try:
        group_day_of_year = int(
            group_name.split("-")[1]
        )  # a-147-yr3jzt3v7bsltaowzc2lz -> 147
        current_day_of_year = int(datetime.now().strftime("%j"))
    except Exception as e:
        print(e)
        return False
    return group_day_of_year + age <= current_day_of_year


def get_projects_oder_than(org_id: str, age: int = 0) -> List[Dict]:
    """Returns the project ids which are older than 'age' days ago """
    base_url = os.getenv(BASE_URL)
    url = "{}/api/public/v1.0/orgs/{}/groups".format(base_url, org_id)

    groups = requests.get(url, auth=get_auth())

    json = groups.json()

    return [
        group for group in json["results"] if was_created_before(group["name"], age)
    ]


def remove_group_by_id(group_id: str, retry=3):
    """Removes a group with a given Id."""
    base_url = os.getenv(BASE_URL)
    url = "{}/api/public/v1.0/groups/{}".format(base_url, group_id)
    while retry > 0:
        result = requests.delete(url, auth=get_auth("org_owner"))
        print(result)
        if result.status_code != 202:
            retry -= 1
            time.sleep(3 + random.random())
            continue

        break

    return result


def remove_group_by_name(name: str):
    """Removes a group by its name."""
    _id = get_group_id_by_name(name)
    result = remove_group_by_id(_id)

    status = "OK" if result.status_code == 202 else "FAILED"
    print("Removing group id: {} and name: {} -> {}".format(_id, name, status))
    return result


def read_namespace():
    """Reads a testing namespace name from a file."""
    namespace_file = os.getenv(NAMESPACE_FILE)
    with open(namespace_file) as fd:
        return fd.read().strip()


def get_key_value_from_line(line: str) -> Tuple[str, str]:
    """Returns a key, value from a line with the format 'export key=value"""
    matcher = re.compile(r"^export\s+([A-Z_]+)\s*=\s*(\S+)$")
    match = matcher.match(line)
    assert match, "Unrecognised pattern in ENV_FILE"
    return match.group(1), match.group(2)


def read_env_file():
    """Returns the env file (in ENV_FILE env variable) as a key=value dict."""
    data = {}

    env_file = os.getenv(ENV_FILE)
    with open(env_file) as fd:
        for line in fd.readlines():
            try:
                key, value = get_key_value_from_line(line)
            except IndexError:
                pass
            data[key] = value

    return data


def configure():
    """Creates a programmatic API Key, and whitelist it. This function also
    creates a sourceable file with the Cloud QA configuration,
    unfortunatelly, that's the only way of passing data from one command to
    the next.
    """
    task_name = os.getenv("task_name", "Unknown task name")
    org = os.getenv(ORG_ID)
    response = create_api_key(org, "Testing: {}".format(task_name))

    # we will use key_id to remove this key
    key_id = response["id"]
    whitelist_key(org, key_id)

    public = response["publicKey"]
    private = response["privateKey"]

    env_file = os.getenv(ENV_FILE)
    base_url = os.getenv(BASE_URL)
    with open(env_file, "w") as fd:
        fd.write("export OM_BASE_URL={}\n".format(base_url))
        fd.write("export OM_USER={}\n".format(public))
        fd.write("export OM_API_KEY={}\n".format(private))
        fd.write("export OM_ORGID={}\n".format(org))
        fd.write("export OM_KEY_ID={}\n".format(key_id))
        fd.write("export OM_EXTERNALLY_CONFIGURED=true\n")


def clean_unused_projects(org_id: str):
    """ Iterates over all existing projects in the organization and removes the leftovers """
    projects = get_projects_oder_than(org_id, age=3)

    for project in projects:
        print("Removing the project {} ({})".format(project["id"], project["name"]))
        remove_group_by_id(project["id"])


def unconfigure():
    """Tries to remove the project and API Key from Cloud-QA"""
    env = read_env_file()
    namespace = read_namespace()

    # The "group" needs to be removed using the user's API credentials
    if namespace is not None:
        print("Got namespace:", namespace)
        try:
            remove_group_by_name(namespace)
        except Exception as e:
            print("Got an exception trying to remove group", e)

    # The API Key needs to be removed using the Owner's API credentials
    key_id = env["OM_KEY_ID"]
    org = os.getenv(ORG_ID)
    try:
        delete_api_key(org, key_id)
    except Exception as e:
        print("Got an exception trying to remove Api Key", e)

    clean_unused_projects(org)


def argv_error() -> int:
    print("This script can only be called with create or delete")
    return 1


def check_env_variables() -> bool:
    status = True
    for var in REQUIRED_ENV_VARIABLES:
        if not os.getenv(var):
            print("Missing env variable: {}".format(var))
            status = False
    return status


def main() -> int:
    if not check_env_variables():
        print("Please define all required env variables")
        return 1
    om_version = os.getenv(OPS_MANAGER_VERSION)
    if om_version is None or om_version != ALLOWED_OPS_MANAGER_VERSION:
        print(
            "ops_manager_version env variable is not correctly defined: "
            "only '{}' is allowed".format(ALLOWED_OPS_MANAGER_VERSION)
        )
        # Should not run if not using Cloud-QA
        return 1

    if len(sys.argv) < 2:
        return argv_error()
    if sys.argv[1] == "delete":
        print("Removing project and api key from Cloud QA")
        unconfigure()
    elif sys.argv[1] == "create":
        print("Configuring Cloud QA")
        configure()
    else:
        return argv_error()
    return 0


if __name__ == "__main__":
    sys.exit(main())
