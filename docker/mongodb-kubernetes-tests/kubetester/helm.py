import glob
import os
import re
import subprocess
import uuid
from typing import Dict, List, Optional, Tuple

from kubetester.consts import *
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

# LOCAL_CRDs_DIR is the dir where local helm chart's CRDs are copied in tests image
LOCAL_CRDs_DIR = "helm_chart/crds"
OCI_HELM_REGISTRY_ECR = "268558157000.dkr.ecr.us-east-1.amazonaws.com"


def helm_template(
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    templates: Optional[str] = None,
    helm_options: Optional[List[str]] = None,
) -> str:
    """generates yaml file using Helm and returns its name. Provide 'templates' if you need to run
    a specific template from the helm chart"""
    command_args = _create_helm_args(helm_args, helm_options)

    if templates is not None:
        command_args.append("--show-only")
        command_args.append(templates)

    args = ("helm", "template", *command_args, _helm_chart_dir(helm_chart_path))
    logger.info(" ".join(args))

    yaml_file_name = "{}.yaml".format(str(uuid.uuid4()))
    with open(yaml_file_name, "w") as output:
        process_run_and_check(" ".join(args), stdout=output, check=True, shell=True)

    return yaml_file_name


def helm_install(
    name: str,
    namespace: str,
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    helm_options: Optional[List[str]] = None,
    custom_operator_version: Optional[str] = None,
):
    command_args = _create_helm_args(helm_args, helm_options)
    args = [
        "helm",
        "upgrade",
        "--install",
        f"--namespace={namespace}",
        *command_args,
        name,
        _helm_chart_dir(helm_chart_path),
    ]
    if custom_operator_version:
        args.append(f"--version={custom_operator_version}")
    logger.info(f"Running helm install command: {' '.join(args)}")

    process_run_and_check(" ".join(args), check=True, capture_output=True, shell=True)


def helm_install_from_chart(
    namespace: str,
    release: str,
    chart: str,
    version: str = "",
    custom_repo: Tuple[str, str] = ("stable", "https://charts.helm.sh/stable"),
    helm_args: Optional[Dict[str, str]] = None,
    override_path: Optional[str] = None,
):
    """Installs a helm chart from a repo. It can accept a new custom_repo to add before the
    chart is installed. Also, `helm_args` accepts a dictionary that will be passed as --set
    arguments to `helm install`.

    Some charts are clusterwide (like CertManager), and simultaneous installation can
    fail. This function tolerates errors when installing the Chart if `stderr` of the
    Helm process has the "release: already exists" string on it.
    """

    args = [
        "helm",
        "upgrade",
        "--install",
        release,
        f"--namespace={namespace}",
        chart,
    ]

    if override_path is not None:
        args.extend(["-f", f"{override_path}"])

    if version != "":
        args.append("--version=" + version)

    if helm_args is not None:
        args += _create_helm_args(helm_args)

    helm_repo_add(custom_repo[0], custom_repo[1])

    try:
        # In shared clusters (Kops: e2e) multiple simultaneous cert-manager
        # installations will fail. We tolerate errors in those cases.
        process_run_and_check(args, capture_output=True)
    except subprocess.CalledProcessError as exc:
        stderr = exc.stderr.decode("utf-8")
        if "release: already exists" in stderr or "Error: UPGRADE FAILED: another operation" in stderr:
            logger.info(f"Helm chart '{chart}' already installed in cluster.")
        else:
            raise


def helm_repo_add(repo_name: str, url: str):
    """
    Adds a new repo to Helm.
    """
    helm_repo = f"helm repo add {repo_name} {url}".split()
    logger.info(helm_repo)
    process_run_and_check(helm_repo, capture_output=True)


def process_run_and_check(args, **kwargs):
    if "check" not in kwargs:
        kwargs["check"] = True

    try:
        logger.debug(f"subprocess.run: {args}")
        completed_process = subprocess.run(args, **kwargs)
        # always print process output
        if completed_process.stdout is not None:
            stdout = completed_process.stdout.decode("utf-8")
            logger.debug(f"stdout: {stdout}")
        if completed_process.stderr is not None:
            stderr = completed_process.stderr.decode("utf-8")
            logger.debug(f"stderr: {stderr}")
            completed_process.check_returncode()
    except subprocess.CalledProcessError as exc:
        if exc.stdout is not None:
            stdout = exc.stdout.decode("utf-8")
            logger.error(f"stdout: {stdout}")
        if exc.stderr is not None:
            stderr = exc.stderr.decode("utf-8")
            logger.error(f"stderr: {stderr}")
        logger.error(f"output: {exc.output}")
        raise


def helm_registry_login_to_ecr(helm_registry: str, region: str):
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


