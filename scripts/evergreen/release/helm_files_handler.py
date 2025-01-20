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


def update_standalone_installer(yaml_file_path: str, version: str):
    """
    Updates a bundle of manifests with the correct image version for
    the operator deployment.
    """
    yaml = ruamel.yaml.YAML()

    yaml.explicit_start = True  # Ensure explicit `---` in the output
    yaml.indent(mapping=2, sequence=4, offset=2)  # Align with tab width produced by Helm
    yaml.preserve_quotes = True  # Preserve original quotes in the YAML file

    with open(yaml_file_path, "r") as fd:
        data = list(yaml.load_all(fd))  # Convert the generator to a list

    for doc in data:
        # We're only interested in the Deployments of the operator, where
        # we change the image version to the one provided in the release.
        if doc["kind"] == "Deployment":
            full_image = doc["spec"]["template"]["spec"]["containers"][0]["image"]
            image = full_image.rsplit(":", 1)[0]
            doc["spec"]["template"]["spec"]["containers"][0]["image"] = image + ":" + version

    with open(yaml_file_path, "w") as fd:
        yaml.dump_all(data, fd)
