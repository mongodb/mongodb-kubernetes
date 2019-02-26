#!/usr/bin/env python3

"""
Reads the release version for provided application from release.json file.
RELEASE_OBJECT is the name of json key in the file.
Note, that the script must be called from the root of the project

Usage:
    read_release_version.py --release-app RELEASE_OBJECT

"""

import json
import docopt


def read_release_from_file(fname, release_object):
    with open(fname, 'r') as fd:
        content = json.load(fd)

    return content[release_object]


def main(args):
    release_object = args['RELEASE_OBJECT']
    print(read_release_from_file('release.json', release_object))


if __name__ == '__main__':
    args = docopt.docopt(__doc__)
    main(args)
