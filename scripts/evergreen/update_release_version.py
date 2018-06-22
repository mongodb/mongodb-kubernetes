#!/usr/bin/env python

'''Applies release version from `.release.yaml` file to relevant files.'''

import re
from build_and_release import read_release_from_file

files = (
    'public/helm_chart/values.yaml',
    'public/mongodb-enterprise.yaml'
)

image_prefix = 'quay.io/mongodb/mongodb-enterprise'
images = ('operator', 'database')

def replace_image_from_file_contents(contents, release):
    for image_type in images:
        image = '{}-{}'.format(image_prefix, image_type)
        new_release = '{}:{}'.format(image, release)
        regex = r'{}:\d+\.\d+(\.\d+)?'.format(image)
        contents = re.sub(regex, new_release, contents)

    return contents

if __name__ == '__main__':
    release = read_release_from_file('.release.yaml')

    for fname in files:
        with open(fname, 'r') as fd:
            contents = fd.read()

        contents = replace_image_from_file_contents(contents, release)

        with open(fname, 'w') as fd:
            fd.write(contents)
