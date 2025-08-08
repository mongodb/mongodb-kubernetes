import argparse
import json
import pathlib

from scripts.release.build.build_info import load_build_info
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.constants import (
    DEFAULT_CHANGELOG_PATH,
    DEFAULT_RELEASE_INITIAL_VERSION,
    DEFAULT_REPOSITORY_PATH,
)


def create_release_info_json(
    repository_path: str, changelog_sub_path: str, initial_commit_sha: str = None, initial_version: str = None
) -> str:
    build_info = load_build_info(
        scenario=BuildScenario.RELEASE,
        repository_path=repository_path,
        changelog_sub_path=changelog_sub_path,
        initial_commit_sha=initial_commit_sha,
        initial_version=initial_version,
    )

    return json.dumps(build_info.to_json(), indent=2)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Create relevant release artifacts information in JSON format.",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "-p",
        "--path",
        default=DEFAULT_REPOSITORY_PATH,
        metavar="",
        action="store",
        type=pathlib.Path,
        help="Path to the Git repository. Default is the current directory '.'",
    )
    parser.add_argument(
        "-c",
        "--changelog-path",
        default=DEFAULT_CHANGELOG_PATH,
        metavar="",
        action="store",
        type=str,
        help=f"Path to the changelog directory relative to a current working directory. Default is '{DEFAULT_CHANGELOG_PATH}'",
    )
    parser.add_argument(
        "-s",
        "--initial-commit-sha",
        metavar="",
        action="store",
        type=str,
        help="SHA of the initial commit to start from if no previous version tag is found.",
    )
    parser.add_argument(
        "-v",
        "--initial-version",
        default=DEFAULT_RELEASE_INITIAL_VERSION,
        metavar="",
        action="store",
        type=str,
        help=f"Version to use if no previous version tag is found. Default is '{DEFAULT_RELEASE_INITIAL_VERSION}'",
    )
    parser.add_argument(
        "--output",
        "-o",
        metavar="",
        type=pathlib.Path,
        help="Path to save the release information file. If not provided, prints to stdout.",
    )
    args = parser.parse_args()

    release_info = create_release_info_json(
        args.path, args.changelog_path, args.initial_commit_sha, args.initial_version
    )

    if args.output is not None:
        with open(args.output, "w") as file:
            file.write(release_info)
    else:
        print(release_info)