def helm_upgrade(
    name: str,
    namespace: str,
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    helm_options: Optional[List[str]] = None,
    helm_override_path: Optional[bool] = False,
    custom_operator_version: Optional[str] = None,
    apply_crds_first: bool = False,
):
    if not helm_chart_path:
        logger.warning("Helm chart path is empty, defaulting to 'helm_chart'")
        helm_chart_path = "helm_chart"

    chart_dir = helm_chart_path if helm_override_path else _helm_chart_dir(helm_chart_path)

    if apply_crds_first:
        apply_crds_from_chart(LOCAL_CRDs_DIR)

    command_args = _create_helm_args(helm_args, helm_options)
    args = [
        "helm",
        "upgrade",
        "--install",
        f"--namespace={namespace}",
        *command_args,
        name,
    ]

    if custom_operator_version:
        args.append(f"--version={custom_operator_version}")

    args.append(chart_dir)

    command = " ".join(args)
    process_run_and_check(command, check=True, capture_output=True, shell=True)


# oci_chart_info returns the respective registry/repo and region information
# based on the build scenario (dev/staging) tests are being run in. These are
# read from build_info.json and then set to the tests image as env vars.
def oci_chart_info():
    registry = os.environ.get(OCI_HELM_REGISTRY_ENV_VAR_NAME)
    repository = os.environ.get(OCI_HELM_REPOSITORY_ENV_VAR_NAME)
    region = os.environ.get(OCI_HELM_REGION_ENV_VAR_NAME)

    logger.info(f"oci chart details in test image is registry {registry}, repo {repository}, region {region}")

    return registry, f"{repository}/mongodb-kubernetes", region


def apply_crds_from_chart(crds_dir: str):
    crd_files = glob.glob(os.path.join(crds_dir, "*.yaml"))

    if not crd_files:
        raise Exception(f"No CRD files found in chart directory: {crds_dir}")

    logger.info(f"Found {len(crd_files)} CRD files to apply:")

    for crd_file in crd_files:
        logger.info(f"Applying CRD from file: {crd_file}")
        args = ["kubectl", "apply", "-f", crd_file]
        process_run_and_check(" ".join(args), check=True, capture_output=True, shell=True)


def helm_uninstall(name):
    args = ("helm", "uninstall", name)
    logger.info(args)
    process_run_and_check(" ".join(args), check=True, capture_output=True, shell=True)


def _create_helm_args(helm_args: Dict[str, str], helm_options: Optional[List[str]] = None) -> List[str]:
    command_args = []
    for key, value in helm_args.items():
        command_args.append("--set")

        if "," in value:
            # helm lists are defined with {<list>}, hence matching this means we don't have to escape.
            if not re.match("^{.+}$", value):
                # Commas in values, but no lists, should be escaped
                value = value.replace(",", "\,")

            # and when commas are present, we should quote "key=value"
            key = '"' + key
            value = value + '"'

        command_args.append("{}={}".format(key, value))

    if "useRunningOperator" in helm_args:
        logger.info("Operator will not be installed this time, passing --dry-run")
        command_args.append("--dry-run")

    command_args.append("--create-namespace")

    if helm_options:
        command_args.extend(helm_options)

    return command_args


def _helm_chart_dir(default: Optional[str] = "helm_chart") -> str:
    return os.environ.get("HELM_CHART_DIR", default)


# helm_chart_path_and_version returns the chart path and version that we would like to install to run the E2E tests.
# for local tests it returns early with local helm chart dir and for other scenarios it figures out the chart and version
# based on the caller. In most of the cases we will install chart from OCI registry but for the tests where we would like
# to install MEKO's specific version or MCK's specific version, we would expect `helm_chart_path` to set already.
def helm_chart_path_and_version(helm_chart_path: str, operator_version: str) -> tuple[str, str]:
    # these are imported here to resolve import cycle issue
    from tests.conftest import LOCAL_HELM_CHART_DIR, local_operator

    if local_operator():
        return LOCAL_HELM_CHART_DIR, ""

    # helm_chart_path not being passed would mean we would like to install helm chart from OCI registry.
    if not helm_chart_path:
        # If operator_version is not passed, we want to install the current version.
        if not operator_version:
            operator_version = os.environ.get(OCI_HELM_VERSION)

        registry, repository, region = oci_chart_info()
        # If ECR we need to login first to the OCI container registry
        if registry == OCI_HELM_REGISTRY_ECR:
            try:
                helm_registry_login_to_ecr(registry, region)
            except Exception as e:
                raise Exception(f"Failed to login to ECR helm registry {registry}. Error: {e}")

        # figure out the registry URI, based on dev/staging scenario
        chart_uri = f"oci://{registry}/{repository}"
        helm_chart_path = chart_uri

    return helm_chart_path, operator_version
