#!/usr/bin/env python3

import datetime
import json
import logging
import os
import subprocess
import sys

import pymongo

LOGLEVEL = os.environ.get("LOGLEVEL", "INFO").upper()
logging.basicConfig(level=LOGLEVEL)


def get_repo_root():
    output = subprocess.check_output("git rev-parse --show-toplevel".split())

    return output.decode("utf-8").strip()


def get_release_version() -> str:
    release_file = os.path.join(get_repo_root(), "release.json")
    return json.load(open(release_file))["mongodbOperator"]


def get_atlas_connection_string() -> str:
    password = os.environ["atlas_password"]
    cnx_str = os.environ["atlas_connection_string"]

    return cnx_str.format(password=password)


def mongo_client() -> pymongo.MongoClient:
    cnx_str = get_atlas_connection_string()
    return pymongo.MongoClient(cnx_str)


def add_release_version(version: str):
    client = mongo_client()

    database = os.environ["atlas_database"]
    collection = client[database]["operator"]

    year_from_now = datetime.datetime.now() + datetime.timedelta(days=365)

    result = collection.insert_one(
        {
            "released_on": datetime.datetime.now(),
            "version": version,
            "supported": True,
            "eol": year_from_now,
            "variants": ["ubi", "ubuntu"],
        }
    )

    logging.info(
        "Added new supported version: {} (id: {})".format(version, result.inserted_id)
    )


def main():
    version = get_release_version()
    logging.info("Adding new release: {}".format(version))

    add_release_version(version)

    return 0


if __name__ == "__main__":
    sys.exit(main())
