"""Tests for scripts/dev/contexts/select_quay_credentials.sh.

Exercises the actual credential-selection helper by sourcing it in bash
with sentinel values. No secrets, no Evergreen calls.
"""

import os
import subprocess
from pathlib import Path
from textwrap import dedent

import pytest

HELPER = Path(__file__).resolve().parents[1] / "contexts" / "select_quay_credentials.sh"

SCRIPT_TEMPLATE = dedent("""\
    #!/usr/bin/env bash
    set -Eeou pipefail
    source "{helper}"
    quay_staging_username="sentinel-staging-user"
    quay_staging_robot_token="sentinel-staging-token"
    quay_prod_username="sentinel-prod-user"
    quay_prod_robot_token="sentinel-prod-token"
    select_quay_credentials "{scenario}"
    echo "$quay_username"
    echo "$quay_password"
    """)


def run_selection(scenario: str) -> tuple[str, str]:
    script = SCRIPT_TEMPLATE.format(helper=HELPER, scenario=scenario)
    out = subprocess_run(script)
    lines = out.strip().splitlines()
    assert len(lines) == 2, f"unexpected output: {out!r}"
    return lines[0], lines[1]


def subprocess_run(script: str) -> str:
    # Strip inherited credential variables (e.g. from Evergreen) so tests
    # only see the values they set themselves.
    clean_env = {
        k: v
        for k, v in os.environ.items()
        if k
        not in (
            "quay_staging_username",
            "quay_staging_robot_token",
            "quay_prod_username",
            "quay_prod_robot_token",
        )
    }
    result = subprocess.run(
        ["bash", "-c", script],
        capture_output=True,
        text=True,
        check=True,
        timeout=30,
        env=clean_env,
    )
    return result.stdout


@pytest.mark.parametrize(
    "scenario, want_user, want_password",
    [
        ("staging", "sentinel-staging-user", "sentinel-staging-token"),
        ("dryrun-release", "sentinel-staging-user", "sentinel-staging-token"),
        ("release", "sentinel-prod-user", "sentinel-prod-token"),
        ("patch", "sentinel-prod-user", "sentinel-prod-token"),
    ],
)
def test_select_quay_credentials(scenario, want_user, want_password):
    user, password = run_selection(scenario)
    assert user == want_user
    assert password == want_password


def test_unset_values_default_to_empty():
    script = dedent(f"""\
        #!/usr/bin/env bash
        set -Eeou pipefail
        source "{HELPER}"
        select_quay_credentials "release"
        echo "[$quay_username|$quay_password]"
        """)
    out = subprocess_run(script).strip()
    assert out == "[|]"
