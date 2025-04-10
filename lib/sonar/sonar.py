"""
sonar/sonar.py

Implements Sonar's main functionality.
"""

import json
import os
import re
import subprocess
import tempfile
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from shutil import copyfile
from typing import Any, Dict, List, Optional, Tuple, Union
from urllib.request import urlretrieve

import boto3
import click
import yaml

from lib.base_logger import logger

from . import DCT_ENV_VARIABLE, DCT_PASSPHRASE
from .builders.docker import (
    SonarAPIError,
    docker_build,
    docker_pull,
    docker_push,
    docker_tag,
)
from .template import render


# pylint: disable=R0902
@dataclass
class Context:
    """
    Sonar's Execution Context.

    Holds information required for a run, including execution parameters,
    inventory dictionary, tags (included and excluded).
    """

    inventory: Dict[str, str]
    image: Dict[str, Dict]

    # Store parameters passed as arguments
    parameters: Dict[str, str]

    skip_tags: List[str] = None
    include_tags: List[str] = None

    stage: Dict = None

    # If continue_on_errors is set to true, errors will
    # be captured and logged, but will not raise, and will
    # not stop future tasks to be executed.
    continue_on_errors: bool = True

    # If errors happened during the execution an exception
    # will be raised. This can help on situations were some
    # errors were captured (continue_on_errors == True) but
    # we still want to fail the overall task.
    fail_on_errors: bool = False

    # List of captured errors to report.
    captured_errors: List[Exception] = field(default_factory=list)

    # Defines if running in pipeline mode, this is, the output
    # is supposed to be consumable by the system calling sonar.
    pipeline: bool = False
    output: dict = field(default_factory=dict)

    # Generates a version_id to use if one is not present
    stored_version_id: str = str(uuid.uuid4())

    # stage_outputs is a dictionary of dictionaries. First dict
    # has a key corresponding to the name of the stage, the dict
    # you get from it is key/value (str, Any) with the values
    # stored by given stage.
    stage_outputs: Dict[str, List[Dict[str, Any]]] = field(default_factory=dict)

    # pylint: disable=C0103
    def I(self, string):
        """
        I interpolates variables in string.
        """
        return interpolate_vars(self, string, stage=self.stage)

    @property
    def image_name(self):
        """Returns current image name"""
        return self.image["name"]

    @property
    def version_id(self):
        """Returns the version_id for this run.

        In evergreen context, it corresponds to Evergreen's run version_id, locally
        a uuid is used as a way of having independent builds.
        """
        return os.environ.get("version_id", self.stored_version_id)


def append_output_in_context(ctx: Context, stage_name: str, values: Dict) -> None:
    """Stores a value as the output of the stage, so it can be consumed by future stages."""
    if stage_name not in ctx.stage_outputs.keys():
        # adds a new empty dictionary to this stage
        # if there isn't one yet.
        ctx.stage_outputs[stage_name] = list()

    ctx.stage_outputs[stage_name].append(values)


def find_inventory(inventory: Optional[str] = None):
    """
    Finds the inventory file, and return it as a yaml object.
    """
    if inventory is None:
        inventory = "inventory.yaml"

    # pylint: disable=C0103
    with open(inventory, "r") as f:
        return yaml.safe_load(f)


def find_image(image_name: str, inventory: str):
    """
    Looks for an image of the given name in the inventory.
    """
    for image in find_inventory(inventory)["images"]:
        if image["name"] == image_name:
            return image

    raise ValueError("Image {} not found".format(image_name))


def find_variable_replacement(ctx: Context, variable: str, stage=None) -> str:
    """
    Returns the variable *value* for this varable.
    """
    if variable == "version_id":
        return ctx.version_id

    replacement = None
    # Find variable value on top level file
    if "vars" in ctx.inventory:
        if variable in ctx.inventory["vars"]:
            replacement = ctx.inventory["vars"][variable]

    # Find top-level defined variables overrides,
    # these might not be defined anywhere in the inventory file.
    # maybe they should?
    if variable in ctx.parameters:
        replacement = ctx.parameters[variable]

    # Find variable value on image
    if "vars" in ctx.image:
        if variable in ctx.image["vars"]:
            replacement = ctx.image["vars"][variable]

    # Find variables in stage
    if stage is not None and "vars" in stage:
        if variable in stage["vars"]:
            replacement = stage["vars"][variable]

    # Find variable values on cli parameters
    if "inputs" in ctx.image:
        if variable in ctx.image["inputs"]:
            # If in inputs then we get it form the parameters
            replacement = ctx.parameters[variable]

    return replacement


