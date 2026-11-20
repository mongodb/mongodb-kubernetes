"""Tests for scripts/evergreen/decide_release_process.py.

Verifies that new-process generation includes the reused update_docs_repo
variant (defined in .evergreen-snippets.yml) and the deprecated pipeline
does not.
"""

import json
import os
import subprocess
import sys

import pytest

SCRIPT = os.path.join(
    os.path.dirname(os.path.abspath(__file__)),
    "decide_release_process.py",
)


@pytest.fixture
def generated_variants(tmp_path, monkeypatch):
    """Run decide_release_process.py in a tmp dir and return variant names."""

    def run(annotation: str):
        # Symlink the pipeline files into the tmp cwd, mirroring the EVG setup.
        for f in (
            ".evergreen-release-publish.yml",
            ".evergreen-release.yml",
        ):
            os.symlink(
                os.path.join(os.path.dirname(SCRIPT), os.pardir, os.pardir, f),
                os.path.join(tmp_path, f),
            )
        # resolve_tasks invokes scripts/evergreen/should_prepare_openshift_bundles.sh
        # relative to cwd; expose the repo's scripts directory.
        os.symlink(
            os.path.join(os.path.dirname(SCRIPT), os.pardir, os.pardir, "scripts"),
            os.path.join(tmp_path, "scripts"),
        )
        env = dict(
            os.environ,
            triggered_by_git_tag="v0.0.0-test",
            tag_description_override=annotation,
        )
        subprocess.run(
            [sys.executable, SCRIPT],
            cwd=tmp_path,
            env=env,
            check=True,
            capture_output=True,
        )
        with open(os.path.join(tmp_path, "evergreen_tasks.json")) as f:
            return [v["name"] for v in json.load(f)["buildvariants"]]

    return run


def test_new_process_includes_update_docs_repo(generated_variants):
    variants = generated_variants("[new] 1.2.3 release")
    assert "update_docs_repo" in variants
    assert "release_decider_patch" not in variants


def test_deprecated_process_excludes_update_docs_repo(generated_variants):
    variants = generated_variants("1.2.3 release")
    assert "update_docs_repo" not in variants
