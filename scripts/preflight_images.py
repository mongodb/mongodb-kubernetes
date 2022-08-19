#!/usr/bin/env python3
import argparse
import json
import logging
import os
import sys
import subprocess

from typing import List, Dict, Tuple

import requests

LOGLEVEL = os.environ.get("LOGLEVEL", "INFO").upper()
logging.basicConfig(level=LOGLEVEL)


def image_config(
    image: str,
    rh_ospid: str,
    rh_cert_project_id: str,
    name_prefix: str = "mongodb-enterprise-",
) -> Tuple[str, Dict[str, str]]:

    if image == "operator":
        prefix = "mongodb-enterprise-"
        args = {
            "rh_registry": f"scan.connect.redhat.com/ospid-{rh_ospid}/{prefix}{image}",
            "public_rh_registry": f"registry.connect.redhat.com/mongodb/{name_prefix}{image}",
            "rh_cert_project_id": rh_cert_project_id,
        }
    else:
        args = {
            "rh_registry": f"scan.connect.redhat.com/ospid-{rh_ospid}/{name_prefix}{image}",
            "public_rh_registry": f"registry.connect.redhat.com/mongodb/{name_prefix}{image}",
            "rh_cert_project_id": rh_cert_project_id,
        }
    return image, args


def args_for_image(image: str) -> Dict[str, str]:
    image_configs = [
        image_config(
            "appdb", "31c2f102-af15-4e15-87b9-30710586d9ad", "60b8fedf2937381643cffb88"
        ),
        image_config(
            "database",
            "239de277-d8bb-44b4-8593-73753752317f",
            "5e6231eb02235d3f505f60c4",
            name_prefix="enterprise-",
        ),
        image_config(
            "init-appdb",
            "053baed4-c625-44bb-a9bf-a3a5585a17e8",
            "5ebbbcf9794b2c602c8a9f70",
        ),
        image_config(
            "init-database",
            "cf1063a9-6391-4dd7-b995-a4614483e6a1",
            "5f58e4d322f81af950128b57",
        ),
        image_config(
            "init-ops-manager",
            "7da92b80-396f-4298-9de5-909165ba0c9e",
            "5ebbbcfcc697cb6ae43265d6",
        ),
        image_config(
            "operator",
            "5558a531-617e-46d7-9320-e84d3458768a",
            "5e622f9d02235d3f505f60c3",
            name_prefix="enterprise-",
        ),
        image_config(
            "ops-manager",
            "b419ca35-17b4-4655-adee-a34e704a6835",
            "5e60b1d32f3c1acdd05f609d",
        ),
        image_config(
            "mongodb-agent",
            "b2beced3-e4db-46e1-9850-4b85ab4ff8d6",
            "6098ffd856933b164fe21129",
            name_prefix="",
        ),
    ]
    images = {k: v for k, v in image_configs}
    return images[image]


def get_api_token():
    token = os.environ.get("rh_pyxis", "")
    return token


def get_project_id():
    project_id = os.environ.get("project_id", "")
    return project_id


def get_supported_version_for_image(image: str) -> List[Dict[str, str]]:
    supported_versions = (
        "https://webhooks.mongodb-realm.com/api/client/v2.0/app/"
        "kubernetes-release-support-kpvbh/service/"
        "supported-{}-versions/incoming_webhook/list".format(image)
    )

    versions = requests.get(supported_versions).json()
    return {v["version"] for v in versions}


def run_preflight_check(image: str, version: str, submit: bool = False) -> int:
    login_command = [
        "podman",
        "login",
        "--username",
        "unused",
        "--password",
        f"{get_project_id()}",
        "--authfile",
        "./temp-authfile.json",
        "scan.connect.redhat.com",
    ]
    logging.info(f"Logging in to scan.connect.redhat.com")
    login = subprocess.run(login_command)
    if login.returncode != 0:
        return login.returncode

    preflight_command = [
        "preflight",
        "check",
        "container",
        f"{args_for_image(image)['rh_registry']}:{version}",
    ]
    if submit:
        preflight_command.extend(
            [
                "--submit",
                f"--pyxis-api-token={get_api_token()}",
                f"--certification-project-id={args_for_image(image)['rh_cert_project_id']}",
            ]
        )
    preflight_command.append("--docker-config=./temp-authfile.json")
    logging.info(f"Submitting image {args_for_image(image)['rh_registry']}:{version}")
    logging.info(f'Running command: {" ".join(preflight_command)}')
    submit = subprocess.run(preflight_command)
    return submit.returncode


def get_available_versions_for_image(image: str):
    image_args = args_for_image(image)
    output = subprocess.check_output(
        [
            "podman",
            "search",
            image_args["public_rh_registry"],
            "--list-tags",
            "--format",
            "json",
            "--limit",
            "200",
        ]
    )

    return json.loads(output.decode("utf-8"))[0].get("Tags", [])


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--image", help="image to run preflight checks on", type=str, required=True
    )
    parser.add_argument(
        "--submit", help="submit image for certification (true|false)", type=str, required=True
    )
    parser.add_argument(
        "--version", help="specific version to check", type=str, default=None
    )
    args = parser.parse_args()
    available_versions = get_available_versions_for_image(args.image)
    supported_versions = get_supported_version_for_image(args.image)
    submit = args.submit.lower() == "true"

    image_version = os.environ.get("image_version", args.version)

    # Attempt to run a pre-flight check on a single version of the image
    logging.info("Submitting preflight check for a single image version")
    if image_version is not None:
        if image_version not in supported_versions:
            logging.error(
                f"Version {image_version} for image {args.image} is not supported. Supported versions: {supported_versions}"
            )
            return 1
        elif image_version in available_versions:
            logging.warn(
                f"Version {image_version} for image {args.image} is already published."
            )
        else:
            return_code = run_preflight_check(
                args.image, image_version, submit=submit
            )
            if return_code != 0:
                logging.error(
                    f"Running preflight check for image: {args.image}:{image_version} failed with exit code: {return_code}"
                )
        return 0

    # Attempt to run pre-flight checks on all the supported and unpublished versions of the image
    logging.info("Submitting preflight check for all unpublished image versions")
    missing_versions = [v for v in supported_versions if v not in available_versions]
    if len(missing_versions) == 0:
        logging.info(f"Every supported version for: {args.image} was already checked")
    for version in missing_versions:
        logging.info(f"Running preflight check for image: {args.image}:{version}")
        return_code = run_preflight_check(args.image, version, submit=submit)
        if return_code != 0:
            logging.error(
                f"Running preflight check for image: {args.image}:{version} failed with exit code: {return_code}"
            )
    return 0


if __name__ == "__main__":
    sys.exit(main())
