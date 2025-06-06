import os
import random
import subprocess
import sys
import time
from typing import List, Optional

import requests
from opentelemetry import trace

from lib.base_logger import logger

SIGNING_IMAGE_URI = os.environ.get(
    "SIGNING_IMAGE_URI", "artifactory.corp.mongodb.com/release-tools-container-registry-local/garasign-cosign"
)

RETRYABLE_ERRORS = [500, 502, 503, 504, 429, "timeout", "WARNING"]

TRACER = trace.get_tracer("evergreen-agent")


def is_retryable_error(stderr: str) -> bool:
    """
    Determines if the error message is retryable.

    :param stderr: The standard error output from the subprocess.
    :return: True if the error is retryable, False otherwise.
    """
    return any(str(error) in stderr for error in RETRYABLE_ERRORS)


@TRACER.start_as_current_span("run_command_with_retries")
def run_command_with_retries(command, retries=6, base_delay=10):
    """
    Runs a subprocess command with retries and exponential backoff.
    6 retries and 10 seconds delays sums up to be around 10 minutes.
    Delays: 10,20,40,80,160,320
    :param command: The command to run.
    :param retries: Number of retries before failing.
    :param base_delay: Base delay in seconds for exponential backoff.
    :raises subprocess.CalledProcessError: If the command fails after retries.
    """
    span = trace.get_current_span()
    span.set_attribute(f"mck.command.command", command)
    for attempt in range(retries):
        try:
            result = subprocess.run(command, capture_output=True, text=True, check=True)
            span.set_attribute(f"mck.command.retries", attempt)
            span.set_attribute(f"mck.command.failure", False)
            span.set_attribute(f"mck.command.result", result.stdout)
            return result
        except subprocess.CalledProcessError as e:
            logger.error(f"Attempt {attempt + 1} failed: {e.stderr}")
            if is_retryable_error(e.stderr):
                logger.error(f"Attempt {attempt + 1} failed with retryable error: {e.stderr}")
                if attempt + 1 < retries:
                    delay = base_delay * (2**attempt) + random.uniform(0, 1)
                    logger.info(f"Retrying in {delay:.2f} seconds...")
                    time.sleep(delay)
                else:
                    logger.error(f"All {retries} attempts failed for command: {command}")
                    span.set_attribute(f"mck.command.failure", "no_retries")
                    raise
            else:
                logger.error(f"Non-retryable error occurred: {e.stderr}")
                span.set_attribute(f"mck.command.failure", e.stderr)
                raise


def mongodb_artifactory_login() -> None:
    command = [
        "docker",
        "login",
        "--password-stdin",
        "--username",
        os.environ.get("ARTIFACTORY_USERNAME", "mongodb-enterprise-kubernetes-operator"),
        "artifactory.corp.mongodb.com/release-tools-container-registry-local/garasign-cosign",
    ]
    subprocess.run(command, input=os.environ["ARTIFACTORY_PASSWORD"].encode("utf-8"), check=True)


