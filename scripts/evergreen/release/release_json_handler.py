#!/usr/bin/env python3

"""
Works with release.json: reads, updates.
"""

import json
from enum import Enum
from typing import List

RELEASE_JSON = "release.json"


class ReleaseObject(Enum):
    mongodb_operator = "mongodbOperator"
    appdb = "appDBImageAgentVersion"
    init_appdb = "initAppDbVersion"
    init_database = "initDatabaseVersion"
    init_om = "initOpsManagerVersion"


def read_release_from_file(release_object: ReleaseObject) -> str:
    return read_value_from_file([release_object.value])


def read_value_from_file(keys: List[str]) -> str:
    with open(RELEASE_JSON, "r") as fd:
        content = json.load(fd)
    current_element = content

    for key in keys:
        current_element = current_element[key]

    return current_element


def update_release_json(release_object: ReleaseObject, release_version: str):
    with open(RELEASE_JSON, "r") as fd:
        doc = json.load(fd)
        doc[release_object.value] = release_version

    with open(RELEASE_JSON, "w") as fd:
        json.dump(doc, fd, indent=2)
        fd.write("\n")
