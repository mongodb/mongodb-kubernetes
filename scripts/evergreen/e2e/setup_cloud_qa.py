#!/usr/bin/env python3

from typing import List
import random
import re
import os
import sys
import time

import requests
from requests.auth import HTTPDigestAuth


allowed_ops_manager_version = "cloud_qa"
base_url = os.getenv("e2e_cloud_qa_baseurl")
org = os.getenv("e2e_cloud_qa_orgid_owner")


def _get_auth(api_key, user):
    """Builds a HTTPDigestAuth from user and api_key"""
    return HTTPDigestAuth(user, api_key)


def get_auth(type="org_owner"):
    """Builds an Authentication object depending on the type required."""
    if type == "org_owner":
        api_key = os.getenv("e2e_cloud_qa_apikey_owner")
        user = os.getenv("e2e_cloud_qa_user_owner")
        return _get_auth(api_key, user)
    elif type == "project_owner":
        env = read_env_file()
        return _get_auth(env["OM_API_KEY"], env["OM_USER"])


def create_api_key(
    org: str, description: str, roles: List[str] = ["ORG_GROUP_CREATOR"]
):
    """Creates an Organization level API Key object."""
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
    url = "{}/api/public/v1.0/groups".format(base_url)
    data = {"orgId": org, "name": name}
    response = requests.post(url, auth=auth, json=data)

    print(response.text)


def delete_api_key(org: str, key_id: str):
    """Deletes an Organization level API Key object."""
    url = "{}/api/public/v1.0/orgs/{}/apiKeys/{}".format(base_url, org, key_id)
    response = requests.delete(url, auth=get_auth())
    if response.status_code != 204:
        raise Exception("Could not remove the Programmatic API Key", response.text)

    return response


def whitelist_key(
    org: str, key_id: str, whitelist: List[str] = ["0.0.0.0/1", "128.0.0.0/1"]
):
    """Whitelists an Organization level API Key object."""
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


def remove_group_by_id(id: str, retry=3):
    """Removes a group with a given Id."""
    url = "{}/api/public/v1.0/groups/{}".format(base_url, id)
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
    namespace_file = os.getenv("NAMESPACE_FILE")
    with open(namespace_file) as fd:
        return fd.read().strip()


def get_key_value_from_line(line: str):
    """Returns a key, value from a line with the format 'export key=value"""
    matcher = re.compile(r"^export\s+([A-Z_]+)\s*=\s*(\S+)$")
    match = matcher.match(line)

    return match.group(1), match.group(2)


def read_env_file():
    """Returns the env file (in ENV_FILE env variable) as a key=value dict."""
    data = {}

    env_file = os.getenv("ENV_FILE")
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
    response = create_api_key(org, "Testing: {}".format(task_name))

    # we will use key_id to remove this key
    key_id = response["id"]
    whitelist_key(org, key_id)

    public = response["publicKey"]
    private = response["privateKey"]

    env_file = os.getenv("ENV_FILE")
    with open(env_file, "w") as fd:
        fd.write("export OM_BASE_URL={}\n".format(base_url))
        fd.write("export OM_USER={}\n".format(public))
        fd.write("export OM_API_KEY={}\n".format(private))
        fd.write("export OM_ORGID={}\n".format(org))
        fd.write("export OM_KEY_ID={}\n".format(key_id))
        fd.write("export OM_EXTERNALLY_CONFIGURED=true\n")


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
    try:
        delete_api_key(org, key_id)
    except Exception as e:
        print("Got an exception trying to remove Api Key", e)


def main():
    om_version = os.getenv("ops_manager_version")
    if om_version is None or om_version != allowed_ops_manager_version:
        # Should not run if not using Cloud-QA
        sys.exit(0)

    if sys.argv[1] == "delete":
        print("Removing project and api key from Cloud QA")
        unconfigure()
    else:
        print("Configuring Cloud QA")
        configure()


if __name__ == "__main__":
    main()