def find_variable_replacements(ctx: Context, variables: List[str], stage=None) -> Dict[str, str]:
    """
    Finds replacements for a list of variables.
    """
    replacements = {}
    for variable in variables:
        value = find_variable_replacement(ctx, variable, stage)
        if value is None:
            raise ValueError("No value for variable {}".format(variable))

        replacements[variable] = value

    return replacements


def execute_interpolatable_function(name: str) -> str:
    if name == "tempfile":
        tmp = tempfile.mkstemp()
        # mkstemp returns a tuple, with the second element of it being
        # the absolute path to the file.
        return tmp[1]

    raise ValueError("Only supported function is 'tempfile'")


def find_variables_to_interpolate_from_stage(string: str) -> List[Any]:
    """Finds a $(stage['stage-name'].outputs[])"""
    var_finder_re = r"\$\(stages\[\'(?P<stage_name>[\w-]+)\'\]\.outputs\[(?P<index>\d+)\]\.(?P<key>\w+)"

    return re.findall(var_finder_re, string, re.UNICODE)


def find_variables_to_interpolate(string) -> List[str]:
    """
    Returns a list of variables in the string that need to be interpolated.
    """
    var_finder_re = r"\$\(inputs\.params\.(?P<var>\w+)\)"
    return re.findall(var_finder_re, string, re.UNICODE)


def find_functions_to_interpolate(string: str) -> List[Any]:
    """Find functions to be interpolated."""
    var_finder_re = r"\$\(functions\.(?P<var>\w+)\)"

    return re.findall(var_finder_re, string, re.UNICODE)


def interpolate_vars(ctx: Context, string: str, stage=None) -> str:
    """
    For each variable to interpolate in string, finds its *value* and
    replace it in the final string.
    """
    variables = find_variables_to_interpolate(string)
    replacements = find_variable_replacements(ctx, variables, stage)

    for variable in variables:
        string = string.replace("$(inputs.params.{})".format(variable), replacements[variable])

    variables = find_variables_to_interpolate_from_stage(string)
    for stage, index, key in variables:
        value = ctx.stage_outputs[stage][int(index)][key]
        string = string.replace("$(stages['{}'].outputs[{}].{})".format(stage, index, key), value)

    functions = find_functions_to_interpolate(string)
    for name in functions:
        value = execute_interpolatable_function(name)
        string = string.replace("$(functions.{})".format(name), value)

    return string


def build_add_statement(ctx, block) -> str:
    """
    DEPRECATED: do not use
    """
    stmt = "ADD "
    if "from" in block:
        stmt += "--from={} ".format(block["from"])

    src = ctx.I(block["src"])
    dst = ctx.I(block["dst"])
    stmt += "{} {}\n".format(src, dst)

    return stmt


def find_docker_context(ctx: Context):
    """
    Finds a docker context in multiple places in the inventory, image or stage.
    """
    if ctx.stage is not None:
        if "vars" in ctx.stage and "context" in ctx.stage["vars"]:
            return ctx.stage["vars"]["context"]

        if "dockercontext" in ctx.stage:
            return ctx.stage["dockercontext"]

    if "vars" in ctx.image and "context" in ctx.image["vars"]:
        return ctx.image["vars"]["context"]

    raise ValueError("No context defined for image or stage")


def should_skip_stage(stage: Dict[str, str], skip_tags: List[str]) -> bool:
    """
    Checks if this stage should be skipped.
    """
    stage_tags = stage.get("tags", [])
    if len(stage_tags) == 0:
        return False

    return not set(stage_tags).isdisjoint(skip_tags)


def should_include_stage(stage: Dict[str, str], include_tags: List[str]) -> bool:
    """
    Checks if this stage should be included in the run. If tags is empty, then
    all stages should be run, included this one.
    """
    stage_tags = stage.get("tags", [])
    if len(include_tags) == 0:
        # We don't have "include_tags" so all tasks should run
        return True

    return not set(stage_tags).isdisjoint(include_tags)


def task_dockerfile_create(ctx: Context):
    """Writes a simple Dockerfile from SCRATCH and ADD statements. This
    is intended to build a 'context' Dockerfile, this is, a Dockerfile that's
    not runnable but contains data.

    DEPRECATED: Use dockerfile_template or docker_build instead.
    """
    output_dockerfile = ctx.I(ctx.stage["output"][0]["dockerfile"])
    fro = ctx.stage.get("from", "scratch")

    # pylint: disable=C0103
    with open("{}".format(output_dockerfile), "w") as fd:
        fd.write("FROM {}\n".format(fro))
        for f in ctx.stage["static_files"]:
            fd.write(build_add_statement(ctx, f))

    echo(ctx, "dockerfile-save-location", output_dockerfile)


