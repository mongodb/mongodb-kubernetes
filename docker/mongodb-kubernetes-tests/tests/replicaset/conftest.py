import logging

import pytest

# Suppress DEBUG noise from low-level HTTP/k8s libraries — their response bodies
# clutter the live log output without adding diagnostic value.
logging.getLogger("kubernetes.client.rest").setLevel(logging.WARNING)
logging.getLogger("botocore").setLevel(logging.WARNING)
logging.getLogger("urllib3").setLevel(logging.WARNING)


def pytest_runtest_setup(item):
    """This allows to automatically install the default Operator before running any test"""
    if "default_operator" not in item.fixturenames:
        item.fixturenames.insert(0, "default_operator")


# ── Phase-report hook for failure-detection in autouse diagnostic fixtures ──
# pytest only auto-discovers hooks declared in conftest.py — declaring this in
# a regular test module made it silently inert and caused the Monarch dump
# fixtures to never run their post-yield bodies. Keep it here.
PHASE_REPORT_KEY = pytest.StashKey[dict]()


@pytest.hookimpl(tryfirst=True, hookwrapper=True)
def pytest_runtest_makereport(item, call):
    """Stash each phase's report on the item so autouse fixtures can detect failure."""
    outcome = yield
    rep = outcome.get_result()
    item.stash.setdefault(PHASE_REPORT_KEY, {})[rep.when] = rep
