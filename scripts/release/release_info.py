import argparse
import json
import pathlib

from scripts.release.build.build_info import load_build_info
from scripts.release.version import BuildScenario


def create_release_info_json(version: str) -> str:
    build_info = load_build_info(BuildScenario.RELEASE, version)

    return json.dumps(build_info.to_json(), indent=2)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Create relevant release artifacts information in JSON format.",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "-v",
        "--version",
        metavar="",
        action="store",
        type=str,
        help=f"Version to use for this release.",
    )
    parser.add_argument(
        "--output",
        "-o",
        metavar="",
        type=pathlib.Path,
        help="Path to save the release information file. If not provided, prints to stdout.",
    )
    args = parser.parse_args()

    release_info = create_release_info_json(args.version)

    if args.output is not None:
        with open(args.output, "w") as file:
            file.write(release_info)
    else:
        print(release_info)
