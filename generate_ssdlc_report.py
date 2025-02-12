#!/usr/bin/env python3

"""
SSDLC report 

At the moment, the following functionality has been implemented:
    - Downloading SBOMs
    - Replacing Date
    - Replacing Release Version

The script should be called manually from the project root. It will put generated files
into "./ssdlc-report/MEKO-$release" directory.
"""

import enum
import json
import os
import pathlib
import subprocess
from concurrent.futures import ProcessPoolExecutor
from dataclasses import dataclass
from datetime import datetime
from queue import Queue
from typing import Dict, List

import boto3

from lib.base_logger import logger
from scripts.evergreen.release.agent_matrix import (
    get_supported_version_for_image_matrix_handling,
)

NUMBER_OF_THREADS = 15
S3_BUCKET = "kubernetes-operators-sboms"


class Subreport(enum.Enum):
    AGENT = ("Containerized MongoDB Agent", "scripts/ssdlc/templates/SSDLC Containerized MongoDB Agent ${VERSION}.md")
    OPERATOR = (
        "Enterprise Kubernetes Operator",
        "scripts/ssdlc/templates/SSDLC Containerized MongoDB Enterprise Kubernetes Operator ${VERSION}.md",
    )
    OPS_MANAGER = (
        "Containerized OpsManager",
        "scripts/ssdlc/templates/SSDLC Containerized MongoDB Enterprise OpsManager ${VERSION}.md",
    )
    TESTING = ("not-used", "scripts/ssdlc/templates/SSDLC MongoDB Enterprise Operator Testing Report ${VERSION}.md")

    def __new__(cls, *args, **kwds):
        value = len(cls.__members__) + 1
        obj = object.__new__(cls)
        obj._value_ = value
        return obj

    def __init__(self, sbom_subpath: str, template_path: str):
        self.sbom_subpath = sbom_subpath
        self.template_path = template_path


@dataclass
class SupportedImage:
    versions: List[str]
    name: str
    image_pull_spec: str
    ssdlc_report_name: str
    sbom_file_names: List[str]
    platforms: List[str]
    subreport: Subreport


def get_release() -> Dict:
    with open("release.json") as release:
        return json.load(release)


def get_supported_images(release: Dict) -> dict[str, SupportedImage]:
    logger.debug(f"Getting list of supported images")
    supported_images: Dict[str, SupportedImage] = dict()
    for supported_image in release["supportedImages"]:
        ssdlc_name = release["supportedImages"][supported_image]["ssdlc_name"]
        if "versions" in release["supportedImages"][supported_image]:
            for tag in release["supportedImages"][supported_image]["versions"]:
                if supported_image not in supported_images:
                    subreport = Subreport.OPERATOR
                    if supported_image == "ops-manager":
                        subreport = Subreport.OPS_MANAGER
                    supported_images[supported_image] = SupportedImage(
                        list(), supported_image, "", ssdlc_name, list(), ["linux/amd64"], subreport
                    )
                supported_images[supported_image].versions.append(tag)

    supported_images = filter_out_unsupported_images(supported_images)
    supported_images = convert_to_image_names(supported_images)
    supported_images["mongodb-agent-ubi"] = SupportedImage(
        get_supported_version_for_image_matrix_handling("mongodb-agent", latest_operator_only=True),
        "mongodb-agent-ubi",
        "quay.io/mongodb/mongodb-agent-ubi",
        release["supportedImages"]["mongodb-agent"]["ssdlc_name"],
        list(),
        # Once MEKO supports both architectures, this should be re-enabled.
        # ["linux/amd64", "linux/arm64"],
        ["linux/amd64"],
        Subreport.AGENT,
    )

    supported_images["mongodb-enterprise-cli"] = SupportedImage(
        [release["mongodbOperator"]],
        "mongodb-enterprise-cli",
        "mongodb-enterprise-cli",
        "MongoDB Enterprise Kubernetes Operator CLI",
        list(),
        ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"],
        Subreport.OPERATOR,
    )
    supported_images = filter_out_non_current_versions(release, supported_images)
    logger.debug(f"Supported images: {supported_images}")
    return supported_images


def convert_to_image_names(supported_images: Dict[str, SupportedImage]):
    for supported_image in supported_images:
        supported_images[supported_image].image_pull_spec = f"quay.io/mongodb/mongodb-enterprise-{supported_image}-ubi"
        supported_images[supported_image].name = f"mongodb-enterprise-{supported_image}-ubi"
    return supported_images


# noinspection PyShadowingNames
def filter_out_unsupported_images(supported_images: Dict[str, SupportedImage]):
    if "mongodb-agent" in supported_images:
        del supported_images["mongodb-agent"]
    if "appdb-database" in supported_images:
        del supported_images["appdb-database"]
    if "mongodb-enterprise-server" in supported_images:
        del supported_images["mongodb-enterprise-server"]
    if "mongodb-kubernetes-operator-version-upgrade-post-start-hook" in supported_images:
        del supported_images["mongodb-kubernetes-operator-version-upgrade-post-start-hook"]
    if "mongodb-kubernetes-readinessprobe" in supported_images:
        del supported_images["mongodb-kubernetes-readinessprobe"]
    if "mongodb-kubernetes-operator" in supported_images:
        del supported_images["mongodb-kubernetes-operator"]
    return supported_images