def get_secret(secret_name: str, region: str) -> str:
    session = boto3.session.Session()
    client = session.client(service_name="secretsmanager", region_name=region)

    get_secret_value_response = client.get_secret_value(SecretId=secret_name)

    return get_secret_value_response.get("SecretString", "")


def get_private_key_id(registry: str, signer_name: str) -> str:
    cp = subprocess.run(
        ["docker", "trust", "inspect", registry],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )

    if cp.returncode != 0:
        return SonarAPIError(cp.stderr)

    json_data = json.loads(cp.stdout)
    assert len(json_data) != 0
    for signer in json_data[0]["Signers"]:
        if signer_name == signer["Name"]:
            assert len(signer["Keys"]) != 0
            return signer["Keys"][0]["ID"] + ".key"


def task_tag_image(ctx: Context):
    """
    Pulls an image from source and pushes into destination.
    """
    registry = ctx.I(ctx.stage["source"]["registry"])
    tag = ctx.I(ctx.stage["source"]["tag"])

    image = docker_pull(registry, tag)

    for output in ctx.stage["destination"]:
        registry = ctx.I(output["registry"])
        tag = ctx.I(output["tag"])
        echo(
            ctx,
            "docker-image-push",
            "{}:{}".format(registry, tag),
        )

        docker_tag(image, registry, tag)
        create_ecr_repository(registry)
        try:
            docker_push(registry, tag)
        except SonarAPIError as e:
            ctx.captured_errors.append(e)
            if ctx.continue_on_errors:
                echo(ctx, "docker-image-push/error", e)
            else:
                raise

        append_output_in_context(ctx, ctx.stage["name"], {"registry": registry, "tag": tag})


def get_rendering_params(ctx: Context) -> Dict[str, str]:
    """
    Finds rendering parameters for a template, based on the `inputs` section
    of the stage.
    """
    params = {}
    for param in ctx.stage.get("inputs", {}):
        params[param] = find_variable_replacement(ctx, param, ctx.stage)

    return params


def run_dockerfile_template(ctx: Context, dockerfile_context: str, distro: str) -> str:
    """
    Renders a template and returns a file name pointing at the render.
    """
    path = dockerfile_context
    params = get_rendering_params(ctx)

    logger.debug("rendering params are:")
    logger.debug(params)

    rendered = render(path, distro, params)
    tmp = tempfile.NamedTemporaryFile(delete=False)
    tmp.write(rendered.encode("utf-8"))

    return tmp.name


def interpolate_dict(ctx: Context, args: Dict[str, str]) -> Dict[str, str]:
    """
    Returns a copy of the provided dictionary with their variables interpolated with values.
    """
    copied_args = {}
    # pylint: disable=C0103
    for k, v in args.items():
        copied_args[k] = ctx.I(v)

    return copied_args


def is_valid_ecr_repo(repo_name: str) -> bool:
    """Returns true if repo_name is a ECR repository, it expectes
    a domain part (*.amazonaws.com) and a repository part (/images/container-x/...)."""
    rex = re.compile(r"^[0-9]{10,}\.dkr\.ecr\.[a-z]{2}\-[a-z]+\-[0-9]+\.amazonaws\.com/.+")
    return rex.match(repo_name) is not None


def create_ecr_repository(tag: str):
    """
    Creates ecr repository if it doesn't exist
    """
    if not is_valid_ecr_repo(tag):
        logger.info("Not an ECR repository: %s", tag)
        return

    try:
        no_tag = tag.partition(":")[0]
        region = no_tag.split(".")[3]
        repository_name = no_tag.partition("/")[2]
    except IndexError:
        logger.debug("Could not parse repository: %s", tag)
        return

    logger.debug("Creating repository in %s with name %s", region, repository_name)

    client = boto3.client("ecr", region_name=region)

    try:
        client.create_repository(
            repositoryName=repository_name,
            imageTagMutability="MUTABLE",
            imageScanningConfiguration={"scanOnPush": False},
        )
    except client.exceptions.RepositoryAlreadyExistsException:
        logger.debug("Repository already exists")


