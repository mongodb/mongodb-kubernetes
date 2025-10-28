import argparse
import os
import subprocess

import yaml
from release.build.build_scenario import SUPPORTED_SCENARIOS

from lib.base_logger import logger
from scripts.release.build.build_info import *

CHART_DIR = "helm_chart"


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


# update_chart_and_get_metadata updates the helm chart's Chart.yaml and sets the proper version
# When we publish the helm chart to dev and staging we append `0.0.0+` in the chart version, details are
# here https://docs.google.com/document/d/1eJ8iKsI0libbpcJakGjxcPfbrTn8lmcZDbQH1UqMR_g/edit?tab=t.gg5ble8qlesq
def update_chart_and_get_metadata(chart_dir: str, version: str) -> tuple[str, str]:
    chart_path = os.path.join(chart_dir, "Chart.yaml")

    if not os.path.exists(chart_path):
        raise FileNotFoundError(
            f"Error: Chart.yaml not found in directory '{chart_dir}'. "
            "Please ensure the directory exists and contains a valid Chart.yaml."
        )

    try:
        with open(chart_path, "r") as f:
            data = yaml.safe_load(f)

        chart_name = data.get("name")
        if not chart_name:
            raise ValueError("Chart.yaml is missing required 'name' field.")
    except Exception as e:
        raise Exception(f"Unable to load Chart.yaml from dir {chart_path}: {e}")

    if data["version"] == version:
        logger.info(f"Chart '{chart_name}' already has version '{version}'. No update needed.")
        return chart_name, version

    try:
        data["version"] = version

        with open(chart_path, "w") as f:
            yaml.safe_dump(data, f, sort_keys=False)

        logger.info(f"Successfully updated version for chart '{chart_name}' to '{version}'.")
        return chart_name, version
    except Exception as e:
        raise RuntimeError(f"Failed to read or update Chart.yaml: {e}")


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


def publish_helm_chart(chart_info: HelmChartInfo, operator_version: str):
    try:
        # If version_prefix is not specified, the chart.yaml would already have correct chart version
        if chart_info.version_prefix is not None:
            helm_version = f"{chart_info.version_prefix}{operator_version}"
        else:
            helm_version = operator_version

        chart_name, chart_version = update_chart_and_get_metadata(CHART_DIR, helm_version)
        tgz_filename = f"{chart_name}-{chart_version}.tgz"

        logger.info(f"Packaging chart: {chart_name} with Version: {chart_version}")
        package_command = ["helm", "package", CHART_DIR]
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

    return publish_helm_chart(build_info.helm_charts["mongodb-kubernetes"], args.version)


if __name__ == "__main__":
    try:
        main()
    except Exception as main_err:
        raise Exception(f"Failure in the helm publishing process {main_err}")
