"""Tests for scripts/dev/bump_third_party_images.py (version selection policy, Docker Hub paging, release.json update)."""

import json
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest

from scripts.dev import bump_third_party_images as bump
from scripts.dev.bump_third_party_images import Candidate, candidates_from_tags, parse_stable_tag, select_version

NOW = datetime(2026, 11, 17, 12, 0, 0, tzinfo=timezone.utc)


def cand(tag: str, age_days: float) -> Candidate:
    parsed = parse_stable_tag(tag)
    assert parsed is not None
    return Candidate(tag=tag, version=parsed[1], last_updated=NOW - timedelta(days=age_days))


def hub_entry(name: str, age_days: float) -> dict:
    return {"name": name, "last_updated": (NOW - timedelta(days=age_days)).strftime("%Y-%m-%dT%H:%M:%S.123456Z")}


@pytest.mark.parametrize(
    "tag,expected",
    [
        ("0.165.0", ("", (0, 165, 0))),
        ("v1.37.11", ("v", (1, 37, 11))),
        ("1.2.3", ("", (1, 2, 3))),
        ("0.165.0-amd64", None),
        ("v1.37.11-rc1", None),
        ("v1.37", None),
        ("v1.37.11.1", None),
        ("nightly", None),
        ("latest", None),
        ("contrib-debug-dev", None),
        ("main-1a2b3c", None),
        ("", None),
    ],
)
def test_parse_stable_tag(tag, expected):
    assert parse_stable_tag(tag) == expected


def test_candidates_from_tags_drops_unstable_and_undated_tags():
    tags = [
        hub_entry("0.165.1", 20),
        hub_entry("0.165.1-amd64", 20),
        hub_entry("0.165.2-rc1", 1),
        hub_entry("latest", 0),
        {"name": "0.165.3", "last_updated": None},
    ]
    assert [c.tag for c in candidates_from_tags(tags)] == ["0.165.1"]


def test_select_patch_only_ignores_newer_minor():
    decision = select_version("0.165.0", [cand("0.166.0", 60), cand("0.165.1", 30)], NOW)
    assert decision.new_tag == "0.165.1"


def test_select_patch_only_with_only_newer_minor_is_no_change():
    decision = select_version("0.165.0", [cand("0.166.0", 60), cand("0.167.0", 60)], NOW)
    assert decision.new_tag is None
    assert "no newer patch" in decision.reason


def test_select_picks_highest_patch():
    candidates = [cand("v1.37.12", 40), cand("v1.37.13", 20), cand("v1.37.11", 90)]
    assert select_version("v1.37.11", candidates, NOW).new_tag == "v1.37.13"


def test_select_never_downgrades_or_repeats_current():
    decision = select_version("v1.37.11", [cand("v1.37.11", 90), cand("v1.37.10", 120), cand("v1.36.9", 200)], NOW)
    assert decision.new_tag is None


def test_select_soak_boundary():
    # exactly 14 days old is eligible, a second younger is not
    assert select_version("0.165.0", [cand("0.165.1", 14)], NOW).new_tag == "0.165.1"
    young = Candidate("0.165.1", (0, 165, 1), NOW - timedelta(days=14) + timedelta(seconds=1))
    assert select_version("0.165.0", [young], NOW).new_tag is None


def test_select_falls_back_to_older_soaked_patch_when_newest_is_still_soaking():
    decision = select_version("0.165.0", [cand("0.165.2", 3), cand("0.165.1", 20)], NOW)
    assert decision.new_tag == "0.165.1"
    assert "0.165.2" in decision.reason and "soaking" in decision.reason


def test_select_all_candidates_soaking_is_no_change_and_hints_at_security_flag():
    decision = select_version("0.165.0", [cand("0.165.1", 2)], NOW)
    assert decision.new_tag is None
    assert "soak" in decision.reason and "--security" in decision.reason


def test_security_bypasses_soak_and_takes_highest():
    decision = select_version("0.165.0", [cand("0.165.2", 0.1), cand("0.165.1", 20)], NOW, security=True)
    assert decision.new_tag == "0.165.2"
    assert "security" in decision.reason