def echo(ctx: Context, entry_name: str, message: str, foreground: str = "white"):
    """
    Echoes a message.
    """

    err = ctx.pipeline
    section = ctx.output

    if ctx.pipeline:
        image_name = ctx.image["name"]
        if image_name not in ctx.output:
            ctx.output[image_name] = {}
        section = ctx.output[image_name]

        if ctx.stage is not None:
            stage_name = ctx.stage["name"]
            if stage_name not in ctx.output[image_name]:
                ctx.output[image_name][stage_name] = {}
            section = ctx.output[image_name][stage_name]

        section[entry_name] = message

    stage_title = ""
    if ctx.stage:
        stage_type = ctx.stage["task_type"]
        stage_name = ctx.stage["name"]
        stage_title = "[{}/{}] ".format(stage_name, stage_type)

    # If --pipeline, these messages go to stderr

    click.secho("{}{}: {}".format(stage_title, entry_name, message), fg=foreground, err=err)


def find_dockerfile(dockerfile: str):
    """Returns a Dockerfile file location that can be local or remote. If remote it
    will be downloaded into a temporary location first."""

    if dockerfile.startswith("https://"):
        tmpfile = tempfile.NamedTemporaryFile(delete=False)
        urlretrieve(dockerfile, tmpfile.name)

        return tmpfile.name

    return dockerfile


def is_signing_enabled(output: Dict) -> bool:
    return all(
        key in output
        for key in (
            "signer_name",
            "key_secret_name",
            "passphrase_secret_name",
            "region",
        )
    )


def setup_signing_environment(ctx: Context, output: Dict) -> str:
    os.environ[DCT_ENV_VARIABLE] = "1"
    os.environ[DCT_PASSPHRASE] = get_secret(ctx.I(output["passphrase_secret_name"]), ctx.I(output["region"]))
    # Asks docker trust inspect for the name the private key for the specified signer
    # has to have
    signing_key_name = get_private_key_id(ctx.I(output["registry"]), ctx.I(output["signer_name"]))

    # And writes the private key stored in the secret to the appropriate path
    private_key = get_secret(ctx.I(output["key_secret_name"]), ctx.I(output["region"]))
    docker_trust_path = f"{Path.home()}/.docker/trust/private"
    Path(docker_trust_path).mkdir(parents=True, exist_ok=True)
    with open(f"{docker_trust_path}/{signing_key_name}", "w+") as f:
        f.write(private_key)

    return signing_key_name


def task_docker_build(ctx: Context):
    """
    Builds a container image.
    """
    docker_context = find_docker_context(ctx)

    platform = ctx.image.get("platform")
    if platform:
        platform = ctx.I(platform)

    dockerfile = find_dockerfile(ctx.I(ctx.stage["dockerfile"]))

    buildargs = interpolate_dict(ctx, ctx.stage.get("buildargs", {}))

    labels = interpolate_dict(ctx, ctx.stage.get("labels", {}))

    image = docker_build(docker_context, dockerfile, buildargs=buildargs, labels=labels, platform=platform)

    for output in ctx.stage["output"]:
        registry = ctx.I(output["registry"])
        tag = ctx.I(output["tag"])
        sign = is_signing_enabled(output)
        signing_key_name = ""
        if sign:
            signing_key_name = setup_signing_environment(ctx, output)

        echo(ctx, "docker-image-push", "{}:{}".format(registry, tag))
        docker_tag(image, registry, tag)

        create_ecr_repository(registry)
        try:
            docker_push(registry, tag)
        except SonarAPIError as e:
            ctx.captured_errors.append(e)
            if ctx.continue_on_errors:
                echo(ctx, "docker-image-push/error", e)
            else:
                raise

        append_output_in_context(
            ctx,
            ctx.stage["name"],
            {
                "registry": registry,
                "tag": tag,
            },
        )

        if sign:
            clear_signing_environment(signing_key_name)


def split_s3_location(s3loc: str) -> Tuple[str, str]:
    if not s3loc.startswith("s3://"):
        raise ValueError("{} is not a S3 URL".format(s3loc))

    bucket, _, location = s3loc.partition("s3://")[2].partition("/")

    return bucket, location


def save_dockerfile(dockerfile: str, destination: str):
    if destination.startswith("s3://"):
        client = boto3.client("s3")
        bucket, location = split_s3_location(destination)
        client.upload_file(dockerfile, bucket, location, ExtraArgs={"ACL": "public-read"})
    else:
        copyfile(dockerfile, destination)


