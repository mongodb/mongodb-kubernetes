#!/usr/bin/env python3

'''
Builds and pushes operator & database image to Quay.io.

Usage:
    build_and_release.py (build|push) --image IMAGE --tag-from-file RELEASE_FILE [--registry REGISTRY]
    build_and_release.py (build|push) --image IMAGE --tag TAG [--registry REGISTRY]

'''

import docker
import docopt
import os
import subprocess
import yaml


registries = {
    'production': 'quay.io/mongodb',
    'development': '268558157000.dkr.ecr.us-east-1.amazonaws.com/dev'
}


def get_registry(name):
    return registries.get(name, 'development')


def image_directories(image):
    if os.getcwd().split('/')[-1] == 'ops-manager-kubernetes':
        return 'docker/{}'.format(image)

    raise ValueError('Should be run from root of repo.')
    # return 'src/github.com/10gen/ops-manager-kubernetes/docker/{}'.format(image)


def get_client():
    return docker.from_env()


def get_quay_private_creds():
    # Private repo (mongodb-enterprise-private) is staging
    return os.getenv('QUAY_STAGING_USER'), os.getenv('QUAY_STAGING_PASSWORD')


def get_quay_public_creds():
    # Public repo (mongodb) is production
    return os.getenv('QUAY_PROD_USER'), os.getenv('QUAY_PROD_PASSWORD')


def parse_password_from_docker_login(cmd):
    'Returns a password from a docker login command, present in command after -p'
    if isinstance(cmd, bytes):
        cmd = cmd.decode('utf-8')

    parts = cmd.split()
    return parts[parts.index('-p') + 1]

def get_bin_dir():
    'Returns the bin directory where `aws` client was installed.'
    # TODO: return if not in evergreen

    mci_dir = '/'.join(os.getcwd().split('/')[:4])
    return os.path.join(mci_dir, 'bin')


def get_password_from_aws_cli():
    'Returns a password from the output of aws-cli erc login'

    aws_client = os.path.join(get_bin_dir(), 'aws')
    cli_cmd = '{} ecr get-login --no-include-email --region us-east-1'.format(aws_client)

    result = subprocess.run(cli_cmd.split(), stdout=subprocess.PIPE)
    return parse_password_from_docker_login(result.stdout)


def get_aws_creds():
    # Private ECR is development
    return 'AWS', get_password_from_aws_cli()


def get_credentials(registry):
    if registry == 'staging':
        return get_quay_private_creds()
    elif registry == 'production':
        return get_quay_public_creds()
    elif registry == 'development':
        return get_aws_creds()

    raise ValueError('Allowed values are {}'.format(', '.join(registries.keys())))


def name_for_image(image, tag):
    return '{}:{}'.format(image, tag)


def build_image(image, tag):
    client = get_client()
    tagged_image = name_for_image(image, tag)
    client.images.build(path=image_directories(image), tag=tagged_image)


def tag_image(image, tag, repo):
    client = get_client()
    tagged_image = name_for_image(image, tag)

    img = client.images.get(tagged_image)
    repo = '{}/{}'.format(repo, tagged_image)
    img.tag(repo, tag=tag)


def push_image(image, tag, repo, creds):
    client = get_client()

    creds = dict(username=creds[0], password=creds[1])
    repo = '{}/{}'.format(repo, image)
    return client.images.push(repo, tag=tag, auth_config=creds)


def read_release_from_file(fname):
    with open(fname, 'r') as fd:
        release_doc = yaml.safe_load(fd)

    return release_doc['releaseTag']


def get_release_tag(args):
    'Helper function to read TAG from command line or from file.'
    if 'TAG' in args and args['TAG'] is not None:
        return args['TAG']

    return read_release_from_file(args['RELEASE_FILE'])


def tag_and_push(image, tag, repo, creds):
    print('Pushing {}'.format(image))
    tag_image(image, tag, repo)
    print(push_image(image, tag, repo, creds))


def main(args):
    image = args['IMAGE']

    tag = get_release_tag(args)

    if args['build']:

        print('Building {}'.format(image))
        build_image(image, tag)

    elif args['push']:
        registry = args.get('REGISTRY', 'development')
        repo = get_registry(registry)

        creds = get_credentials(registry)

        print('Pushing: {}/{}:{}'.format(repo, image, tag))
        tag_and_push(image, tag, repo, creds)


if __name__ == '__main__':
    args = docopt.docopt(__doc__)
    main(args)
