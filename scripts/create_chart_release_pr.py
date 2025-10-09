import argparse
import os
import shutil
import subprocess
import sys
import tempfile

from github import Github, GithubException

from lib.base_logger import logger

REPO_URL = "https://github.com/mongodb/helm-charts.git"
REPO_NAME = "mongodb/helm-charts"
TARGET_CHART_SUBDIR = "charts/mongodb-kubernetes"
BASE_BRANCH = "main"


# run_command runs the command `command` from dir cwd
def run_command(command, cwd=None):
    logger.info(f"Running command: {' '.join(command)} in directory {cwd}")
    result = subprocess.run(command, capture_output=True, text=True, cwd=cwd)
    if result.returncode != 0:
        logger.error("ERROR:")
        logger.error(result.stdout)
        logger.error(result.stderr)
        raise RuntimeError(f"Command failed: {' '.join(command)}")
    logger.info("Command succeeded")
    return result.stdout


def create_pull_request(branch_name, chart_version):
    logger.info("Creating the pull request in the helm-charts repo.")
    github_token = os.environ.get("GH_TOKEN")

    if not github_token:
        logger.info("Warning: GH_TOKEN environment variable not set.")
        pr_url = f"https://github.com/{REPO_NAME}/pull/new/{branch_name}"
        logger.info(f"Please create the Pull Request manually by following the link:\n{pr_url}")

    try:
        g = Github(github_token)
        repo = g.get_repo(REPO_NAME)
        pr_title = f"Release MCK {chart_version}"
        body = f"This PR publishes the MCK chart version {chart_version}."

        pr = repo.create_pull(
            title=pr_title,
            body=body,
            head=branch_name,
            base=BASE_BRANCH,
        )
        logger.info(f"Successfully created Pull Request {pr.html_url}")
    except GithubException as e:
        logger.error(f"ERROR: Could not create Pull Request. GitHub API returned an error: {e.status}")
        logger.error(f"Details: {e.data}")
        logger.error("Please check your github token permissions and repository details.")
        return 1
    except Exception as e:
        logger.error(f"An unexpected error occurred while creating the PR: {e}")
        return 1


def main():
    parser = argparse.ArgumentParser(
        description="Automate PR creation to release MCK helm chart to github helm chart repo."
    )
    parser.add_argument(
        "--chart_version", help="The version of the chart to be released (e.g., '1.3.0').", required=True
    )
    args = parser.parse_args()

    chart_version = args.chart_version
    branch_name = f"mck-release-{chart_version}"

    workdir = os.environ.get("MCK_DIR")
    if not workdir:
        logger.info("The workdir environment variable is not set this should be set to the root of MCK code.")
        return 1
    # source_chart_path is local helm chart in MCK repo
    source_chart_path = os.path.join(workdir, "helm_chart")

    if not os.path.isdir(source_chart_path):
        logger.info(f"The source chart path '{source_chart_path}' is not a valid directory.")
        return 1

    github_token = os.environ.get("GH_TOKEN")

    with tempfile.TemporaryDirectory() as temp_dir:
        helm_repo_path = os.path.join(temp_dir, "helm-charts")
        logger.info(f"Working in a temporary directory: {temp_dir}")

        try:
            run_command(["git", "clone", REPO_URL, helm_repo_path])
            run_command(["git", "checkout", "-b", branch_name], cwd=helm_repo_path)

            target_dir = os.path.join(helm_repo_path, TARGET_CHART_SUBDIR)
            logger.info(f"Clearing content from dir '{target_dir}'")
            if os.path.exists(target_dir):
                for item in os.listdir(target_dir):
                    item_path = os.path.join(target_dir, item)
                    if os.path.isdir(item_path):
                        shutil.rmtree(item_path)
                    else:
                        os.remove(item_path)

            logger.info(f"Copying local MCK chart from '{source_chart_path}' to helm repo chart path {target_dir}")
            shutil.copytree(source_chart_path, target_dir, dirs_exist_ok=True)

            commit_message = f"Release MCK {chart_version}"
            run_command(["git", "add", "."], cwd=helm_repo_path)
            run_command(["git", "commit", "-m", commit_message], cwd=helm_repo_path)

            if github_token:
                logger.info("Configuring remote URL for authenticated push...")
                # Constructs a URL like https://x-access-token:YOUR_TOKEN@github.com/owner/repo.git
                authenticated_url = f"https://x-access-token:{github_token}@{REPO_URL.split('//')[1]}"
                run_command(["git", "remote", "set-url", "origin", authenticated_url], cwd=helm_repo_path)
            else:
                logger.error("github token not found. Push may fail if credentials are not cached.")
                return 1

            run_command(["git", "push", "-u", "origin", branch_name], cwd=helm_repo_path)

            create_pull_request(branch_name, chart_version)

        except (RuntimeError, FileNotFoundError, PermissionError) as e:
            logger.error(f"\nAn error occurred during local git operations: {e}")
            return 1


if __name__ == "__main__":
    sys.exit(main())
