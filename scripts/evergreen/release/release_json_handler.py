#!/usr/bin/env python3

"""
Works with release.json: reads, updates.
"""

import json
from enum import Enum

RELEASE_JSON = "release.json"


class ReleaseObject(Enum):
    mongodb_operator = "mongodbOperator"
    init_appdb = "initAppDbVersion"
    init_database = "initDatabaseVersion"
    init_om = "initOpsManagerVersion"


def read_release_from_file(release_object: ReleaseObject) -> str:
    with open(RELEASE_JSON, "r") as fd:
        content = json.load(fd)

    return content[release_object.value]


def update_release_json(release_object: ReleaseObject, release_version: str):
    with open(RELEASE_JSON, "r") as fd:
        doc = json.load(fd)
        doc[release_object.value] = release_version

    with open(RELEASE_JSON, "w") as fd:
        json.dump(doc, fd, indent=2)
        fd.write("\n")
