import os
import sys
import json

from dataclasses import asdict
from lib.base_logger import logger
from scripts.release.build.build_info import load_build_info

def main():
    build_scenario = os.environ.get("BUILD_SCENARIO")
    build_info = load_build_info(build_scenario)
    chart_info = build_info.helm_charts["mongodb-kubernetes"]

    j = json.dumps(asdict(chart_info))
    print(j)



if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        logger.error(f"Failed while dumping the chart_info as json. Error: {e}")
        sys.exit(1)
