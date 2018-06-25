#!/usr/bin/env python
"""
Downloads an Automation Agent of a certain version, in one of three ways:
- {filename} release_yaml: retrieves the AA version from the 'release.yaml' file in the current repository
- {filename} repo MMS_REPO COMMIT_HASH: retrieves it from 'mms/server/conf/conf-hosted.properties' at the specified hash
- {filename} custom_version AUTOMATION_AGENT_VERSION: uses the passed parameter to download the appropriate agent

Note: If using the 'repo' form, point COMMIT_HASH to the latest release tag in the MMS repo (e.g., 'on-prem-4.0.0' at the time of this writing)

Usage:
    {filename} release_yaml
    {filename} repo MMS_REPO COMMIT_HASH
    {filename} custom_version AUTOMATION_AGENT_VERSION
"""

import configparser
import os
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import yaml

from os.path import abspath, dirname, isfile
from os.path import join as pjoin
from os.path import basename

import docopt

# Replace current filename in docopt
__doc__ = __doc__.format(filename=basename(__file__))

def git(cmd, path=None):
    old_dir = os.getcwd()
    try:
        os.chdir(path)
        result = subprocess.check_output(shlex.split('git {}'.format(cmd)))
        return result.decode('utf-8').strip(' \n ')
    finally:
        os.chdir(old_dir)

def get_aa_version_from_disk(fname):
    with open(fname, 'r') as fd:
        release_doc = yaml.safe_load(fd)

    return release_doc['automation.agent.version']

def get_aa_version_from_repo(mms_repo, commit_hash):
    git('fetch', path=mms_repo)
    config = git('show {}:server/conf/conf-hosted.properties'.format(commit_hash), path=mms_repo)
    parser = configparser.ConfigParser()
    parser.read_string('[default]' + os.linesep + config)
    return parser.get('default', 'automation.agent.version')

def download(url, path):
    with urllib.request.urlopen(url) as response, open(path, 'wb') as fh:
        shutil.copyfileobj(response, fh)

if __name__ == '__main__':
    args = docopt.docopt(__doc__)

    repo_root = dirname(dirname(dirname(abspath(__file__))))
    release_file = pjoin(repo_root, 'release.yaml')
    destination = pjoin(repo_root, 'docker/mongodb-enterprise-database/content/mongodb-mms-automation-agent')
    version_info = pjoin(repo_root, 'docker/mongodb-enterprise-database/content/mongodb-mms-automation-agent-version.properties')

    if args['release_yaml']:
        aa_version = get_aa_version_from_disk(release_file)
    elif args['repo']:
        aa_version = get_aa_version_from_repo(args['MMS_REPO'], args['COMMIT_HASH'])
    elif args['custom_version']:
        aa_version = args['AUTOMATION_AGENT_VERSION']
    else:
        print("Unsupported mode, check your args!")
        sys.exit(1)

    url_template = 'https://s3.amazonaws.com/mciuploads/mms-automation/mongodb-mms-build-agent/builds/automation-agent/{env}/mongodb-mms-automation-agent-{automation_agent_version}.linux_x86_64.tar.gz'
    url = url_template.format(env='hosted', automation_agent_version=aa_version)

    tmpdir = tempfile.mkdtemp()

    print('Downloading agent tarball from {} ...'.format(url))
    tarball = pjoin(tmpdir, 'tarball.tar.gz')
    download(url, tarball)

    tf = tarfile.open(tarball)
    tf.extractall(path=tmpdir)

    src = pjoin(tmpdir, 'mongodb-mms-automation-agent-{automation_agent_version}.linux_x86_64'.format(automation_agent_version=aa_version), 'mongodb-mms-automation-agent')
    assert isfile(src), 'Could not find agent in tarball!'

    print('Overwriting previous version at {} ...'.format(destination))
    shutil.move(src, destination)

    print('Saving info about the automation agent at {} ...'.format(version_info))
    with open(version_info, 'w') as f:
        f.write('automation.agent.version={}\n'.format(aa_version))
        if args['COMMIT_HASH']:
            f.write('git.latestCommit={}\n'.format(args['COMMIT_HASH']))
