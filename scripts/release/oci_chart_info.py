import argparse
import json
from dataclasses import asdict

from scripts.release.argparse_utils import get_scenario_from_arg
from scripts.release.build.build_info import load_build_info
from scripts.release.build.build_scenario import SUPPORTED_SCENARIOS, BuildScenario


def main():
    parser = argparse.ArgumentParser(
        description="""Dump the Helm chart information for the 'mongodb-kubernetes' chart as JSON.""",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "-b",
        "--build-scenario",
        metavar="",
        action="store",
        required=True,
        type=str,
        choices=SUPPORTED_SCENARIOS,
        help=f"""Build scenario when reading configuration from 'build_info.json'.
    Options: {", ".join(SUPPORTED_SCENARIOS)}. For '{BuildScenario.DEVELOPMENT}' the '{BuildScenario.PATCH}' scenario is used to read values from 'build_info.json'""",
    )
    args = parser.parse_args()

    build_scenario = get_scenario_from_arg(args.build_scenario)
    build_info = load_build_info(build_scenario)
    chart_info = build_info.helm_charts["mongodb-kubernetes"]

    j = json.dumps(asdict(chart_info))
    print(j)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        raise Exception(f"Failed while dumping the chart_info as json. Error: {e}")
