#!/usr/bin/env python

"""
Performs the update of release fields in all relevant files in the project
Note, that the script must be called from the root of the project

Usage:
    update_release_version.py <version>
"""
import json
import sys

import ruamel.yaml
from read_release_version import read_release_from_file


if __name__ == '__main__':
    new_release = sys.argv[1]
    print("Updating files to the version ", new_release)

    release_json = 'release.json'
    current_release = read_release_from_file(release_json, 'mongodbOperator')

    # TODO implement the proper comparison of versions
    # if current_release >= new_release:
    #     raise Exception("Current release {} is bigger or equal to the new {}!".format(current_release, new_release))

    # 1. update the release.json
    with open(release_json, 'r') as fd:
        doc = json.load(fd)
        doc['mongodbOperator'] = new_release

    with open(release_json, 'w') as fd:
        json.dump(doc, fd, indent=2)
        fd.write("\n")

    print('Updated "{}"'.format(release_json))

    # 2. update the public helm 'values.yaml' file
    values_yaml = 'public/helm_chart/values.yaml'
    yaml = ruamel.yaml.YAML()
    with open(values_yaml, 'r') as fd:
        doc = yaml.load(fd)

    doc['operator']['version'] = new_release

    # Make sure we are writing a valid values.yaml file.
    assert 'createCrds' in doc
    assert 'operator' in doc
    assert 'registry' in doc

    with open(values_yaml, 'w') as fd:
        yaml.dump(doc, fd)
    print('Updated "{}"'.format(values_yaml))

    # 3. update the Chart.yaml
    values_yaml = 'public/helm_chart/Chart.yaml'
    yaml = ruamel.yaml.YAML()
    with open(values_yaml, 'r') as fd:
        doc = yaml.load(fd)

    doc['version'] = new_release

    with open(values_yaml, 'w') as fd:
        yaml.dump(doc, fd)
    print('Updated "{}"'.format(values_yaml))
