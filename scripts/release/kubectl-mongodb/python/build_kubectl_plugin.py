import os
import subprocess
import sys

import boto3
from botocore.exceptions import ClientError, NoCredentialsError, PartialCredentialsError
from mypy.types import ExtraAttrs

# from lib.base_logger import logger

DEV_S3_BUCKET_NAME = "mongodb-kubernetes-dev"
STAGING_S3_BUCKET_NAME = "mongodb-kubernetes-staging"
RELEASE_S3_BUCKET_NAME = "mongodb-kubernetes-release"

AWS_REGION = "eu-north-1"
S3_BUCKET_KUBECTL_PLUGIN_SUBPATH = "kubectl-mongodb"

COMMIT_SHA_ENV_VAR = "github_commit"

GORELEASER_DIST_DIR = "dist"

# LOCAL_FILE_PATHis the full filename where tests image expects the kuebctl-mongodb binary to be available
LOCAL_FILE_PATH = "docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_linux"

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
        print("ERROR: 'goreleaser' command not found. Please ensure goreleaser is installed and in your system's PATH.")
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

            print(f"Uploading artifact {local_path} to s3://{DEV_S3_BUCKET_NAME}/{s3_key}")

            stat = os.stat(local_path)
            permissions = str(oct(stat.st_mode)[-3:])

            try:
                s3_client.upload_file(local_path, DEV_S3_BUCKET_NAME, s3_key, ExtraArgs={
                    "Metadata":{
                        "posix-permissions": permissions
                    },
                })
                print(f"Successfully uploaded the artifact {filename}")
                uploaded_files += 1
            except Exception as e:
                print(f"ERROR: Failed to upload file {filename}: {e}")

    if uploaded_files > 0:
        print(f"Successfully uploaded {uploaded_files} kubectl-mongodb plugin artifacts to S3.")


# s3_path returns the path where the artifacts should be uploaded to in S3 obect store.
# For dev workflows it's going to be `kubectl-mongodb/{evg-patch-id}/{goreleaser-artifact}`,
# for staging workflows it would be `kubectl-mongodb/{commit-sha}/{goreleaser-artifact}`.
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

    plugin_path = f"{S3_BUCKET_KUBECTL_PLUGIN_SUBPATH}/{commit_sha}/dist/kubectl-mongodb_linux_amd64_v1/kubectl-mongodb"

    print(f"Downloading s3://{DEV_S3_BUCKET_NAME}/{plugin_path} to {LOCAL_FILE_PATH}")
    try:
        s3_client.download_file(DEV_S3_BUCKET_NAME, plugin_path, LOCAL_FILE_PATH)
        # change the file's permissions so that it can be executed
        os.chmod(LOCAL_FILE_PATH, 0o755)

        print(f"Successfully downloaded artifact to {LOCAL_FILE_PATH}")
    except ClientError as e:
        if e.response['Error']['Code'] == '404':
            print(f"ERROR: Artifact not found at s3://{DEV_S3_BUCKET_NAME}/{plugin_path} ")
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
