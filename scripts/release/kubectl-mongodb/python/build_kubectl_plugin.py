import os
import subprocess
import sys

import boto3
from botocore.exceptions import ClientError, NoCredentialsError, PartialCredentialsError

# from lib.base_logger import logger

S3_BUCKET_NAME = "mongodb-kubernetes-dev"
AWS_REGION = "eu-north-1"
S3_BUCKET_KUBECTL_PLUGIN_SUBPATH = "kubectl-mongodb"

COMMIT_SHA_ENV_VAR = "github_commit"

GORELEASER_DIST_DIR = "dist"


def run_goreleaser():
    try:
        command = ["./goreleaser", "build", "--snapshot", "--clean"]

        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

        for log in iter(process.stdout.readline, ""):
            print(log, end="")

        process.stdout.close()
        exit_code = process.wait()

        if exit_code != 0:
            print(f"GoReleaser command failed with exit code {exit_code}.")
            sys.exit(1)

        print("GoReleaser build completed successfully!")

    except FileNotFoundError:
        print("ERROR: 'goreleaser' command not found. Please ensure GoReleaser is installed and in your system's PATH.")
        sys.exit(1)
    except Exception as e:
        print(f"An unexpected error occurred while running `goreleaser build`: {e}")
        sys.exit(1)


def upload_artifacts_to_s3():
    if not os.path.isdir(GORELEASER_DIST_DIR):
        print(f"ERROR: GoReleaser dist directory '{GORELEASER_DIST_DIR}' not found.")
        sys.exit(1)

    try:
        s3_client = boto3.client("s3", region_name=AWS_REGION)
    except (NoCredentialsError, PartialCredentialsError):
        print("ERROR: Failed to create S3 client. AWS credentials not found.")
        sys.exit(1)
    except Exception as e:
        print(f"An error occurred connecting to S3: {e}")
        sys.exit(1)

    uploaded_files = 0
    for root, _, files in os.walk(GORELEASER_DIST_DIR):
        for filename in files:
            local_path = os.path.join(root, filename)
            s3_key = s3_path(local_path)

            print(f"Uploading artifact {local_path} to s3://{S3_BUCKET_NAME}/{s3_key}")

            try:
                s3_client.upload_file(local_path, S3_BUCKET_NAME, s3_key)
                print(f"Successfully uploaded the artifact {filename}")
                uploaded_files += 1
            except Exception as e:
                print(f"ERROR: Failed to upload file {filename}: {e}")

    if uploaded_files > 0:
        print(f"Successfully uploaded {uploaded_files} kubectl-mongodb plugin artifacts to S3.")


def s3_path(local_path: str):
    commit_sha = os.environ.get(COMMIT_SHA_ENV_VAR, "").strip()
    if commit_sha == "":
        print(f"Error: The commit sha environment variable {COMMIT_SHA_ENV_VAR} is not set. It's required to form the S3 Path.")
        sys.exit(1)

    return f"{S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/{local_path}"


def download_plugin_for_tests_image():
    try:
        s3_client = boto3.client("s3", region_name=AWS_REGION)
    except Exception as e:
        print(f"An error occurred connecting to S3 to download kubectl plugin for tests image: {e}")
        return


    commit_sha = os.environ.get(COMMIT_SHA_ENV_VAR, "").strip()
    if commit_sha == "":
        print("Error: The commit sha environment variable is not set. It's required to form the S3 Path.")
        sys.exit(1)

    local_file_path = "docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_linux"

    bucket_path = f"{S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_linux_amd64_v1/kubectl-mongodb"
    print(f"Downloading s3://{S3_BUCKET_NAME}/{bucket_path} to {local_file_path}")

    try:
        s3_client.download_file(S3_BUCKET_NAME, bucket_path, local_file_path)
        print(f"Successfully downloaded artifact to {local_file_path}")
    except ClientError as e:
        if e.response['Error']['Code'] == '404':
            print(f"ERROR: Artifact not found at s3://{S3_BUCKET_NAME}/{bucket_path} ")
        else:
            print(f"ERROR: Failed to download artifact. S3 Client Error: {e}")
    except Exception as e:
        print(f"An unexpected error occurred during download: {e}")

def main():
    run_goreleaser()
    upload_artifacts_to_s3()

    download_plugin_for_tests_image()


if __name__ == "__main__":
    main()
