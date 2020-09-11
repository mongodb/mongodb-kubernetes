import subprocess
import uuid
from typing import Dict, Optional, List, Tuple


def helm_template(
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    templates: Optional[str] = None,
    helm_options: Optional[List[str]] = None,
) -> str:
    """ generates yaml file using Helm and returns its name. Provide 'templates' if you need to run
     a specific template from the helm chart """
    command_args = _create_helm_args(helm_args, helm_options)

    if templates is not None:
        command_args.append("--show-only")
        command_args.append(templates)

    args = ("helm", "template", *(command_args), helm_chart_path)
    print()
    print(args)

    yaml_file_name = "{}.yaml".format(str(uuid.uuid4()))
    with open(yaml_file_name, "w") as output:
        subprocess.run(args, stdout=output, check=True)
    return yaml_file_name


def helm_install(
    name: str,
    helm_args: Dict,
    helm_chart_path: Optional[str] = "helm_chart",
    helm_options: Optional[List[str]] = None,
):
    command_args = _create_helm_args(helm_args, helm_options)
    args = ("helm", "install", *(command_args), name, helm_chart_path)
    print()
    print(args)

    subprocess.run(args, check=True)


def helm_install_from_chart(
    namespace: str,
    release: str,
    chart: str,
    version: str = "",
    custom_repo: Tuple[str, str] = (
        "stable",
        "https://kubernetes-charts.storage.googleapis.com",
    ),
    helm_args: Optional[Dict[str, str]] = None,
):
    """Installs a helm chart from a repo. It can accept a new custom_repo to add before the
    chart is installed. Also `helm_args` accepts a dictionary that will be passed as --set
    arguments to `helm install`."""

    args = [
        "helm",
        "upgrade",
        "--install",
        release,
        f"--namespace={namespace}",
        chart,
    ]

    if version != "":
        args.append("--version=" + version)

    if helm_args is not None:
        args += _create_helm_args(helm_args)

    process = "helm repo add {} {}".format(custom_repo[0], custom_repo[1])
    subprocess.run(process.split())
    subprocess.run(args, check=True)


def helm_upgrade(
    name: str,
    helm_args: Dict,
    install: bool = True,
    helm_chart_path: Optional[str] = "helm_chart",
    helm_options: Optional[List[str]] = None,
):
    command_args = _create_helm_args(helm_args, helm_options)
    if install:
        # the helm chart will be installed if it doesn't exist yet
        command_args.append("--install")
    args = ("helm", "upgrade", *(command_args), name, helm_chart_path)
    print()
    print(args)

    subprocess.run(args, check=True)


def helm_uninstall(name):
    args = ("helm", "uninstall", name)
    print()
    print(args)

    subprocess.run(args, check=True)


def _create_helm_args(helm_args, helm_options: Optional[List[str]] = None) -> List[str]:
    command_args = []
    for (key, value) in helm_args.items():
        command_args.append("--set")
        command_args.append("{}={}".format(key, value))

    command_args.append("--create-namespace")

    if helm_options:
        command_args.extend(helm_options)

    return command_args
