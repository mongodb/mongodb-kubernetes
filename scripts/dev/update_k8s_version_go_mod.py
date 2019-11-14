#!/usr/bin/env python3
"""Script to update go libraries by patching go.mod, and then running necessary
commands
"""

import argparse
import logging
import os
import pathlib
import subprocess
import sys
from typing import List, Optional, Tuple

import jinja2

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(filename)s:%(lineno)d - " "%(levelname)s - %(message)s",
)
LOGGER = logging.getLogger()


DO_NOT_MODIFY_WARNING = """// DO NOT MODIFY: AUTO GENERATED FILE
// modify template scripts/dev/go.mod.jinja and run scripts/dev/update_k8s_version_go_mod.py

"""


def get_go_mod_file() -> pathlib.Path:
    "Returns go.mod file"
    current_dir = pathlib.Path().parent.absolute()
    return current_dir.joinpath("go.mod")


def parse_k8s_label(label: str) -> Optional[str]:
    "Returns a k8s label"
    try:
        major, minor, patch = label.split(".")
    except ValueError:
        LOGGER.debug("Label must have 3 parts")
        return None
    try:
        int(major)
        int(minor)
        int(patch)
    except ValueError:
        LOGGER.debug("Versions must be integers")
        return None
    return ".".join((major, minor, patch))


def run_cmd_with_no_goflags(cmd: List[str]) -> bool:
    "Run a shell command without GOFLAGS (useful for `go get` commands)"
    env = os.environ.copy()
    env["GOFLAGS"] = ""
    env["GO111MODULE"] = "on"
    LOGGER.debug("Running `%s`", cmd)
    try:
        subprocess.run(cmd, env=env)
    except subprocess.CalledProcessError as msg:
        LOGGER.error('Failed to run `GOFLAGS="" %s`', cmd)
        LOGGER.exception(msg)
        return False
    return True


def run_cmd(cmd: List[str]) -> bool:
    "Run shell command"
    LOGGER.debug("Running cmd: `%s`", cmd)
    try:
        subprocess.run(cmd)
    except subprocess.CalledProcessError as msg:
        LOGGER.error("Failed to run `%s`", cmd)
        LOGGER.exception(msg)
        return False
    return True


def main() -> int:
    "Main program function"
    description = """This script creates a new go.mod file using the k8s labels, and
    then calls `go get -u=patch` to update the minor patch version of all libs, and
    finally calls `go mod tidy`.
    """
    go_mod_file = get_go_mod_file()
    if not go_mod_file.exists():
        LOGGER.error("This script must be called from a directory with a go.mod file")
        return 1
    parser = argparse.ArgumentParser(description=description)
    parser.add_argument(
        "--debug", "-d", default=False, action="store_true", help="Run in debug mode"
    )
    parser.add_argument(
        "k8s_label", help="Kubernetes label to get k8s libs from, such as 1.15.3",
    )
    args = parser.parse_args()
    if args.debug:
        LOGGER.setLevel(logging.DEBUG)
    k8s_label = parse_k8s_label(args.k8s_label)
    if not k8s_label:
        parser.error("Need to pass a valid k8s label. Got: %s" % args.k8s_label)
        return 1
    LOGGER.info("Setting k8s_label as %s", k8s_label)
    scripts_dev_dir = pathlib.Path(os.path.abspath(__file__)).parent.absolute()
    template_file = scripts_dev_dir.joinpath("go.mod.jinja")
    if not template_file.exists():
        LOGGER.error("Template file %s does not exist", template_file)
        return 1
    LOGGER.info("Using template file: %s", template_file)
    with open(template_file) as template_fh:
        template = jinja2.Template(template_fh.read())
    go_mod_contents = template.render(k8s_label=k8s_label)
    with open(go_mod_file, "w") as go_mod_handle:
        go_mod_handle.write(DO_NOT_MODIFY_WARNING)
        go_mod_handle.write(go_mod_contents)
    # patch version of k8s does not get updated because version is rewritten
    if not run_cmd_with_no_goflags(["go", "get", "-u=patch"]):
        return 1
    # ensure we get an updated copy of the vendor dir
    if not run_cmd(["go", "mod", "vendor"]):
        return 1
    if not run_cmd(["go", "mod", "tidy"]):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
