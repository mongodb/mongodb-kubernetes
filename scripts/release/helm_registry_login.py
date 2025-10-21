import argparse
import os
import subprocess
import sys

from lib.base_logger import logger
from scripts.release.build.build_info import load_build_info

BUILD_SCENARIO_RELEASE = "release"
QUAY_USERNAME_ENV_VAR = "quay_prod_username"
QUAY_PASSWORD_ENV_VAR = "quay_prod_robot_token"


def helm_registry_login_to_nonrelease(helm_registry: str, region: str):
    logger.info(f"Attempting to log into ECR registry: {helm_registry}, using helm registry login.")

    aws_command = ["aws", "ecr", "get-login-password", "--region", region]

    # as we can see the password is being provided by stdin, that would mean we will have to
    # pipe the aws_command (it figures out the password) into helm_command.
    helm_command = ["helm", "registry", "login", "--username", "AWS", "--password-stdin", helm_registry]

    try:
        logger.info("Starting AWS ECR credential retrieval.")
        aws_proc = subprocess.Popen(
            aws_command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True  # Treat input/output as text strings
        )

        logger.info("Starting Helm registry login.")
        helm_proc = subprocess.Popen(
            helm_command, stdin=aws_proc.stdout, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True
        )

        # Close the stdout stream of aws_proc in the parent process
        # to prevent resource leakage (only needed if you plan to do more processing)
        aws_proc.stdout.close()

        # Wait for the Helm command (helm_proc) to finish and capture its output
        helm_stdout, helm_stderr = helm_proc.communicate()

        # Wait for the AWS process to finish as well
        aws_proc.wait()

        if aws_proc.returncode != 0:
            _, aws_stderr = aws_proc.communicate()
            raise Exception(f"aws command to get password failed. Error: {aws_stderr}")

        if helm_proc.returncode == 0:
            logger.info("Login to helm registry was successful.")
            logger.info(helm_stdout.strip())
        else:
            raise Exception(
                f"Login to helm registry failed, Exit code: {helm_proc.returncode}, Error: {helm_stderr.strip()}"
            )

    except FileNotFoundError as e:
        # This catches errors if 'aws' or 'helm' are not in the PATH
        raise Exception(f"Command not found. Please ensure '{e.filename}' is installed and in your system's PATH.")
    except Exception as e:
        raise Exception(f"An unexpected error occurred: {e}.")


def main():
    parser = argparse.ArgumentParser(description="Script to login to the dev/staging helm registries.")
    parser.add_argument("--build_scenario", type=str, help="Build scenario (e.g., patch, staging etc).")
    args = parser.parse_args()

    build_scenario = args.build_scenario

    build_info = load_build_info(build_scenario)

    registry = build_info.helm_charts["mongodb-kubernetes"].registry
    region = build_info.helm_charts["mongodb-kubernetes"].region

    if build_scenario == BUILD_SCENARIO_RELEASE:
        return helm_registry_login_to_release(registry)

    return helm_registry_login_to_nonrelease(registry, region)


def helm_registry_login_to_release(registry):
    username = os.environ.get(QUAY_USERNAME_ENV_VAR)
    password = os.environ.get(QUAY_PASSWORD_ENV_VAR)

    if not username:
        raise Exception(f"Env var {QUAY_USERNAME_ENV_VAR} must be set with the quay username.")
    if not password:
        raise Exception(f"Env var {QUAY_PASSWORD_ENV_VAR} must be set with the quay password.")

    command = ["helm", "registry", "login", "--username", username, "--password-stdin", registry]

    try:
        result = subprocess.run(
            command,
            input=password,  # Pass the password as input bytes
            capture_output=True,
            text=True,
            check=False,  # Do not raise an exception on non-zero exit code
        )

        if result.returncode == 0:
            logger.info(f"Successfully logged into helm continer registry {registry}.")
        else:
            raise Exception(
                f"Helm registry login failed to {registry}. Stdout: {result.stderr.strip()}, Stderr: {result.stderr.strip()}"
            )

    except FileNotFoundError:
        raise Exception("Error: 'helm' command not found. Ensure Helm CLI is installed and in your PATH.")
    except Exception as e:
        raise Exception(f"An unexpected error occurred during execution: {e}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        logger.error(f"Failed while logging in to the helm registry. Error: {e}")
        sys.exit(1)
