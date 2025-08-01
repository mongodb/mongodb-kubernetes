import json
from enum import StrEnum
from typing import Dict


class ImageInfo:
    def __init__(self, repository: str, platforms: list[str], version: str):
        self.repository = repository
        self.platforms = platforms
        self.version = version

    def __json__(self):
        return {"repository": self.repository, "platforms": self.platforms, "version": self.version}


class BinaryInfo:
    def __init__(self, s3_store: str, platforms: list[str], version: str):
        self.s3_store = s3_store
        self.platforms = platforms
        self.version = version

    def __json__(self):
        return {"platforms": self.platforms, "version": self.version}


class HelmChartInfo:
    def __init__(self, repository: str, version: str):
        self.repository = repository
        self.version = version

    def __json__(self):
        return {"repository": self.repository, "version": self.version}


class BuildInfo:
    def __init__(
        self, images: Dict[str, ImageInfo], binaries: Dict[str, BinaryInfo], helm_charts: Dict[str, HelmChartInfo]
    ):
        self.images = images
        self.binaries = binaries
        self.helm_charts = helm_charts

    def __json__(self):
        return {"images": self.images, "binaries": self.binaries, "helm_charts": self.helm_charts}


class Environment(StrEnum):
    DEV = "dev"
    STAGING = "staging"
    PROD = "prod"


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
    data = {
        "images": {name: images.__json__() for name, images in build_info.images.items()},
        "binaries": {name: bin.__json__() for name, bin in build_info.binaries.items()},
        "helm-charts": {name: chart.__json__() for name, chart in build_info.helm_charts.items()},
    }

    return json.dumps(data, indent=2)