def filter_out_non_current_versions(release: Dict, supported_images: Dict[str, SupportedImage]):
    for supported_image_key in supported_images:
        supported_image = supported_images[supported_image_key]
        if supported_image.subreport == Subreport.OPERATOR:
            supported_image.versions = filter(lambda x: x == release["mongodbOperator"], supported_image.versions)
    return supported_images


def _download_augmented_sbom(s3_path: str, directory: str, sbom_name: str):
    logger.debug(f"Downloading Augmented SBOM for {s3_path} into {directory}/{sbom_name}")

    s3 = boto3.client("s3")
    response = s3.list_objects(Bucket=S3_BUCKET, Prefix=s3_path, MaxKeys=1)
    if "Contents" not in response:
        raise Exception(f"Could not download Augmented SBOM for {s3_path}")
    else:
        augmented_sbom_key = response["Contents"][0]["Key"]
        pathlib.Path(directory).mkdir(parents=True, exist_ok=True)
        with open(f"{directory}/{sbom_name}", "wb") as data:
            s3.download_fileobj(S3_BUCKET, augmented_sbom_key, data)


def _queue_exception_handling(tasks_queue, ignore_sbom_download_errors: bool):
    exceptions_found = False
    for task in tasks_queue.queue:
        if task.exception() is not None:
            exceptions_found = True
            logger.fatal(f"The following exception has been found when downloading SBOMs: {task.exception()}")
    if exceptions_found:
        if not ignore_sbom_download_errors:
            raise Exception(f"Exception(s) found when downloading SBOMs")


def download_augmented_sboms(
    report_path: str, supported_images: Dict[str, SupportedImage], ignore_sbom_download_errors: bool
) -> Dict[str, SupportedImage]:
    tasks_queue = Queue()
    with ProcessPoolExecutor(max_workers=NUMBER_OF_THREADS) as executor:
        for supported_image_key in supported_images:
            supported_image = supported_images[supported_image_key]
            for tag in supported_image.versions:
                for platform in supported_image.platforms:
                    platform_sanitized = platform.replace("/", "-")
                    s3_path = f"sboms/release/augmented/{supported_image.image_pull_spec}/{tag}/{platform_sanitized}"
                    sbom_name = f"{supported_image.name}_{tag}_{platform_sanitized}-augmented.json"
                    tasks_queue.put(
                        executor.submit(
                            _download_augmented_sbom,
                            s3_path,
                            f"{report_path}/{supported_image.subreport.sbom_subpath}",
                            sbom_name,
                        )
                    )
                    supported_image.sbom_file_names.append(sbom_name)
    _queue_exception_handling(tasks_queue, ignore_sbom_download_errors)
    return supported_images


def prepare_sbom_markdown(supported_images: Dict[str, SupportedImage], subreport: Subreport):
    lines = ""
    for supported_image_key in supported_images:
        supported_image = supported_images[supported_image_key]
        if supported_image.subreport == subreport:
            lines = f"{lines}\n\t\t- {supported_image.ssdlc_report_name}:"
            for sbom_location in supported_image.sbom_file_names:
                lines = (
                    f"{lines}\n\t\t\t- [{sbom_location}](./{supported_image.subreport.sbom_subpath}/{sbom_location})"
                )
    return lines


def get_git_user_name() -> str:
    res = subprocess.run(["git", "config", "user.name"], stdout=subprocess.PIPE)
    return res.stdout.strip().decode()


def generate_ssdlc_report(ignore_sbom_download_errors: bool = False):
    """Generates the SSDLC report.

    :param ignore_sbom_download_errors: True if downloading SBOM errors should be ignored.
    :return: N/A
    """
    logger.info(f"Producing SSDLC report for more manual edits")
    release = get_release()

    operator_version = release["mongodbOperator"]
    supported_images = get_supported_images(release)
    report_path = os.getcwd() + "/ssdlc-report/MEKO-" + operator_version

    try:
        if not os.path.exists(path=report_path):
            os.makedirs(report_path)

        logger.info(f"Downloading SBOMs")
        downloaded_sboms = download_augmented_sboms(report_path, supported_images, ignore_sbom_download_errors)

        for subreport in Subreport:
            logger.info(f"Generating subreport {subreport.template_path}")
            with open(subreport.template_path, "r") as report_template:
                content = report_template.read()

                content = content.replace("${SBOMS}", prepare_sbom_markdown(downloaded_sboms, subreport))
                content = content.replace("${VERSION}", operator_version)
                content = content.replace("${DATE}", datetime.today().strftime("%Y-%m-%d"))
                content = content.replace("${RELEASE_TYPE}", "Minor")
                content = content.replace("${AUTHOR}", get_git_user_name())

                report_file_name = subreport.template_path.replace("${VERSION}", operator_version)
                report_file_name = os.path.basename(report_file_name)
                report_file_name = f"{report_path}/{report_file_name}"
                with open(report_file_name, "w") as file:
                    file.write(content)
        logger.info(f"Done")
    except:
        logger.exception(f"Could not generate report")


if __name__ == "__main__":
    generate_ssdlc_report()
