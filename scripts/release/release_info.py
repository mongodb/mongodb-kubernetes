import argparse
import json
import pathlib
from typing import Dict

from scripts.release.version import (
    Environment,
)


class ImageInfo(dict):
    def __init__(self, repository: str, platforms: list[str], version: str):
        super().__init__()
        self.repository = repository
        self.platforms = platforms
        self.version = version

    def to_json(self):
        return {"repository": self.repository, "platforms": self.platforms, "version": self.version}


class BinaryInfo(dict):
    def __init__(self, s3_store: str, platforms: list[str], version: str):
        super().__init__()
        self.s3_store = s3_store
        self.platforms = platforms
        self.version = version

    def to_json(self):
        return {"platforms": self.platforms, "version": self.version}


class HelmChartInfo(dict):
    def __init__(self, repository: str, version: str):
        super().__init__()
        self.repository = repository
        self.version = version

    def to_json(self):
        return {"repository": self.repository, "version": self.version}


class BuildInfo(dict):
    def __init__(
        self, images: Dict[str, ImageInfo], binaries: Dict[str, BinaryInfo], helm_charts: Dict[str, HelmChartInfo]
    ):
        super().__init__()
        self.images = images
        self.binaries = binaries
        self.helm_charts = helm_charts

    def __dict__(self):
        return {
            "images": {name: images.__dict__ for name, images in self.images.items()},
            "binaries": {name: bin.__dict__ for name, bin in self.binaries.items()},
            "helm-charts": {name: chart.__dict__ for name, chart in self.helm_charts.items()},
        }

    def to_json(self):
        return {
            "images": {name: images.to_json() for name, images in self.images.items()},
            "binaries": {name: bin.to_json() for name, bin in self.binaries.items()},
            "helm-charts": {name: chart.to_json() for name, chart in self.helm_charts.items()},
        }


def load_build_info(environment: Environment, version: str) -> BuildInfo:
    with open("build_info.json", "r") as f:
        build_info = json.load(f)

    images = {}
    for name, env_data in build_info["images"].items():
        data = env_data[environment]
        # Only update the image_version if it is not already set in the build_info.json file
        image_version = data.get("version")
        if not image_version:
            image_version = version

        images[name] = ImageInfo(repository=data["repository"], platforms=data["platforms"], version=image_version)

    binaries = {}
    for name, env_data in build_info["binaries"].items():
        data = env_data[environment]
        binaries[name] = BinaryInfo(s3_store=data["s3-store"], platforms=data["platforms"], version=version)

    helm_charts = {}
    for name, env_data in build_info["helm-charts"].items():
        data = env_data[environment]
        helm_charts[name] = HelmChartInfo(repository=data["repository"], version=version)

    return BuildInfo(images=images, binaries=binaries, helm_charts=helm_charts)


def create_release_info_json(version: str) -> str:
    build_info = load_build_info(Environment.PROD, version)

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
