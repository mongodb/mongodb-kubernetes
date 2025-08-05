import json
from typing import Dict

from scripts.release.build.build_scenario import BuildScenario
from scripts.release.constants import DEFAULT_REPOSITORY_PATH


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


def load_build_info(scenario: BuildScenario, repository_path: str = DEFAULT_REPOSITORY_PATH) -> BuildInfo:
    version = scenario.get_version(repository_path)

    with open("build_info.json", "r") as f:
        build_info = json.load(f)

    images = {}
    for name, env_data in build_info["images"].items():
        data = env_data[scenario]
        # Only update the image_version if it is not already set in the build_info.json file
        image_version = data.get("version")
        if not image_version:
            image_version = version

        images[name] = ImageInfo(repository=data["repository"], platforms=data["platforms"], version=image_version)

    binaries = {}
    for name, env_data in build_info["binaries"].items():
        data = env_data[scenario]
        binaries[name] = BinaryInfo(s3_store=data["s3-store"], platforms=data["platforms"], version=version)

    helm_charts = {}
    for name, env_data in build_info["helm-charts"].items():
        data = env_data[scenario]
        helm_charts[name] = HelmChartInfo(repository=data["repository"], version=version)

    return BuildInfo(images=images, binaries=binaries, helm_charts=helm_charts)
