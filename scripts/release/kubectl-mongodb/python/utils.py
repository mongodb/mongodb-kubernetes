import subprocess
import sys

from lib.base_logger import logger

KUBECTL_PLUGIN_BINARY_NAME = "kubectl-mongodb"


def run_goreleaser_command(command: list[str]):
    try:
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

        for log in iter(process.stdout.readline, ""):
            print(log, end="")

        process.stdout.close()
        exit_code = process.wait()

        if exit_code != 0:
            logger.debug(f"GoReleaser command failed with exit code {exit_code}.")
            sys.exit(1)

        logger.info("GoReleaser build completed successfully!")

    except FileNotFoundError:
        logger.debug(
            "ERROR: 'goreleaser' command not found. Please ensure goreleaser is installed and in your system's PATH."
        )
        sys.exit(1)
    except Exception as e:
        logger.debug(f"An unexpected error occurred while running `goreleaser build`: {e}")
        sys.exit(1)
