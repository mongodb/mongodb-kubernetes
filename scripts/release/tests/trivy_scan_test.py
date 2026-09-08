"""Tests for scripts/release/trivy_scan.py"""

from unittest.mock import patch

import pytest

from scripts.release.build.build_scenario import BuildScenario
from scripts.release.trivy_scan import (
    ScanResult,
    Vulnerability,
    format_slack_message,
    parse_trivy_report,
    resolve_scan_targets,
    should_notify,
    summarize_result,
)

SAMPLE_TRIVY_REPORT = {
    "Results": [
        {
            "Target": "quay.io/mongodb/mongodb-kubernetes:1.5.0 (ubi 9.4)",
            "Vulnerabilities": [
                {
                    "VulnerabilityID": "CVE-2026-0001",
                    "PkgName": "openssl-libs",
                    "InstalledVersion": "3.0.7-1",
                    "FixedVersion": "3.0.7-2",
                    "Severity": "CRITICAL",
                    "Title": "openssl: buffer overflow",
                    "PrimaryURL": "https://avd.aquasec.com/nvd/cve-2026-0001",
                },
                {
                    "VulnerabilityID": "CVE-2026-0002",
                    "PkgName": "glibc",
                    "InstalledVersion": "2.34-60",
                    "Severity": "HIGH",
                    "Title": "glibc: use after free",
                },
            ],
        },
        {
            "Target": "usr/local/bin/mongodb-kubernetes-operator",
            "Vulnerabilities": [
                {
                    "VulnerabilityID": "CVE-2026-0003",
                    "PkgName": "golang.org/x/crypto",
                    "InstalledVersion": "v0.30.0",
                    "FixedVersion": "v0.31.0",
                    "Severity": "HIGH",
                },
                # duplicate finding, must be deduplicated
                {
                    "VulnerabilityID": "CVE-2026-0003",
                    "PkgName": "golang.org/x/crypto",
                    "InstalledVersion": "v0.30.0",
                    "FixedVersion": "v0.31.0",
                    "Severity": "HIGH",
                },
            ],
        },
        {
            "Target": "empty target",
            # trivy emits results without a Vulnerabilities key when nothing is found
        },
    ]
}


class TestParseTrivyReport:
    def test_parses_and_deduplicates_vulnerabilities(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "quay.io/mongodb/mongodb-kubernetes:1.5.0", "linux/amd64")

        assert result.image_ref == "quay.io/mongodb/mongodb-kubernetes:1.5.0"
        assert result.platform == "linux/amd64"
        assert len(result.vulnerabilities) == 3
        assert [v.id for v in result.vulnerabilities] == ["CVE-2026-0001", "CVE-2026-0002", "CVE-2026-0003"]

    def test_fix_status(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "image:tag", "linux/amd64")

        fixed = {v.id: v.is_fixed for v in result.vulnerabilities}
        assert fixed == {"CVE-2026-0001": True, "CVE-2026-0002": False, "CVE-2026-0003": True}

    def test_empty_report(self):
        result = parse_trivy_report({}, "image:tag", "linux/amd64")
        assert result.vulnerabilities == []

    def test_report_with_null_results(self):
        result = parse_trivy_report({"Results": None}, "image:tag", "linux/amd64")
        assert result.vulnerabilities == []


class TestSummarizeResult:
    def test_counts_by_severity_with_fixable(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "image:tag", "linux/amd64")
        assert summarize_result(result) == "CRITICAL: 1 (1 fixable), HIGH: 2 (1 fixable)"

    def test_no_vulnerabilities(self):
        result = ScanResult(image_ref="image:tag", platform="linux/amd64")
        assert summarize_result(result) == "no vulnerabilities found"


class TestResolveScanTargets:
    def test_resolves_operator_from_build_info(self):
        targets = resolve_scan_targets("operator", BuildScenario.RELEASE, "1.5.0")

        assert targets == [
            (
                "quay.io/mongodb/mongodb-kubernetes:1.5.0",
                ["linux/arm64", "linux/amd64", "linux/s390x", "linux/ppc64le"],
            )
        ]

    def test_unknown_image_raises(self):
        with pytest.raises(ValueError, match="not defined in the build info"):
            resolve_scan_targets("does-not-exist", BuildScenario.RELEASE, "1.5.0")

    @patch("scripts.release.trivy_scan.get_currently_used_agents")
    def test_agent_current_expands_to_currently_used_agents(self, mock_agents):
        mock_agents.return_value = [("108.0.2.8729-1", "100.12.2"), ("107.0.13.8702-1", "100.12.0")]

        targets = resolve_scan_targets("agent", BuildScenario.RELEASE, "current")

        refs = [ref for ref, _ in targets]
        assert refs == [
            "quay.io/mongodb/mongodb-agent:107.0.13.8702-1",
            "quay.io/mongodb/mongodb-agent:108.0.2.8729-1",
        ]

    @patch("scripts.release.trivy_scan.get_currently_used_agents")
    def test_agent_all_expands_to_currently_used_agents(self, mock_agents):
        mock_agents.return_value = [("108.0.2.8729-1", "100.12.2")]

        targets = resolve_scan_targets("agent", BuildScenario.RELEASE, "all")

        assert [ref for ref, _ in targets] == ["quay.io/mongodb/mongodb-agent:108.0.2.8729-1"]

    @patch("scripts.release.trivy_scan.get_currently_used_agents")
    def test_agent_no_versions_raises(self, mock_agents):
        mock_agents.return_value = []

        with pytest.raises(ValueError, match="Could not resolve any currently used agent versions"):
            resolve_scan_targets("agent", BuildScenario.RELEASE, "current")

    def test_agent_explicit_version_is_not_expanded(self):
        targets = resolve_scan_targets("agent", BuildScenario.RELEASE, "108.0.2.8729-1")

        assert [ref for ref, _ in targets] == ["quay.io/mongodb/mongodb-agent:108.0.2.8729-1"]


