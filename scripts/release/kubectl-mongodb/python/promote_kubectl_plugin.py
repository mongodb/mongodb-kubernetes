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

    promote_to_release_bucket(args.staging_commit, args.release_version)

    artifacts_tar = create_tarballs()

    upload_assets_to_github_release(artifacts_tar, args.release_version)


def artifacts_source_dir_s3(commit_sha: str):
    return f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/"


def artifacts_dest_dir_s3(release_verion: str):
    return f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{release_verion}/dist/"


def promote_to_release_bucket(commit_sha: str, release_version: str):
    try:
        s3_client = boto3.client("s3", region_name=build_kubectl_plugin.AWS_REGION)
    except (NoCredentialsError, PartialCredentialsError):
        print("ERROR: AWS credentials not found. Please configure AWS credentials.")
        sys.exit(1)
    except Exception as e:
        print(f"An error occurred connecting to S3: {e}")
        sys.exit(1)

    copy_count = 0
    try:
        paginator = s3_client.get_paginator("list_objects_v2")
        pages = paginator.paginate(
            Bucket=build_kubectl_plugin.DEV_S3_BUCKET_NAME, Prefix=artifacts_source_dir_s3(commit_sha)
        )

        for page in pages:
            if "Contents" not in page:
                break

            for obj in page["Contents"]:
                source_key = obj["Key"]

                if source_key.endswith("/"):
                    continue

                # Determine the new key for the destination
                relative_path = os.path.relpath(source_key, artifacts_source_dir_s3(commit_sha))

                destination_dir = artifacts_dest_dir_s3(release_version)
                destination_key = os.path.join(destination_dir, relative_path)

                # Ensure forward slashes for S3 compatibility
                destination_key = destination_key.replace(os.sep, "/")

                print(f"Copying {source_key} to {destination_key}...")

                # Prepare the source object for the copy operation
                copy_source = {"Bucket": build_kubectl_plugin.DEV_S3_BUCKET_NAME, "Key": source_key}

                # Perform the server-side copy
                s3_client.copy_object(
                    CopySource=copy_source, Bucket=build_kubectl_plugin.STAGING_S3_BUCKET_NAME, Key=destination_key
                )
                copy_count += 1

    except ClientError as e:
        print(f"ERROR: A client error occurred during the copy operation. Error: {e}")
        sys.exit(1)
    except Exception as e:
        print(f"An unexpected error occurred: {e}")
        sys.exit(1)

    if copy_count > 0:
        print(f"Successfully copied {copy_count} object(s).")


def s3_artifacts_path_to_local_path(release_version: str, commit_sha: str):
    return {
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_darwin_amd64_v1/": f"kubectl-mongodb_{release_version}_darwin_amd64",
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_darwin_arm64/": f"kubectl-mongodb_{release_version}_darwin_arm64",
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_linux_amd64_v1/": f"kubectl-mongodb_{release_version}_linux_amd64",
        f"{build_kubectl_plugin.S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_linux_arm64/": f"kubectl-mongodb_{release_version}_linux_arm64",
    }


def download_artifacts_from_s3(release_version: str, commit_sha: str):
    print(f"\nStarting download of artifacts from S3 bucket: {build_kubectl_plugin.DEV_S3_BUCKET_NAME}")

    try:
        s3_client = boto3.client("s3", region_name=build_kubectl_plugin.AWS_REGION)
    except (NoCredentialsError, PartialCredentialsError):
        print("ERROR: AWS credentials were not set.")
        sys.exit(1)
    except Exception as e:
        print(f"An error occurred connecting to S3: {e}")
        sys.exit(1)

    artifacts_to_promote = s3_artifacts_path_to_local_path(release_version, commit_sha)

    # Create the local temporary directory if it doesn't exist
    os.makedirs(LOCAL_ARTIFACTS_DIR, exist_ok=True)

    download_count = 0
    for s3_artifact_dir, local_subdir in artifacts_to_promote.items():
        try:
            paginator = s3_client.get_paginator("list_objects_v2")
            pages = paginator.paginate(Bucket=build_kubectl_plugin.DEV_S3_BUCKET_NAME, Prefix=s3_artifact_dir)

            for page in pages:
                # "Contents" corresponds to the directory in the S3 bucket
                if "Contents" not in page:
                    continue
                for obj in page["Contents"]:
                    # obj is the S3 object in page["Contents"] directory
                    s3_key = obj["Key"]
                    if s3_key.endswith("/"):
                        # it's a directory
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
    print(f"\nCreating archives for subdirectories in {LOCAL_ARTIFACTS_DIR}")
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
