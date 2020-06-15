import subprocess
import uuid
from typing import Dict, Optional, List


def helm_template(
    helm_args: Dict,
    helm_chart_name: Optional[str] = "helm_chart",
    templates: Optional[str] = None,
) -> str:
    """ generates yaml file using Helm and returns its name. Provide 'templates' if you need to run
     a specific template from the helm chart """
    command_args = _create_helm_args(helm_args)

    if templates is not None:
        command_args.append("-x")
        command_args.append(templates)

    args = ("helm", "template", *(command_args), helm_chart_name)
    print()
    print(args)

    yaml_file_name = "{}.yaml".format(str(uuid.uuid4()))
    with open(yaml_file_name, "w") as output:
        subprocess.run(args, stdout=output, check=True)
    return yaml_file_name


def helm_install(
    name: str, helm_args: Dict, helm_chart_name: Optional[str] = "helm_chart"
):
    command_args = _create_helm_args(helm_args)
    args = ("helm", "install", *(command_args), name, helm_chart_name)
    print()
    print(args)

    # we use Helm binary installed in the image instead of PyHelm as the latter seems to be quite limited
    # and not active
    subprocess.run(args, check=True)


def helm_upgrade(
    name: str,
    helm_args: Dict,
    install: bool = True,
    helm_chart_name: Optional[str] = "helm_chart",
):
    command_args = _create_helm_args(helm_args)
    if install:
        # the helm chart will be installed if it doesn't exist yet
        command_args.append("--install")
    args = ("helm", "upgrade", *(command_args), name, helm_chart_name)
    print()
    print(args)

    subprocess.run(args, check=True)


def _create_helm_args(helm_args) -> List[str]:
    command_args = []
    for (key, value) in helm_args.items():
        command_args.append("--set")
        command_args.append("{}={}".format(key, value))

    command_args.append("--create-namespace")

    return command_args