def test_security_still_respects_patch_only_scope():
    decision = select_version("0.165.0", [cand("0.166.0", 0.1)], NOW, security=True)
    assert decision.new_tag is None


def test_allow_minor_picks_highest_minor_within_major():
    candidates = [cand("v1.38.4", 60), cand("v1.40.2", 21), cand("v1.37.12", 30)]
    assert select_version("v1.37.11", candidates, NOW, allow_minor=True).new_tag == "v1.40.2"


def test_allow_minor_respects_soak_per_version():
    candidates = [cand("v1.40.0", 3), cand("v1.39.1", 30)]
    assert select_version("v1.37.11", candidates, NOW, allow_minor=True).new_tag == "v1.39.1"


def test_major_is_never_bumped_automatically():
    decision = select_version("v1.37.11", [cand("v2.0.0", 365)], NOW, allow_minor=True, security=True)
    assert decision.new_tag is None


def test_tag_style_of_current_version_is_kept():
    # envoy tags carry a "v" prefix, the bare variants are not the tags we track
    assert select_version("v1.37.11", [cand("1.37.12", 30)], NOW).new_tag is None
    assert select_version("0.165.0", [cand("v0.165.1", 30)], NOW).new_tag is None


def test_custom_soak_days():
    assert select_version("0.165.0", [cand("0.165.1", 5)], NOW, soak_days=3).new_tag == "0.165.1"
    assert select_version("0.165.0", [cand("0.165.1", 5)], NOW, soak_days=7).new_tag is None


def test_non_stable_current_version_is_rejected():
    with pytest.raises(ValueError):
        select_version("latest", [], NOW)


def test_fetch_tags_follows_pagination_and_stops_at_current_tag():
    pages = {
        "p1": {"results": [hub_entry("0.165.2", 1)], "next": "p2"},
        "p2": {"results": [hub_entry("0.165.1", 20), hub_entry("0.165.0", 30)], "next": "p3"},
        "p3": {"results": [hub_entry("0.164.0", 90)], "next": None},
    }
    requested = []

    def fake_fetch(url):
        requested.append(url)
        return pages[url if url in pages else "p1"]

    tags = bump.fetch_tags("otel/opentelemetry-collector-contrib", "0.165.0", False, fetch_json=fake_fetch)

    assert [t["name"] for t in tags] == ["0.165.2", "0.165.1", "0.165.0"]
    assert len(requested) == 2  # p3 is never requested
    assert "name=0.165." in requested[0]
    assert "page_size=100" in requested[0]


def test_fetch_tags_walks_all_pages_when_current_tag_is_missing():
    pages = {
        "p2": {"results": [hub_entry("0.165.1", 20)], "next": None},
    }

    def fake_fetch(url):
        return {"results": [hub_entry("0.165.2", 1)], "next": "p2"} if url not in pages else pages[url]

    tags = bump.fetch_tags("otel/opentelemetry-collector-contrib", "0.165.0", False, fetch_json=fake_fetch)
    assert [t["name"] for t in tags] == ["0.165.2", "0.165.1"]


def test_fetch_tags_name_filter_depends_on_policy():
    seen = []

    def fake_fetch(url):
        seen.append(url)
        return {"results": [], "next": None}

    bump.fetch_tags("envoyproxy/envoy", "v1.37.11", False, fetch_json=fake_fetch)
    bump.fetch_tags("envoyproxy/envoy", "v1.37.11", True, fetch_json=fake_fetch)
    assert seen[0].endswith("&name=v1.37.")
    assert seen[1].endswith("&name=v1.")


def write_release(path, otel="0.165.0", envoy="v1.37.11"):
    path.write_text(
        json.dumps(
            {
                "mongodbOperator": "1.15.1",
                "search": {"version": "1.70.1", "metricsForwarderVersion": otel, "envoyVersion": envoy},
            },
            indent=2,
        )
        + "\n"
    )


