#!/usr/bin/env python

'''Applies release version from `release.json` file to relevant files.'''

import ruamel.yaml
from read_release_version import read_release_from_file

'''The only file with the release is `values.yaml` from the helm chart.'''
files = (
    'public/helm_chart/values.yaml',
)

if __name__ == '__main__':
    release = read_release_from_file('release.json', 'mongodbOperator')

    for fname in files:
        yaml = ruamel.yaml.YAML()
        with open(fname, 'r') as fd:
            doc = yaml.load(fd)

        doc['operator']['version'] = release

        # Make sure we are writing a valid values.yaml file.
        assert 'createCrds' in doc
        assert 'operator' in doc
        assert 'registry' in doc

        with open(fname, 'w') as fd:
            yaml.dump(doc, fd)
