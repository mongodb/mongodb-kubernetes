import re
from typing import Any

import ruamel.yaml


def update_all_helm_values_files(chart_key: str, new_release: str):
    """Updates all values.yaml files setting chart_key.'version' field to new_release"""
    update_single_helm_values_file("helm_chart/values.yaml", key=chart_key, new_release=new_release)


def update_single_helm_values_file(values_yaml_path: str, key: str, new_release: str):
    yaml = ruamel.yaml.YAML()
    with open(values_yaml_path, "r") as fd:
        doc = yaml.load(fd)
    doc[key]["version"] = new_release
    # Make sure we are writing a valid values.yaml file.
    assert "operator" in doc
    assert "registry" in doc
    with open(values_yaml_path, "w") as fd:
        yaml.dump(doc, fd)
    print(f'Set "{values_yaml_path} {key}.version to {new_release}"')


def set_value_in_doc(yaml_doc: Any, dotted_path: str, new_value: Any):
    """Sets the value at the given dotted path in the given yaml document."""

    path = dotted_path.split(".")
    doc = yaml_doc
    for key in path[:-1]:
        doc = doc[key]
    doc[path[-1]] = new_value


def get_value_in_doc(yaml_doc: Any, dotted_path: str):
    """Gets the value at the given dotted path in the given yaml document."""

    path = dotted_path.split(".")
    doc = yaml_doc
    for key in path[:-1]:
        doc = doc[key]
    return doc[path[-1]]


def set_value_in_yaml_file(yaml_file_path: str, key: str, new_value: Any, preserve_quotes: bool = False):
    """Sets one value under key in yaml_file. Key could be passed as a dotted path, e.g. relatedImages.mongodb."""

    yaml = ruamel.yaml.YAML()
    if preserve_quotes:
        yaml.preserve_quotes = True

    with open(yaml_file_path, "r") as fd:
        doc = yaml.load(fd)

    set_value_in_doc(doc, key, new_value)

    with open(yaml_file_path, "w") as fd:
        yaml.dump(doc, fd)

    print(f'Setting in "{yaml_file_path} value {key}')


def get_value_in_yaml_file(yaml_file_path: str, key: str):

    yaml = ruamel.yaml.YAML()
    with open(yaml_file_path, "r") as fd:
        doc = yaml.load(fd)

    return get_value_in_doc(doc, key)


def update_community_agent_image_in_file(yaml_file_path: str, new_version: str):
    """Updates MDB_COMMUNITY_AGENT_IMAGE and AGENT_IMAGE env vars in Kubernetes Deployment manifests."""
    yaml = ruamel.yaml.YAML()
    yaml.explicit_start = True
    yaml.preserve_quotes = True
    yaml.width = 4096

    with open(yaml_file_path, "r") as fd:
        docs = list(yaml.load_all(fd))

    updated = False
    for doc in docs:
        if doc is None or doc.get("kind") != "Deployment":
            continue
        containers = doc.get("spec", {}).get("template", {}).get("spec", {}).get("containers", [])
        for container in containers:
            for env in container.get("env", []):
                if env.get("name") in ("MDB_COMMUNITY_AGENT_IMAGE", "AGENT_IMAGE"):
                    registry = env["value"].rsplit(":", 1)[0]
                    new_value = f"{registry}:{new_version}"
                    if env["value"] != new_value:
                        env["value"] = new_value
                        updated = True

    if updated:
        with open(yaml_file_path, "w") as fd:
            yaml.dump_all(docs, fd)
        print(f"Updated community agent image to {new_version} in {yaml_file_path}")


def update_community_agent_image_in_go_file(go_file_path: str, new_version: str):
    """Updates the hardcoded mongodb-agent default version in a Go source file."""
    pattern = re.compile(r"(quay\.io/mongodb/mongodb-agent:)\d+\.\d+\.\d+\.\d+-\d+")
    with open(go_file_path, "r") as fd:
        content = fd.read()
    new_content, count = pattern.subn(rf"\g<1>{new_version}", content)
    if count:
        with open(go_file_path, "w") as fd:
            fd.write(new_content)
        print(f"Updated community agent image to {new_version} in {go_file_path}")
