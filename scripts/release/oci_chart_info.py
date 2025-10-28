import argparse
import json
from dataclasses import asdict

from release.build.build_scenario import BuildScenario

from scripts.release.build.build_info import load_build_info


def main():
    supported_scenarios = list(BuildScenario)

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
        choices=supported_scenarios,
        help=f"""Build scenario when reading configuration from 'build_info.json'.
    Options: {", ".join(supported_scenarios)}. For '{BuildScenario.DEVELOPMENT}' the '{BuildScenario.PATCH}' scenario is used to read values from 'build_info.json'""",
    )
    args = parser.parse_args()

    build_scenario = BuildScenario(args.build_scenario)
    build_info = load_build_info(build_scenario)
    chart_info = build_info.helm_charts["mongodb-kubernetes"]

    j = json.dumps(asdict(chart_info))
    print(j)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        raise Exception(f"Failed while dumping the chart_info as json. Error: {e}")
