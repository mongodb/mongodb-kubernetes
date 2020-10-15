#!/usr/bin/env python

"""
Performs the update of release fields in all relevant files in the project
Note, that the script must be called from the root of the project

Usage:
    update_release_version.py <version>
"""
import argparse
import json
import sys

import ruamel.yaml
import semver

from read_release_version import read_release_from_file


def update_helm_values(
    values_yaml_path: str,
    new_release: str,
    key: str = "operator",
    value: str = "version",
):
    yaml = ruamel.yaml.YAML()
    with open(values_yaml_path, "r") as fd:
        doc = yaml.load(fd)
    doc[key][value] = new_release
    # Make sure we are writing a valid values.yaml file.
    assert "operator" in doc
    assert "registry" in doc
    with open(values_yaml_path, "w") as fd:
        yaml.dump(doc, fd)
    print('Updated "{}"'.format(values_yaml_path))


def main(argv=None):
    if argv is None:
        argv = sys.argv[1:]
    parser = argparse.ArgumentParser(
        description="Update release.json and helm chart values"
    )
    parser.add_argument("--operator_version")
    parser.add_argument("--init_appdb_version")
    parser.add_argument("--init_opsmanager_version")
    args = parser.parse_args(argv)

    release_json = "release.json"

    for container, release_object, chart_key in (
        ("operator_version", "mongodbOperator", "operator"),
        ("init_appdb_version", "initAppDbVersion", "initAppDb"),
        ("init_opsmanager_version", "initOpsManagerVersion", "initOpsManager"),
    ):
        if not getattr(args, container):
            continue
        new_release = getattr(args, container)
        current_release = read_release_from_file(release_json, release_object)

        if semver.compare(current_release, new_release) == 1:
            raise Exception(
                "Current release {} is bigger than the new {}!".format(
                    current_release, new_release
                )
            )

        print("Updating files to the {} version: {}".format(container, new_release))

        # 1. update the release.json
        with open(release_json, "r") as fd:
            doc = json.load(fd)
            doc[release_object] = new_release

        with open(release_json, "w") as fd:
            json.dump(doc, fd, indent=2)
            fd.write("\n")

        print('Updated "{}"'.format(release_json))

        # 2. update the public helm 'values.yaml' and 'values-openshift.yaml' files
        update_helm_values("public/helm_chart/values.yaml", new_release, key=chart_key)
        update_helm_values(
            "public/helm_chart/values-openshift.yaml", new_release, key=chart_key
        )

        if container == "operator_version":
            # 3. update the Chart.yaml
            values_yaml = "public/helm_chart/Chart.yaml"
            yaml = ruamel.yaml.YAML()
            with open(values_yaml, "r") as fd:
                doc = yaml.load(fd)

            doc["version"] = new_release

            with open(values_yaml, "w") as fd:
                yaml.dump(doc, fd)
            print('Updated "{}"'.format(values_yaml))
    return 0


if __name__ == "__main__":
    sys.exit(main())
