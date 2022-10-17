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
    rh_cert_project_id: str,
    name_prefix: str = "mongodb-enterprise-",
    name_suffix: str = "-ubi"
) -> Tuple[str, Dict[str, str]]:
    args = {
        "registry": f"quay.io/mongodb/{name_prefix}{image}{name_suffix}",
        "rh_cert_project_id": rh_cert_project_id,
    }
    return image, args


def args_for_image(image: str) -> Dict[str, str]:
    image_configs = [
        image_config(
            "database",
            "633fc9e582f7934b1ad3be45",
        ),
        image_config(
            "init-appdb",
            "633fcb576f43719c9df9349f",
        ),
        image_config(
            "init-database",
            "633fcc2982f7934b1ad3be46",
        ),
        image_config(
            "init-ops-manager",
            "633fccb16f43719c9df934a0",
        ),
        image_config(
            "operator",
            "633fcdfaade0e891294196ac",
        ),
        image_config(
            "ops-manager",
            "633fcd36c4ee7ff29edff589",
        ),
        image_config(
            "mongodb-agent",
            "633fcfd482f7934b1ad3be47",
            name_prefix="",
        ),
    ]
    images = {k: v for k, v in image_configs}
    return images[image]


def get_api_token():
    token = os.environ.get("rh_pyxis", "")
    return token

def get_supported_version_for_image(image: str) -> List[Dict[str, str]]:
    supported_versions = (
        "https://webhooks.mongodb-realm.com/api/client/v2.0/app/"
        "kubernetes-release-support-kpvbh/service/"
        "supported-{}-versions/incoming_webhook/list".format(image)
    )

    versions = requests.get(supported_versions).json()
    return {v["version"] for v in versions}


def run_preflight_check(image: str, version: str, submit: bool = False) -> int:
    # In theory, we could remove this as our container images reside in public repo
    # However, due to https://github.com/redhat-openshift-ecosystem/openshift-preflight/issues/685
    # we need to supply a non-empty --docker-config
    public_auth = """
    {
        "auths": {
            "quay.io": {
                "auth": ""
            }
        }
    }
    """
    logging.info(f"Creating auth file: {public_auth}")
    with open("./temp-authfile.json", "w") as file:
        file.write(public_auth)

    preflight_command = [
        "preflight",
        "check",
        "container",
        f"{args_for_image(image)['registry']}:{version}",
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
    logging.info(f"Submitting image {args_for_image(image)['registry']}:{version}")
    logging.info(f'Running command: {" ".join(preflight_command)}')
    submit = subprocess.run(preflight_command)
    return submit.returncode


def get_available_versions_for_image(image: str):
    image_args = args_for_image(image)
    logging.info(f'Searching for available tags for: {image}')
    logging.info(f'Image args: {image_args}')
    output = subprocess.check_output(
        [
            "podman",
            "search",
            image_args["registry"],
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
