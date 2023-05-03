#!/usr/bin/env python3

import json
import os
from distutils.version import StrictVersion

import yaml


def get_latest_om_versions_from_evergreen_yml():
    # Define a custom constructor to preserve the anchors in the YAML file
    evergreen_file = os.path.join(os.getcwd(), ".evergreen.yml")
    with open(evergreen_file) as f:
        data = yaml.safe_load(f)
    return data["variables"][1], data["variables"][3]


# get release.json
def update_release_json(versions):
    # Define a custom constructor to preserve the anchors in the YAML file
    release = os.path.join(os.getcwd(), "release.json")
    with open(release, "r") as fd:
        data = json.load(fd)
    for version in versions:
        if version not in data["supportedImages"]["ops-manager"]["versions"]:
            data["supportedImages"]["ops-manager"]["versions"].insert(version)
    data["supportedImages"]["ops-manager"]["versions"].sort(key=StrictVersion)

    with open(release, "w") as f:
        json.dump(
            data,
            f,
            indent=2,
        )
        f.write("\n")


latest_5, latest_6 = get_latest_om_versions_from_evergreen_yml()
update_release_json([latest_5, latest_6])
