"""The kind network helpers keep the addresses the e2e tests hardcoded before.

`scripts/funcs/kind_network` (bash) and `docker/mongodb-kubernetes-tests/tests/kind_network.py`
(Python) derive the MetalLB addresses from the same two inputs. With `KIND_NETWORK_SUBNET` and
`KIND_LB_PREFIX` unset, they must give the 172.18.255.x addresses that Evergreen and every plain
checkout used. They must also agree with each other when either variable is set.
"""

import json
import os
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
KIND_NETWORK_PY = REPO / "docker/mongodb-kubernetes-tests/tests/kind_network.py"
KIND_NETWORK_SH = REPO / "scripts/funcs/kind_network"

SLOTS = {"operator": 200, "cluster_1": 210, "cluster_2": 220, "cluster_3": 230, "kind": 240}

PYTHON_PROBE = """
import importlib.util, json
spec = importlib.util.spec_from_file_location("kind_network", {path!r})
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
print(json.dumps({{
    "slots": [module.kind_lb_ip_str(slot) for slot in {slots}],
    "offsets": [module.kind_lb_ip_str(210, 1), module.kind_lb_ip_str(220, 1), module.kind_lb_ip_str(230, 1)],
    "subnet": module.KIND_NETWORK_SUBNET,
}}))
"""

BASH_PROBE = (
    f"source {KIND_NETWORK_SH}; "
    'echo "${KIND_METALLB_RANGE_OPERATOR} ${KIND_METALLB_RANGE_CLUSTER_1} ${KIND_METALLB_RANGE_CLUSTER_2} '
    '${KIND_METALLB_RANGE_CLUSTER_3} ${KIND_METALLB_RANGE_KIND} ${KIND_METALLB_RANGE_SINGLE} ${KIND_LB_PREFIX}"'
)


def clean_env(**extra: str) -> dict:
    env = {k: v for k, v in os.environ.items() if not k.startswith("KIND_")}
    env.update(extra)
    return env


def python_addresses(**env: str) -> dict:
    code = PYTHON_PROBE.format(path=str(KIND_NETWORK_PY), slots=list(SLOTS.values()))
    out = subprocess.run([sys.executable, "-c", code], env=clean_env(**env), capture_output=True, text=True, check=True)
    return json.loads(out.stdout)


def bash_ranges(**env: str) -> dict:
    out = subprocess.run(["bash", "-c", BASH_PROBE], env=clean_env(**env), capture_output=True, text=True, check=True)
    operator, cluster_1, cluster_2, cluster_3, kind, single, prefix = out.stdout.split()
    return {
        "ranges": [operator, cluster_1, cluster_2, cluster_3, kind],
        "single": single,
        "prefix": prefix,
    }


def range_starts(ranges: list) -> list:
    return [r.split("-")[0] for r in ranges]


def test_python_defaults_are_the_172_18_255_literals():
    addresses = python_addresses()

    assert addresses["slots"] == [
        "172.18.255.200",
        "172.18.255.210",
        "172.18.255.220",
        "172.18.255.230",
        "172.18.255.240",
    ]
    assert addresses["offsets"] == ["172.18.255.211", "172.18.255.221", "172.18.255.231"]
    assert addresses["subnet"] == "172.18.0.0/16"


def test_bash_defaults_are_the_172_18_255_ranges_of_the_recreate_scripts():
    ranges = bash_ranges()

    assert ranges["ranges"] == [
        "172.18.255.200-172.18.255.209",
        "172.18.255.210-172.18.255.219",
        "172.18.255.220-172.18.255.229",
        "172.18.255.230-172.18.255.239",
        "172.18.255.240-172.18.255.249",
    ]
    assert ranges["single"] == "172.18.255.200-172.18.255.250"
    assert ranges["prefix"] == "172.18.255"


def test_python_and_bash_agree_without_overrides():
    assert range_starts(bash_ranges()["ranges"]) == python_addresses()["slots"]


def test_kind_network_subnet_alone_moves_the_python_addresses_like_the_bash_ones():
    bash = bash_ranges(KIND_NETWORK_SUBNET="10.9.0.0/16")

    assert range_starts(bash["ranges"]) == python_addresses(KIND_NETWORK_SUBNET="10.9.0.0/16")["slots"]
    assert bash["prefix"] == "10.9.255"


def test_kind_lb_prefix_alone_moves_the_python_addresses_like_the_bash_ones():
    bash = bash_ranges(KIND_LB_PREFIX="172.16.34")

    assert bash["prefix"] == "172.16.34"
    assert range_starts(bash["ranges"]) == python_addresses(KIND_LB_PREFIX="172.16.34")["slots"]
    assert python_addresses(KIND_LB_PREFIX="172.16.34")["slots"][0] == "172.16.34.200"


def test_empty_variables_fall_back_to_the_defaults_on_both_sides():
    bash = bash_ranges(KIND_NETWORK_SUBNET="", KIND_LB_PREFIX="")

    assert bash["prefix"] == "172.18.255"
    assert range_starts(bash["ranges"]) == python_addresses(KIND_NETWORK_SUBNET="", KIND_LB_PREFIX="")["slots"]
