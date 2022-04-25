import ruamel.yaml


def update_all_helm_values_files(chart_key: str, new_release: str):
    """ Updates all values.yaml files setting chart_key.'version' field to new_release """
    update_single_helm_values_file(
        "helm_chart/values.yaml", key=chart_key, new_release=new_release
    )
    update_single_helm_values_file(
        "helm_chart/values-openshift.yaml",
        key=chart_key,
        new_release=new_release,
    )


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
