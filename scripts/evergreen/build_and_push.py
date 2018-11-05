#!/usr/bin/env python3

'''
Builds and pushes operator & database image to Quay.io.
If docker file is not located in the directory named the same as image specify the path to it using "--path" parameter
(the location must be relative to "docker" directory)
Docker arguments are passed as string in format "key1=val1,key2=val2"

Usage:
    build_and_push.py --image IMAGE --tag TAG [--registry REGISTRY  --path PATH --docker-args DOCKER_ARGS]

'''

import docker
import docopt
import os
import subprocess

registries = {
    'production': 'quay.io/mongodb',
    'development': '268558157000.dkr.ecr.us-east-1.amazonaws.com/dev'
}


def get_registry(name):
    return registries.get(name, 'development')


def image_directories(path):
    if os.getcwd().split('/')[-1] == 'ops-manager-kubernetes':
        return os.path.join('docker', path)

    raise ValueError('Should be run from root of repo.')
    # return 'src/github.com/10gen/ops-manager-kubernetes/docker/{}'.format(image)


def get_client():
    return docker.from_env()


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
    if registry == 'production':
        return get_quay_public_creds()
    elif registry == 'development':
        return get_aws_creds()

    raise ValueError('Allowed values are {}'.format(', '.join(registries.keys())))


def name_for_image(image, tag):
    tag_colon = '' if tag is None or tag == "" else ':' + str(tag)
    return '{}{}'.format(image, tag_colon)


def parse_docker_args(docker_args):
    if not docker_args:
        return {}
    return {k.strip(): v.strip() for k, v in
            [option.split("=") for option in docker_args.split(",")]}


def build_image(image, tag, path_to_image, docker_args):
    client = get_client()
    tagged_image = name_for_image(image, tag)
    if path_to_image == "":
        path_to_image = image
    client.images.build(path=image_directories(path_to_image), tag=tagged_image,
                        buildargs=parse_docker_args(docker_args))


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


def main(args):
    image = args['IMAGE']
    path = args['PATH']
    tag = args['TAG']
    docker_args = args['DOCKER_ARGS']
    print(os.getenv("QUAY_PROD_USER"))
    print(os.getenv("QUAY_PROD_PASSWORD"))

    registry = args.get('REGISTRY', 'development')

    print('Script arguments: {}'.format(args))

    repo = get_registry(registry)

    creds = get_credentials(registry)

    image_with_tag = name_for_image(image, tag)

    print('Building: {}'.format(image_with_tag))
    build_image(image, tag, path, docker_args)

    print('Tagging: {}/{}'.format(repo, image_with_tag))
    tag_image(image, tag, repo)

    print('Pushing: {}/{}'.format(repo, image_with_tag))

    output = push_image(image, tag, repo, creds)

    print(output)

    # For some reasons push_image doesn't through the error but only returns it in the format
    # {"errorDetail":{"message":"name unknown: The repository with name 'dev/mongodb-enterprise' does not exist in the
    # registry with id '268558157000'"}...
    if "errorDetail" in output:
        raise RuntimeError("There was error pushing image")


if __name__ == '__main__':
    args = docopt.docopt(__doc__)
    main(args)
