import subprocess
import os
import yaml

from lib.base_logger import logger

CHART_DIR = "helm_chart"

OCI_REGISTRY = "oci://268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/helm-charts"

def run_command(command: list[str], description: str):
    try:
        subprocess.run(command, check=True, text=True, capture_output=False)
        logger.info(f"Command {' '.join(command)} executed successfully.")
    except subprocess.CalledProcessError as e:
        logger.error(f"Error executing command: {' '.join(command)}")
        raise RuntimeError(f"{description} failed.") from e
    except FileNotFoundError:
        raise FileNotFoundError("Error: 'helm' command not found. Ensure Helm CLI is installed and in your PATH.")

def update_chart_and_get_metadata(chart_dir: str) -> tuple[str, str]:
    chart_path = os.path.join(chart_dir, "Chart.yaml")
    version_id = os.environ.get('version_id')
    if not version_id:
        raise ValueError("Error: Environment variable 'version_id' must be set to determine the chart version to publish.")
        
    new_version = f"0.0.0+{version_id}"

    logger.info(f"New helm chart version will be: {new_version}")

    if not os.path.exists(chart_path):
        raise FileNotFoundError(
            f"Error: Chart.yaml not found in directory '{chart_dir}'. "
            "Please ensure the directory exists and contains a valid Chart.yaml."
        )

    try:
        with open(chart_path, 'r') as f:
            data = yaml.safe_load(f)

        chart_name = data.get('name')
        if not chart_name:
             raise ValueError("Chart.yaml is missing required 'name' field.")

        data['version'] = new_version
        
        with open(chart_path, 'w') as f:
            yaml.safe_dump(data, f, sort_keys=False) 

        logger.info(f"Successfully updated version for chart '{chart_name}' to '{new_version}' before publishing it.")
        return chart_name, new_version

    except Exception as e:
        raise RuntimeError(f"Failed to read or update Chart.yaml: {e}")

def publish_helm_chart():
    try:
        chart_name, chart_version = update_chart_and_get_metadata(CHART_DIR)
        
        tgz_filename = f"{chart_name}-{chart_version}.tgz"
        logger.info(f"Packaging chart: {chart_name} with Version: {chart_version}")

        package_command = ["helm", "package", CHART_DIR]
        run_command(package_command, f"Packaging chart '{CHART_DIR}'")

        push_command = ["helm", "push", tgz_filename, OCI_REGISTRY]
        run_command(push_command, f"Pushing '{tgz_filename}' to '{OCI_REGISTRY}'")

        if os.path.exists(tgz_filename):
            logger.info(f"\nCleaning up local file: {tgz_filename}")
            os.remove(tgz_filename)
        
        logger(f"Helm Chart {chart_name}:{chart_version} was published successfully!")
    except (FileNotFoundError, RuntimeError, ValueError) as e:
        logger.error(f"\Failed publishing the helm chart: {e}")

if __name__ == "__main__":
    publish_helm_chart()
