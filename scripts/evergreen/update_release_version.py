#!/usr/bin/env python

'''Applies release version from `release.yaml` file to relevant files.'''

import re
import yaml
from build_and_release import read_release_from_file

'''The only file with the release is `values.yaml` from the helm chart.'''
files = (
    'public/helm_chart/values.yaml',
)

if __name__ == '__main__':
    release = read_release_from_file('release.yaml')

    for fname in files:
        with open(fname, 'r') as fd:
            doc = yaml.load(fd)
            
        doc['operator']['version'] = release

        # Make sure we are writing a valid values.yaml file.
        assert 'createCrds' in doc
        assert 'createNamespace' in doc
        assert 'operator' in doc
        assert 'registry' in doc

        with open(fname, 'w') as fd:
            yaml.dump(doc, fd, default_flow_style=False)
