import argparse
import subprocess

from lib.base_logger import logger
from scripts.release.build.build_info import *
from scripts.release.build.build_scenario import SUPPORTED_SCENARIOS

CHART_DIR = "helm_chart"
MONGODB_KUBERNETES_CHART = "mongodb-kubernetes"


def run_command(command: list[str]):
    try:
        # Using capture_output=True to grab stdout/stderr for better error logging.
        process = subprocess.run(command, check=True, text=True, capture_output=True)
        logger.info(f"Successfully executed: {' '.join(command)}")
        if process.stdout:
            logger.info(process.stdout)
    except subprocess.CalledProcessError as e:
        raise RuntimeError(f"Command {' '.join(command)} failed. Stderr: {e.stderr.strip()}") from e
    except FileNotFoundError:
        raise FileNotFoundError(
            f"Error: {command[0]} command not found. Ensure {command[0]} is installed and in your PATH."
        )


def get_oci_registry(chart_info: HelmChartInfo) -> str:
    registry = chart_info.registry
    repo = chart_info.repository

    if not registry:
        raise ValueError("Error: registry doesn't seem to be set in HelmChartInfo.")

    if not repo:
        raise ValueError("Error: repository doesn't seem to be set in HelmChartInfo.")

    oci_registry = f"oci://{registry}/{repo}"
    logger.info(f"Determined OCI Registry: {oci_registry}")
    return oci_registry


def publish_helm_chart(chart_name: str, chart_info: HelmChartInfo, operator_version: str):
    try:
        # If version_prefix is not specified, use the operator_version as is.
        if chart_info.version_prefix is not None:
            chart_version = f"{chart_info.version_prefix}{operator_version}"
        else:
            chart_version = operator_version

        tgz_filename = f"{chart_name}-{chart_version}.tgz"

        logger.info(f"Packaging chart: {chart_name} with Version: {chart_version}")
        package_command = ["helm", "package", "--version", chart_version, CHART_DIR]
        run_command(package_command)

        oci_registry = get_oci_registry(chart_info)
        logger.info(f"Pushing chart to registry: {oci_registry}")
        push_command = ["helm", "push", tgz_filename, oci_registry]
        run_command(push_command)

        logger.info(f"Helm Chart {chart_name}:{chart_version} was published successfully!")
    except Exception as e:
        raise Exception(f"Failed publishing the helm chart {e}")


def main():
    parser = argparse.ArgumentParser(
        description="Script to publish helm chart to the OCI container registry, based on the build scenario.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "-b",
        "--build-scenario",
        metavar="",
        action="store",
        required=True,
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
        help="Operator version to use when publishing helm chart",
    )
    args = parser.parse_args()

    build_scenario = args.build_scenario
    build_info = load_build_info(build_scenario)

    return publish_helm_chart(MONGODB_KUBERNETES_CHART, build_info.helm_charts[MONGODB_KUBERNETES_CHART], args.version)


if __name__ == "__main__":
    try:
        main()
    except Exception as main_err:
        raise Exception(f"Failure in the helm publishing process {main_err}")
