import argparse
import os
import sys
import tarfile

import boto3
import build_kubectl_plugin
from botocore.exceptions import ClientError, NoCredentialsError, PartialCredentialsError
from github import Github, GithubException

GITHUB_REPO = "mongodb/mongodb-kubernetes"
GITHUB_TOKEN = os.environ.get("GH_TOKEN")

LOCAL_ARTIFACTS_DIR = "artifacts"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--release_version",
        required=True,
        help="product release version, ideally should match with github tag/release.",
    )
    parser.add_argument("--staging_commit", required=True, help="staging commit that we want to promote to release.")

    args = parser.parse_args()
    download_artifacts_from_s3(args.release_version, args.staging_commit)
    arcs = create_tarballs()
    # promote to release s3 bucket
    print(f"created archives are {arcs}")
    upload_assets_to_github_release(arcs, args.release_version)


def s3_artifacts_path_to_local_path(commit_sha: str):
    return {
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_darwin_amd64_v1/": "kubectl-mongodb_darwin_amd64_v1",
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_darwin_arm64/": "kubectl-mongodb_darwin_arm64",
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_linux_amd64_v1/": "kubectl-mongodb_linux_amd64_v1",
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_linux_arm64/": "kubectl-mongodb_linux_arm64",
    }


def download_artifacts_from_s3(release_version: str, commit_sha: str):
    print(f"Starting download of artifacts from S3 bucket: {build_kubectl_plugin.DEV_S3_BUCKET_NAME}")

    try:
        s3_client = boto3.client("s3", region_name=build_kubectl_plugin.AWS_REGION)
    except (NoCredentialsError, PartialCredentialsError):
        print("ERROR: AWS credentials were not set.")
        sys.exit(1)
    except Exception as e:
        print(f"An error occurred connecting to S3: {e}")
        sys.exit(1)

    artifacts_to_promote = s3_artifacts_path_to_local_path(commit_sha)

    # Create the local temporary directory if it doesn't exist
    os.makedirs(LOCAL_ARTIFACTS_DIR, exist_ok=True)

    download_count = 0
    for s3_artifact_dir, local_subdir in artifacts_to_promote.items():
        print(f"Copying from s3://{build_kubectl_plugin.DEV_S3_BUCKET_NAME}/{s3_artifact_dir} to {local_subdir}/")
        try:
            paginator = s3_client.get_paginator("list_objects_v2")
            pages = paginator.paginate(Bucket=build_kubectl_plugin.DEV_S3_BUCKET_NAME, Prefix=s3_artifact_dir)

            for page in pages:
                if "Contents" not in page:
                    continue
                for obj in page["Contents"]:
                    s3_key = obj["Key"]
                    if s3_key.endswith("/"):
                        continue

                    # Get the path of the file relative to its S3 prefix, this would mostly be the object name itself
                    # if s3_artifact_dir doesn't container directories and has just the objects.
                    relative_path = os.path.relpath(s3_key, s3_artifact_dir)

                    final_local_path = os.path.join(LOCAL_ARTIFACTS_DIR, local_subdir, relative_path)

                    # Create the local directory structure if it doesn't exist
                    os.makedirs(os.path.dirname(final_local_path), exist_ok=True)

                    print(f"Downloading {s3_key} to {final_local_path}")
                    s3_client.download_file(build_kubectl_plugin.DEV_S3_BUCKET_NAME, s3_key, final_local_path)
                    download_count += 1

        except ClientError as e:
            print(f"ERROR: Failed to list or download from prefix '{s3_artifact_dir}'. S3 Client Error: {e}")
            return False

    print("All the artifacts have been downloaded successfully.")
    return True


def create_tarballs():
    print(f"Creating archives for subdirectories in {LOCAL_ARTIFACTS_DIR}")
    created_archives = []
    original_cwd = os.getcwd()
    try:
        os.chdir(LOCAL_ARTIFACTS_DIR)

        for dir_name in os.listdir("."):
            if os.path.isdir(dir_name):
                archive_name = f"{dir_name}.tar.gz"

                with tarfile.open(archive_name, "w:gz") as tar:
                    tar.add(dir_name)

                full_archive_path = os.path.join(original_cwd, LOCAL_ARTIFACTS_DIR, archive_name)
                print(f"Successfully created archive at {full_archive_path}")
                created_archives.append(full_archive_path)

    except Exception as e:
        print(f"ERROR: Failed to create tar.gz archives: {e}")
        return []
    finally:
        os.chdir(original_cwd)

    return created_archives


def upload_assets_to_github_release(asset_paths, release_version: str):
    if not GITHUB_TOKEN:
        print("ERROR: GITHUB_TOKEN environment variable not set.")
        sys.exit(1)

    try:
        g = Github(GITHUB_TOKEN)
        repo = g.get_repo(GITHUB_REPO)
    except GithubException as e:
        print(f"ERROR: Could not connect to GitHub or find repository '{GITHUB_REPO}', Error {e}.")
        sys.exit(1)

    try:
        release = repo.get_release(release_version)
    except GithubException as e:
        print(
            f"ERROR: Could not find release with tag '{release_version}'. Please ensure release exists already. Error: {e}"
        )
        return

    for asset_path in asset_paths:
        asset_name = os.path.basename(asset_path)
        print(f"Uploading artifact '{asset_name}' to github release as asset")
        try:
            release.upload_asset(path=asset_path, name=asset_name, content_type="application/gzip")
        except GithubException as e:
            print(f"ERROR: Failed to upload asset {asset_name}. Error: {e}")
        except Exception as e:
            print(f"An unexpected error occurred during upload of {asset_name}: {e}")


if __name__ == "__main__":
    main()
