import os
import subprocess
import sys

import yaml

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


# update_chart_and_get_metadata updates the helm chart's Chart.yaml and sets the version
# to either evg patch id or commit which is set in OPERATOR_VERSION.
def update_chart_and_get_metadata(chart_dir: str) -> tuple[str, str]:
    chart_path = os.path.join(chart_dir, "Chart.yaml")
    version_id = os.environ.get("OPERATOR_VERSION")
    if not version_id:
        raise ValueError(
            "Error: Environment variable 'OPERATOR_VERSION' must be set to determine the chart version to publish."
        )

    new_version = f"0.0.0+{version_id}"
    logger.info(f"New helm chart version will be: {new_version}")

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

        data["version"] = new_version

        with open(chart_path, "w") as f:
            yaml.safe_dump(data, f, sort_keys=False)

        logger.info(f"Successfully updated version for chart '{chart_name}' to '{new_version}'.")
        return chart_name, new_version
    except Exception as e:
        raise RuntimeError(f"Failed to read or update Chart.yaml: {e}")


def get_oci_registry(chart_info: HelmChartInfo) -> str:
    registry = chart_info.registry
    repo = chart_info.repository

    if not registry:
        raise ValueError("Error: registry doesn't seem to be set in HelmChartInfo.")

    if not repo:
        raise ValueError("Error: reposiotry doesn't seem to be set in HelmChartInfo.")

    oci_registry = f"oci://{registry}/{repo}"
    logger.info(f"Determined OCI Registry: {oci_registry}")
    return oci_registry


def publish_helm_chart(chart_info: HelmChartInfo):
    try:
        oci_registry = get_oci_registry(chart_info)
        chart_name, chart_version = update_chart_and_get_metadata(CHART_DIR)
        tgz_filename = f"{chart_name}-{chart_version}.tgz"

        try:
            logger.info(f"Packaging chart: {chart_name} with Version: {chart_version}")
            package_command = ["helm", "package", CHART_DIR]
            run_command(package_command)

            logger.info(f"Pushing chart to registry: {oci_registry}")
            push_command = ["helm", "push", tgz_filename, oci_registry]
            run_command(push_command)

            logger.info(f"Helm Chart {chart_name}:{chart_version} was published successfully!")
        finally:
            # Cleanup the local .tgz file regardless of push success/failure
            if os.path.exists(tgz_filename):
                logger.info(f"Cleaning up local file: {tgz_filename}")
                os.remove(tgz_filename)

    except (FileNotFoundError, RuntimeError, ValueError) as e:
        raise Exception(f"Failed publishing the helm chart {e}")


def main():
    build_scenario = os.environ.get("BUILD_SCENARIO")
    build_info = load_build_info(build_scenario)

    return publish_helm_chart(build_info.helm_charts["mongodb-kubernetes"])


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        logger.error(f"Failure in the helm publishing process {e}")
        sys.exit(1)
