#!/usr/bin/env python3

'''
Reads the release version for provided application from release.yaml file. RELEASE_OBJECT is the name of yaml key in the file.

Usage:
    read_release_version.py --release-app RELEASE_OBJECT

'''

import yaml
import docopt


def read_release_from_file(fname, release_object):
    with open(fname, 'r') as fd:
        content = yaml.safe_load(fd)

    return content[release_object]

def main(args):
    release_object = args['RELEASE_OBJECT']
    print(read_release_from_file('release.yaml', release_object))

if __name__ == '__main__':
    args = docopt.docopt(__doc__)
    main(args)