def get_ecr_login_password(region: str) -> Optional[str]:
    """
    Retrieves the login password from aws CLI, the secrets need to be stored in ~/.aws/credentials or equivalent.
    :param region: Registry's AWS region
    :return: The password as a string
    """
    try:
        result = subprocess.run(
            ["aws", "ecr", "get-login-password", "--region", region], capture_output=True, text=True, check=True
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError as e:
        logger.error(f"Failed to get ECR login password: {e.stderr}")
        return None


def is_ecr_registry(image_name: str) -> bool:
    return "amazonaws.com" in image_name


def get_image_digest(image_name: str) -> Optional[str]:
    """
    Retrieves the digest of an image from its tag. Uses the skopeo container to be able to retrieve manifests tags as well.
    :param image_name: The full image name with its tag.
    :return: the image digest, or None in case of failure.
    """

    transport_protocol = "docker://"
    # Get digest
    digest_command = [
        "docker",
        "run",
        "--rm",
        f"--volume={os.path.expanduser('~')}/.aws:/root/.aws:ro",
        "quay.io/skopeo/stable:latest",
        "inspect",
        "--format={{.Digest}}",
    ]

    # Specify ECR credentials if necessary
    if is_ecr_registry(image_name):
        aws_region = os.environ.get("AWS_DEFAULT_REGION", "us-east-1")
        ecr_password = get_ecr_login_password(aws_region)
        digest_command.append(f"--creds=AWS:{ecr_password}")

    digest_command.append(f"{transport_protocol}{image_name}")

    try:
        result = run_command_with_retries(digest_command)
        digest = result.stdout.strip()
        return digest
    except subprocess.CalledProcessError as e:
        logger.error(f"Failed to get digest for {image_name}: {e.stderr}")


def build_cosign_docker_command(additional_args: List[str], cosign_command: List[str]) -> List[str]:
    """
    Common logic to build a cosign command with the garasign cosign image provided by DevProd.
    :param additional_args: additional arguments passed to the docker container, e.g mounted volume or env
    :param cosign_command: actual command executed with cosign such as `sign` or `verify`
    :return: the full command as a List of strings
    """
    home_dir = os.path.expanduser("~")
    base_command = [
        "docker",
        "run",
        "--platform",
        "linux/amd64",
        "--rm",
        f"--volume={home_dir}/.docker/config.json:/root/.docker/config.json:ro",
    ]
    return base_command + additional_args + [SIGNING_IMAGE_URI, "cosign"] + cosign_command


@TRACER.start_as_current_span("sign_image")
def sign_image(repository: str, tag: str) -> None:
    start_time = time.time()
    span = trace.get_current_span()

    image = repository + ":" + tag
    logger.debug(f"Signing image {image}")

    working_directory = os.getcwd()
    container_working_directory = "/usr/local/kubernetes"

    # Referring to the image via its tag is deprecated in cosign
    # We fetch the digest from the registry
    digest = get_image_digest(image)
    if digest is None:
        logger.error("Impossible to get image digest, exiting...")
        sys.exit(1)
    image_ref = f"{repository}@{digest}"

    # Read secrets from environment and put them in env file for container
    grs_username = os.environ["GRS_USERNAME"]
    grs_password = os.environ["GRS_PASSWORD"]
    pkcs11_uri = os.environ["PKCS11_URI"]
    env_file_content = [
        f"GRS_CONFIG_USER1_USERNAME={grs_username}",
        f"GRS_CONFIG_USER1_PASSWORD={grs_password}",
        f"COSIGN_REPOSITORY={repository}",
    ]
    env_file_content = "\n".join(env_file_content)
    temp_file = "./env-file"
    with open(temp_file, "w") as f:
        f.write(env_file_content)

    additional_args = [
        f"--env-file={temp_file}",
        f"--volume={working_directory}:{container_working_directory}",
        f"--workdir={container_working_directory}",
    ]
    cosign_command = [
        "sign",
        f"--key={pkcs11_uri}",
        f"--sign-container-identity={image}",
        f"--tlog-upload=false",
        image_ref,
    ]
    command = build_cosign_docker_command(additional_args, cosign_command)

    try:
        run_command_with_retries(command)
    except subprocess.CalledProcessError as e:
        # Fail the pipeline if signing fails
        logger.error(f"Failed to sign image {image}: {e.stderr}")
        raise

    end_time = time.time()
    duration = end_time - start_time
    span.set_attribute(f"mck.signing.duration", duration)
    span.set_attribute(f"mck.signing.repository", repository)
    logger.debug("Signing successful")


@TRACER.start_as_current_span("verify_signature")
def verify_signature(repository: str, tag: str) -> bool:
    start_time = time.time()
    span = trace.get_current_span()

    image = repository + ":" + tag
    logger.debug(f"Verifying signature of {image}")
    public_key_url = os.environ.get(
        "SIGNING_PUBLIC_KEY_URL", "https://cosign.mongodb.com/mongodb-enterprise-kubernetes-operator.pem"
    )
    r = requests.get(public_key_url)
    # Ensure the request was successful
    if r.status_code == 200:
        # Access the content of the file
        kubernetes_operator_public_key = r.text
    else:
        logger.error(f"Failed to retrieve the public key from {public_key_url}: Status code {r.status_code}")
        return False

    public_key_var_name = "OPERATOR_PUBLIC_KEY"
    additional_args = [
        "--env",
        f"{public_key_var_name}={kubernetes_operator_public_key}",
    ]
    cosign_command = ["verify", "--insecure-ignore-tlog=true", f"--key=env://{public_key_var_name}", image]
    command = build_cosign_docker_command(additional_args, cosign_command)

    try:
        run_command_with_retries(command, retries=10)
    except subprocess.CalledProcessError as e:
        # Fail the pipeline if verification fails
        logger.error(f"Failed to verify signature for image {image}: {e.stderr}")
        raise

    end_time = time.time()
    duration = end_time - start_time
    span.set_attribute(f"mck.verification.duration", duration)
    span.set_attribute(f"mck.verification.repository", repository),
    logger.debug("Successful verification")
