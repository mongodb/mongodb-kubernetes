#!/usr/bin/env python3

import configparser
import json
import os
import sys

import requests
import yaml
from packaging.version import Version


def get_latest_om_versions_from_evergreen_yml():
    # Define a custom constructor to preserve the anchors in the YAML file
    evergreen_file = os.path.join(os.getcwd(), ".evergreen.yml")
    with open(evergreen_file) as f:
        data = yaml.safe_load(f)
    return data["variables"][0], data["variables"][1]


def get_headers():
    """
    Returns an authentication header that can be used when accessing
    the Github API. This is used to access private 10gen repos.
    """

    github_token = os.getenv("GITHUB_TOKEN_READ")
    if github_token is None:
        raise Exception(
            "Missing GITHUB_TOKEN_READ environment variable; see https://wiki.corp.mongodb.com/display/MMS/Pre-Commit+Hook"
        )
    return {
        "Authorization": f"token {github_token}",
    }


def update_release_json():
    # Define a custom constructor to preserve the anchors in the YAML file
    release = os.path.join(os.getcwd(), "release.json")
    with open(release, "r") as fd:
        data = json.load(fd)

    # PCT already bumps the release.json, such that the last element contains the newest version, since they are sorted
    newest_version = data["supportedImages"]["ops-manager"]["versions"][-1]
    update_tools_version(data, newest_version)

    with open(release, "w") as f:
        json.dump(
            data,
            f,
            indent=2,
        )
        f.write("\n")


def update_tools_version(data, missing_version):
    repo_owner = "10gen"
    repo_name = "mms"
    file_path = "server/conf/conf-hosted.properties"
    # starting om 7 our tag starts with ops-manager-<version> instead
    if missing_version.startswith("7."):
        tag_to_search = f"ops-manager-{missing_version}"
    else:
        tag_to_search = f"on-prem-{missing_version}"
    url = f"https://raw.githubusercontent.com/{repo_owner}/{repo_name}/{tag_to_search}/{file_path}"
    response = requests.get(url, headers=get_headers())
    # Check if the request was successful
    if response.status_code == 200:
        config = configparser.ConfigParser()
        input_data = (
            "[DEFAULT]\n" + response.text
        )  # configparser needs a section, but our properties do not contain one.
        config.read_string(input_data)
        mongo_tool_version = config.get("DEFAULT", "mongotools.version")
        version_name = f"mongodb-database-tools-rhel80-x86_64-{mongo_tool_version}.tgz"
        data["mongodbToolsBundle"]["ubi"] = version_name
    else:
        print(f"was not able to request file from {url}: {response.text}")
        sys.exit(1)


latest_5, latest_6 = get_latest_om_versions_from_evergreen_yml()
update_release_json()
