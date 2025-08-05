import argparse
import json
import pathlib

from scripts.release.build.build_info import load_build_info
from scripts.release.build.build_scenario import BuildScenario
from scripts.release.constants import DEFAULT_REPOSITORY_PATH


def create_release_info_json(repository_path: str) -> str:
    build_info = load_build_info(BuildScenario.RELEASE, repository_path)

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
        "--output",
        "-o",
        metavar="",
        type=pathlib.Path,
        help="Path to save the release information file. If not provided, prints to stdout.",
    )
    args = parser.parse_args()

    release_info = create_release_info_json(args.path)

    if args.output is not None:
        with open(args.output, "w") as file:
            file.write(release_info)
    else:
        print(release_info)