def task_dockerfile_template(ctx: Context):
    """
    Templates a dockerfile.
    """
    docker_context = find_docker_context(ctx)
    template_context = docker_context

    try:
        template_context = ctx.image["vars"]["template_context"]
    except KeyError:
        pass

    template_file_extension = ctx.stage.get("template_file_extension")
    if template_file_extension is None:
        # Use distro as compatibility with pre 0.11
        template_file_extension = ctx.stage.get("distro")

    dockerfile = run_dockerfile_template(ctx, template_context, template_file_extension)

    for output in ctx.stage["output"]:
        if "dockerfile" in output:
            output_dockerfile = ctx.I(output["dockerfile"])
            save_dockerfile(dockerfile, output_dockerfile)

            echo(ctx, "dockerfile-save-location", output_dockerfile)

            append_output_in_context(ctx, ctx.stage["name"], {"dockerfile": output_dockerfile})


def find_skip_tags(params: Optional[Dict[str, str]] = None) -> List[str]:
    """Returns a list of tags passed in params that should be excluded from the build."""
    if params is None:
        params = {}

    tags = params.get("skip_tags", [])

    if isinstance(tags, str):
        tags = [t.strip() for t in tags.split(",") if t != ""]

    return tags


def find_include_tags(params: Optional[Dict[str, str]] = None) -> List[str]:
    """Returns a list of tags passed in params that should be included in the build."""
    if params is None:
        params = {}

    tags = params.get("include_tags", [])

    if isinstance(tags, str):
        tags = [t.strip() for t in tags.split(",") if t != ""]

    return tags


def clear_signing_environment(key_to_remove: str):
    # Note that this is not strictly needed
    os.unsetenv(DCT_ENV_VARIABLE)
    os.unsetenv(DCT_PASSPHRASE)
    os.remove(f"{Path.home()}/.docker/trust/private/{key_to_remove}")


# pylint: disable=R0913, disable=R1710
def process_image(
    image_name: str,
    skip_tags: Union[str, List[str]],
    include_tags: Union[str, List[str]],
    build_args: Optional[Dict[str, str]] = None,
    inventory: Optional[str] = None,
    build_options: Optional[Dict[str, str]] = None,
):
    """
    Runs the Sonar process over an image, for an inventory and a set of configurations.
    """
    if build_args is None:
        build_args = {}

    ctx = build_context(image_name, skip_tags, include_tags, build_args, inventory, build_options)

    echo(ctx, "image_build_start", image_name, foreground="yellow")

    for idx, stage in enumerate(ctx.image.get("stages", [])):
        ctx.stage = stage
        name = ctx.stage["name"]
        task = stage["task_type"]
        if should_skip_stage(stage, ctx.skip_tags):
            echo(ctx, "skipping-stage", name, foreground="green")
            continue

        if not should_include_stage(stage, ctx.include_tags):
            echo(ctx, "skipping-stage", name, foreground="green")
            continue

        stages_len = len(ctx.image["stages"])

        echo(ctx, f"stage-started {name} - task-started {task}", f"{idx + 1}/{stages_len}")

        if task == "dockerfile_create":
            task_dockerfile_create(ctx)
        elif task == "dockerfile_template":
            task_dockerfile_template(ctx)
        elif task == "docker_build":
            task_docker_build(ctx)
        elif task == "tag_image":
            task_tag_image(ctx)
        else:
            raise NotImplementedError("task_type {} not supported".format(task))

    if len(ctx.captured_errors) > 0 and ctx.fail_on_errors:
        echo(ctx, "docker-image-push/captured-errors", ctx.captured_errors)
        raise SonarAPIError(ctx.captured_errors[0])

    if ctx.pipeline:
        return ctx.output


def make_list_of_str(value: Union[None, str, List[str]]) -> List[str]:
    """
    Returns a list of strings from multiple different types.
    """
    if value is None:
        return []

    if isinstance(value, str):
        if len(value) == 0:
            return []

        return [e.strip() for e in value.split(",") if e != ""]

    return value


def build_context(
    image_name: str,
    skip_tags: Union[str, List[str]],
    include_tags: Union[str, List[str]],
    build_args: Optional[Dict[str, str]] = None,
    inventory: Optional[str] = None,
    build_options: Optional[Dict[str, str]] = None,
) -> Context:
    """A Context includes the whole inventory, the image to build, the current stage,
    and the `I` interpolation function."""
    image = find_image(image_name, inventory)

    if build_args is None:
        build_args = dict()
    build_args = build_args.copy()
    logger.debug("Should skip tags %s", skip_tags)

    if build_options is None:
        build_options = {}

    context = Context(
        inventory=find_inventory(inventory),
        image=image,
        parameters=build_args,
        skip_tags=make_list_of_str(skip_tags),
        include_tags=make_list_of_str(include_tags),
    )

    for k, v in build_options.items():
        if hasattr(context, k):
            setattr(context, k, v)

    return context
