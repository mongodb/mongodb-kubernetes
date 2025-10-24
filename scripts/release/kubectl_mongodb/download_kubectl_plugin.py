import argparse
import os

from botocore.exceptions import ClientError

from lib.base_logger import logger
from scripts.release.argparse_utils import get_platforms_from_arg, get_scenario_from_arg
from scripts.release.build.build_info import (
    KUBECTL_PLUGIN_BINARY,
    load_build_info,
)
from scripts.release.build.build_scenario import SUPPORTED_SCENARIOS, BuildScenario
from scripts.release.kubectl_mongodb.build_kubectl_plugin import (
    kubectl_plugin_name,
    parse_platform,
    s3_path,
)
from scripts.release.kubectl_mongodb.utils import create_s3_client


def local_tests_plugin_path(arch_name: str) -> str:
    return f"docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_{arch_name}"


def download_kubectl_plugin_from_s3(s3_bucket: str, s3_plugin_path: str, local_path: str):
    """
    Downloads the plugin for provided platform and puts it in the path expected by the tests image
    """
    s3_client = create_s3_client()

    logger.info(f"Downloading s3://{s3_bucket}/{s3_plugin_path} to {local_path}")

    try:
        s3_client.download_file(s3_bucket, s3_plugin_path, local_path)
        # change the file's permissions to make file executable
        os.chmod(local_path, 0o755)

        logger.info(f"Successfully downloaded artifact to {local_path}")
    except ClientError as e:
        if e.response["Error"]["Code"] == "404":
            raise Exception(f"Artifact not found at s3://{s3_bucket}/{s3_plugin_path}: {e}")
        raise Exception(f"Failed to download artifact. S3 Client Error: {e}")
    except Exception as e:
        raise Exception(f"An unexpected error occurred during download: {e}")


def main():
    parser = argparse.ArgumentParser(
        description="Download kubectl-mongodb plugin binaries from S3 bucket based on the build scenario.",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "-b",
        "--build-scenario",
        metavar="",
        action="store",
        default=BuildScenario.DEVELOPMENT,
        type=str,
        choices=SUPPORTED_SCENARIOS,
        help=f"""Build scenario when reading configuration from 'build_info.json'.
Options: {", ".join(SUPPORTED_SCENARIOS)}. For '{BuildScenario.DEVELOPMENT}' the '{BuildScenario.PATCH}' scenario is used to read values from 'build_info.json'""",
    )
    parser.add_argument(
        "-v",
        "--version",
        metavar="",
        action="store",
        required=True,
        type=str,
        help="Version to use when building kubectl-mongodb binary.",
    )
    parser.add_argument(
        "-p",
        "--platform",
        metavar="",
        action="store",
        type=str,
        help="Override the platforms instead of resolving from build scenario. Multi-arch builds are comma-separated. Example: linux/amd64,linux/arm64",
    )
    args = parser.parse_args()

    build_scenario = get_scenario_from_arg(args.build_scenario)
    build_info = load_build_info(build_scenario).binaries[KUBECTL_PLUGIN_BINARY]

    platforms = get_platforms_from_arg(args.platform) or build_info.platforms
    version = args.version

    for platform in platforms:
        os_name, arch_name = parse_platform(platform)
        if os_name != "linux":
            logger.debug(f"Skipping non-linux platform {platform}, not used in e2e tests in Evergreen CI")
            continue

        filename = kubectl_plugin_name(os_name, arch_name)
        s3_plugin_path = s3_path(filename, version)
        local_path = local_tests_plugin_path(arch_name)

        download_kubectl_plugin_from_s3(build_info.s3_store, s3_plugin_path, local_path)


if __name__ == "__main__":
    main()