class TestFormatSlackMessage:
    def test_message_contains_findings_and_fix_status(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "quay.io/mongodb/mongodb-kubernetes:1.5.0", "linux/amd64")

        message = format_slack_message("operator", BuildScenario.RELEASE, [result])

        text = str(message)
        assert "CVE scan: vulnerabilities found in operator image" in text
        assert "quay.io/mongodb/mongodb-kubernetes:1.5.0" in text
        assert "CVE-2026-0001" in text
        assert "fixed in `3.0.7-2`" in text
        assert "*no fix available*" in text

    def test_results_without_findings_are_omitted(self):
        clean = ScanResult(image_ref="quay.io/mongodb/clean-image:1.0.0", platform="linux/arm64")
        dirty = parse_trivy_report(SAMPLE_TRIVY_REPORT, "quay.io/mongodb/dirty-image:1.0.0", "linux/amd64")

        message = format_slack_message("operator", BuildScenario.STAGING, [clean, dirty])

        text = str(message)
        assert "clean-image" not in text
        assert "dirty-image" in text

    def test_long_finding_lists_are_truncated(self):
        vulnerabilities = [
            Vulnerability(id=f"CVE-2026-{i:04d}", severity="HIGH", pkg_name="pkg", installed_version="1.0")
            for i in range(25)
        ]
        result = ScanResult(image_ref="image:tag", platform="linux/amd64", vulnerabilities=vulnerabilities)

        message = format_slack_message("operator", BuildScenario.STAGING, [result])

        assert "and 15 more" in str(message)

    def test_task_link_included_when_task_id_set(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "some_task_id_123")
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "image:tag", "linux/amd64")

        message = format_slack_message("operator", BuildScenario.RELEASE, [result])

        assert "https://spruce.mongodb.com/task/some_task_id_123" in str(message)


class TestShouldNotify:
    @pytest.fixture(autouse=True)
    def clean_env(self, monkeypatch):
        monkeypatch.delenv("TRIVY_SCAN_NOTIFY", raising=False)
        monkeypatch.delenv("triggered_by_git_tag", raising=False)
        monkeypatch.delenv("requester", raising=False)

    @pytest.mark.parametrize("requester,expected", [("commit", True), ("github_tag", True), ("trigger", True)])
    def test_notifies_on_mainline_requesters(self, monkeypatch, requester, expected):
        monkeypatch.setenv("requester", requester)
        assert should_notify() is expected

    @pytest.mark.parametrize("requester", ["patch", "github_pr", "ad_hoc", ""])
    def test_does_not_notify_on_patches(self, monkeypatch, requester):
        monkeypatch.setenv("requester", requester)
        assert should_notify() is False

    def test_notifies_on_tag_triggered_release_patch(self, monkeypatch):
        # The new release process (release_publish) runs as a decider-created
        # patch carrying the triggered_by_git_tag param
        monkeypatch.setenv("requester", "patch")
        monkeypatch.setenv("triggered_by_git_tag", "1.12.0")
        assert should_notify() is True

    def test_empty_git_tag_does_not_notify(self, monkeypatch):
        monkeypatch.setenv("requester", "patch")
        monkeypatch.setenv("triggered_by_git_tag", "")
        assert should_notify() is False

    def test_override_enables_notifications(self, monkeypatch):
        monkeypatch.setenv("requester", "patch")
        monkeypatch.setenv("TRIVY_SCAN_NOTIFY", "true")
        assert should_notify() is True

    def test_override_disables_notifications(self, monkeypatch):
        monkeypatch.setenv("requester", "commit")
        monkeypatch.setenv("TRIVY_SCAN_NOTIFY", "false")
        assert should_notify() is False

    def test_override_disables_notifications_even_for_git_tag(self, monkeypatch):
        monkeypatch.setenv("triggered_by_git_tag", "1.12.0")
        monkeypatch.setenv("TRIVY_SCAN_NOTIFY", "false")
        assert should_notify() is False
