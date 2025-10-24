import argparse
import os
import subprocess

from lib.base_logger import logger
from scripts.release.argparse_utils import get_platforms_from_arg, get_scenario_from_arg
from scripts.release.build.build_info import (
    KUBECTL_PLUGIN_BINARY,
    load_build_info,
)
from scripts.release.build.build_scenario import SUPPORTED_SCENARIOS, BuildScenario
from scripts.release.kubectl_mongodb.utils import (
    create_s3_client,
    kubectl_plugin_name,
    parse_platform,
    s3_path,
)


def build_kubectl_plugin(local_dir: str, platforms: list[str]):
    logger.info(f"Building kubectl-mongodb plugin for platforms: {platforms}")

    for platform in platforms:
        os_name, arch_name = parse_platform(platform)

        os.makedirs(local_dir, exist_ok=True)
        output_filename = kubectl_plugin_name(os_name, arch_name)
        output_path = os.path.join(local_dir, output_filename)

        build_command = [
            "go",
            "build",
            "-o",
            output_path,
            "./cmd/kubectl-mongodb",
        ]

        env = os.environ.copy()
        env["GOOS"] = os_name
        env["GOARCH"] = arch_name
        env["CGO_ENABLED"] = "0"

        try:
            process = subprocess.Popen(
                build_command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1, env=env
            )

            for log in iter(process.stdout.readline, ""):
                print(log, end="")

            process.stdout.close()
            exit_code = process.wait()

            if exit_code != 0:
                raise Exception(f"Build command failed with exit code {exit_code}: {process.stderr}")

            logger.debug(f"Successfully built kubectl-mongodb for {platform} at {output_path}")
        except subprocess.CalledProcessError as e:
            raise Exception(f"Build command failed with code {e.returncode}: {e.stderr}")

    logger.info("Building kubectl-mongodb plugin completed successfully!")


def upload_artifacts_to_s3(local_dir: str, platforms: list[str], s3_bucket: str, version: str):
    """
    Uploads the artifacts that are generated to S3 bucket at a specific path.
    The S3 bucket and version are figured out and passed to this function based on BuildScenario.
    """
    if not os.path.isdir(local_dir):
        raise Exception(f"Input directory '{local_dir}' not found.")

    s3_client = create_s3_client()

    for platform in platforms:
        os_name, arch_name = parse_platform(platform)
        filename = kubectl_plugin_name(os_name, arch_name)
        filepath = os.path.join(local_dir, filename)
        if not os.path.isfile(filepath):
            raise Exception(f"Expected build artifact '{filename}' not found in '{local_dir}' directory.")

        s3_key = s3_path(filename, version)
        logger.info(f"Uploading artifact {filepath} to s3://{s3_bucket}/{s3_key}")

        try:
            s3_client.upload_file(filepath, s3_bucket, s3_key)
            logger.info(f"Successfully uploaded the artifact {filepath}")
        except Exception as e:
            raise Exception(f"Failed to upload file {filepath}: {e}")


def main():
    parser = argparse.ArgumentParser(
        description="Compile and upload kubectl-mongodb plugin binaries to S3 bucket based on the build scenario.",
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
    local_dir = "bin"

    build_kubectl_plugin(local_dir, platforms)

    upload_artifacts_to_s3(local_dir, platforms, build_info.s3_store, version)


if __name__ == "__main__":
    main()