def patch_hub(monkeypatch, tags):
    monkeypatch.setattr(bump, "fetch_tags", lambda *args, **kwargs: tags)
    monkeypatch.setattr(bump, "utcnow", lambda: NOW)


def test_main_writes_release_json_and_github_output(tmp_path, monkeypatch, capsys):
    release = tmp_path / "release.json"
    write_release(release)
    gh_output = tmp_path / "gh_output"
    monkeypatch.setenv("GITHUB_OUTPUT", str(gh_output))
    patch_hub(monkeypatch, [hub_entry("0.165.1", 30), hub_entry("0.165.0", 40)])

    assert bump.main(["--image", "otel", "--release-json", str(release)]) == 0

    data = json.loads(release.read_text())
    assert data["search"]["metricsForwarderVersion"] == "0.165.1"
    assert data["search"]["envoyVersion"] == "v1.37.11"  # untouched
    assert release.read_text().endswith("}\n")
    assert gh_output.read_text() == "otel_changed=true\notel_version=0.165.1\n"
    assert "bump 0.165.0 -> 0.165.1" in capsys.readouterr().out


def test_main_updates_the_envoy_key(tmp_path, monkeypatch):
    release = tmp_path / "release.json"
    write_release(release)
    patch_hub(monkeypatch, [hub_entry("v1.37.12", 30)])

    assert bump.main(["--image", "envoy", "--release-json", str(release)]) == 0

    data = json.loads(release.read_text())
    assert data["search"]["envoyVersion"] == "v1.37.12"
    assert data["search"]["metricsForwarderVersion"] == "0.165.0"


def test_main_without_eligible_version_exits_zero_and_leaves_file_untouched(tmp_path, monkeypatch, capsys):
    release = tmp_path / "release.json"
    write_release(release)
    before = release.read_text()
    gh_output = tmp_path / "gh_output"
    monkeypatch.setenv("GITHUB_OUTPUT", str(gh_output))
    patch_hub(monkeypatch, [hub_entry("0.165.1", 2), hub_entry("0.166.0", 90)])

    assert bump.main(["--image", "otel", "--release-json", str(release)]) == 0

    assert release.read_text() == before
    assert gh_output.read_text() == "otel_changed=false\notel_version=\n"
    assert "decision: no change" in capsys.readouterr().out


def test_main_dry_run_does_not_write(tmp_path, monkeypatch, capsys):
    release = tmp_path / "release.json"
    write_release(release)
    before = release.read_text()
    patch_hub(monkeypatch, [hub_entry("0.165.1", 30)])

    assert bump.main(["--image", "otel", "--release-json", str(release), "--dry-run"]) == 0

    assert release.read_text() == before
    assert "bump 0.165.0 -> 0.165.1" in capsys.readouterr().out


def test_main_security_flag_skips_soak(tmp_path, monkeypatch):
    release = tmp_path / "release.json"
    write_release(release)
    patch_hub(monkeypatch, [hub_entry("0.165.1", 0.5)])

    assert bump.main(["--image", "otel", "--release-json", str(release), "--security"]) == 0
    assert json.loads(release.read_text())["search"]["metricsForwarderVersion"] == "0.165.1"


def test_main_allow_minor_flag(tmp_path, monkeypatch):
    release = tmp_path / "release.json"
    write_release(release)
    patch_hub(monkeypatch, [hub_entry("v1.38.0", 30)])

    assert bump.main(["--image", "envoy", "--release-json", str(release)]) == 0
    assert json.loads(release.read_text())["search"]["envoyVersion"] == "v1.37.11"

    assert bump.main(["--image", "envoy", "--release-json", str(release), "--allow-minor"]) == 0
    assert json.loads(release.read_text())["search"]["envoyVersion"] == "v1.38.0"


def test_release_json_in_repo_has_the_tracked_versions():
    search = json.loads((Path(__file__).resolve().parents[3] / "release.json").read_text())["search"]
    for spec in bump.IMAGES.values():
        assert parse_stable_tag(search[spec.release_json_key]) is not None
