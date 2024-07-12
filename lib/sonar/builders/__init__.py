class SonarBuildError(Exception):
    """Wrapper over docker.errors.BuildError"""

    pass


class SonarAPIError(Exception):
    """Wrapper over docker.errors.APIError"""

    pass


def buildarg_from_dict(args):
    if args is None:
        return ""

    return " ".join(["--build-arg {}={}".format(k, v) for k, v in args.items()])


def labels_from_dict(args):
    if args is None:
        return ""

    return " ".join(["--label {}={}".format(k, v) for k, v in args.items()])
