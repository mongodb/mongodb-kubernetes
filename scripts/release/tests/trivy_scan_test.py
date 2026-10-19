"""Tests for scripts/release/trivy_scan.py"""

import sys
from unittest.mock import patch

import pytest

from scripts.release.build.build_scenario import BuildScenario
from scripts.release.trivy_scan import (
    ScanResult,
    Vulnerability,
    aggregate_by_image,
    format_slack_message,
    get_image_digest,
    get_registry_url,
    get_report_url,
    get_task_files_url,
    main,
    parse_trivy_report,
    print_stdout_report,
    resolve_image_versions,
    resolve_scan_targets,
    scan_images,
    short_image_name,
    should_notify,
    split_into_sections,
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


def make_result(image_ref="image:tag", platform="linux/amd64", fixable=0, unfixable=0):
    """Build a ScanResult with the requested number of fixable/unfixable findings."""
    vulnerabilities = [
        Vulnerability(id=f"CVE-F{i:04d}", severity="HIGH", pkg_name="pkg", installed_version="1.0", fixed_version="1.1")
        for i in range(fixable)
    ] + [
        Vulnerability(id=f"CVE-U{i:04d}", severity="HIGH", pkg_name="pkg", installed_version="1.0")
        for i in range(unfixable)
    ]
    return ScanResult(image_ref=image_ref, platform=platform, vulnerabilities=vulnerabilities)


class TestParseTrivyReport:
    def test_parses_and_deduplicates_vulnerabilities(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "quay.io/mongodb/mongodb-kubernetes:1.5.0", "linux/amd64")

        assert result.image_ref == "quay.io/mongodb/mongodb-kubernetes:1.5.0"
        assert result.platform == "linux/amd64"
        assert [v.id for v in result.vulnerabilities] == ["CVE-2026-0001", "CVE-2026-0002", "CVE-2026-0003"]
        assert result.total_count == 3
        assert result.fixable_count == 2
        assert result.unfixable_count == 1

    def test_fix_status(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "image:tag", "linux/amd64")

        fixed = {v.id: v.is_fixed for v in result.vulnerabilities}
        assert fixed == {"CVE-2026-0001": True, "CVE-2026-0002": False, "CVE-2026-0003": True}

    def test_empty_fixed_version_is_unfixable(self):
        report = {
            "Results": [
                {
                    "Vulnerabilities": [
                        {"VulnerabilityID": "CVE-1", "PkgName": "pkg", "InstalledVersion": "1.0", "FixedVersion": ""},
                        {"VulnerabilityID": "CVE-2", "PkgName": "pkg", "InstalledVersion": "1.0", "FixedVersion": None},
                        {
                            "VulnerabilityID": "CVE-3",
                            "PkgName": "pkg",
                            "InstalledVersion": "1.0",
                            "FixedVersion": "   ",
                        },
                    ]
                }
            ]
        }
        result = parse_trivy_report(report, "image:tag", "linux/amd64")

        assert result.total_count == 3
        assert result.fixable_count == 0
        assert result.unfixable_count == 3

    def test_duplicate_findings_are_deduplicated(self):
        report = {
            "Results": [
                {
                    "Vulnerabilities": [
                        {"VulnerabilityID": "CVE-1", "PkgName": "pkg", "InstalledVersion": "1.0", "FixedVersion": ""},
                    ]
                },
                {
                    "Vulnerabilities": [
                        {"VulnerabilityID": "CVE-1", "PkgName": "pkg", "InstalledVersion": "1.0", "FixedVersion": ""},
                    ]
                },
            ]
        }
        result = parse_trivy_report(report, "image:tag", "linux/amd64")

        assert result.total_count == 1

    def test_empty_report(self):
        result = parse_trivy_report({}, "image:tag", "linux/amd64")
        assert result.vulnerabilities == []
        assert result.total_count == 0

    def test_report_with_null_results(self):
        result = parse_trivy_report({"Results": None}, "image:tag", "linux/amd64")
        assert result.vulnerabilities == []
        assert result.total_count == 0


class TestAggregateByImage:
    def test_merges_platforms_of_the_same_image(self):
        amd64 = make_result(image_ref="image:tag", platform="linux/amd64", fixable=2, unfixable=1)
        arm64 = make_result(image_ref="image:tag", platform="linux/arm64", fixable=2, unfixable=1)

        merged = aggregate_by_image([amd64, arm64])

        assert len(merged) == 1
        assert merged[0].image_ref == "image:tag"
        # findings are unioned, not summed per platform
        assert merged[0].total_count == 3
        assert merged[0].fixable_count == 2
        assert merged[0].unfixable_count == 1

    def test_keeps_distinct_images(self):
        merged = aggregate_by_image([make_result(image_ref="a:1.0", fixable=1), make_result(image_ref="b:1.0")])

        assert [r.image_ref for r in merged] == ["a:1.0", "b:1.0"]


class TestAggregateReportFiles:
    def test_merges_digest_and_report_files_of_all_platforms(self):
        amd64 = make_result(image_ref="image:tag", platform="linux/amd64", fixable=1)
        amd64.image_digest = "sha256:abc"
        amd64.report_files = {"linux/amd64": "amd64.json"}
        arm64 = make_result(image_ref="image:tag", platform="linux/arm64", fixable=1)
        arm64.report_files = {"linux/arm64": "arm64.json"}

        merged = aggregate_by_image([arm64, amd64])[0]

        assert merged.image_digest == "sha256:abc"
        assert merged.report_files == {"linux/arm64": "arm64.json", "linux/amd64": "amd64.json"}

    def test_report_file_prefers_amd64(self):
        result = ScanResult(image_ref="i:1", platform="")
        result.report_files = {"linux/arm64": "arm64.json", "linux/amd64": "amd64.json"}

        assert result.report_file == "amd64.json"

    def test_report_file_falls_back_to_the_first_platform(self):
        result = ScanResult(image_ref="i:1", platform="")
        result.report_files = {"linux/s390x": "s390x.json", "linux/arm64": "arm64.json"}

        assert result.report_file == "s390x.json"

    def test_no_report_file(self):
        assert ScanResult(image_ref="i:1", platform="").report_file == ""


class TestSummarizeResult:
    def test_without_unfixable_for_slack(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "image:tag", "linux/amd64")

        assert summarize_result(result, with_unfixable=False) == "image:tag: 3 vulnerabilities: 2 fixable"

    def test_one_line_with_fixable_and_unfixable_counts(self):
        result = parse_trivy_report(SAMPLE_TRIVY_REPORT, "image:tag", "linux/amd64")

        assert summarize_result(result) == "image:tag: 3 vulnerabilities: 2 fixable, 1 not fixable"

    def test_uses_image_name_and_tag_without_registry(self):
        result = make_result(image_ref="quay.io/mongodb/mongodb-agent:1.0", fixable=1)

        assert summarize_result(result) == "mongodb-agent:1.0: 1 vulnerabilities: 1 fixable, 0 not fixable"

    def test_no_vulnerabilities(self):
        result = ScanResult(image_ref="image:tag", platform="linux/amd64")

        assert summarize_result(result) == "image:tag: 0 vulnerabilities: 0 fixable, 0 not fixable"


class TestPrintStdoutReport:
    def test_clean_image_is_not_printed(self, capsys):
        print_stdout_report([ScanResult(image_ref="clean:1.0", platform="linux/amd64")])

        assert capsys.readouterr().out == ""

    def test_unfixable_only_image_is_printed(self, capsys):
        print_stdout_report([make_result(image_ref="unfixable:1.0", unfixable=12)])

        out = capsys.readouterr().out
        assert out == "- unfixable:1.0: 12 vulnerabilities: 0 fixable, 12 not fixable\n"

    def test_only_images_with_findings_are_printed(self, capsys):
        clean = ScanResult(image_ref="clean:1.0", platform="linux/amd64")
        unfixable = make_result(image_ref="unfixable:1.0", unfixable=12)
        fixable = parse_trivy_report(SAMPLE_TRIVY_REPORT, "fixable:1.0", "linux/amd64")

        print_stdout_report([clean, unfixable, fixable])

        out = capsys.readouterr().out
        assert "clean:1.0" not in out
        assert "- unfixable:1.0: 12 vulnerabilities: 0 fixable, 12 not fixable" in out
        assert "- fixable:1.0: 3 vulnerabilities: 2 fixable, 1 not fixable" in out


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
    @pytest.fixture(autouse=True)
    def clean_env(self, monkeypatch):
        for name in ("TASK_ID", "execution", "TRIVY_REPORTS_AWS_KEY", "TRIVY_REPORTS_AWS_SECRET"):
            monkeypatch.delenv(name, raising=False)

    def test_single_message_lists_only_images_with_fixable_vulnerabilities(self):
        clean = ScanResult(image_ref="registry.example.com/mongodb/clean-image:1.0.0", platform="linux/arm64")
        unfixable = make_result(image_ref="registry.example.com/mongodb/unfixable-image:1.0.0", unfixable=12)
        dirty = parse_trivy_report(SAMPLE_TRIVY_REPORT, "registry.example.com/mongodb/dirty-image:1.0.0", "linux/amd64")

        message = format_slack_message(BuildScenario.STAGING, [clean, unfixable, dirty])

        # a single section holds every reported image
        sections = [b for b in message["blocks"] if b["type"] == "section"]
        assert len(sections) == 1
        text = sections[0]["text"]["text"]
        # images without findings, or with only unfixable ones, are not in the message
        assert "clean-image" not in text
        assert "unfixable-image" not in text
        # the description has the total and the fixable ones, not the unfixable ones
        assert "- dirty-image:1.0.0: 3 vulnerabilities: 2 fixable" in text
        assert "not fixable" not in text
        assert "build scenario: `staging`" in text
        # no per-CVE detail in the message
        assert "CVE-2026-0001" not in text

    def test_image_is_linked_to_its_registry_entry(self):
        digest = "sha256:" + "a" * 64
        result = make_result(
            image_ref="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent:1.0", fixable=2
        )
        result.image_digest = digest

        text = format_slack_message(BuildScenario.STAGING, [result])["blocks"][1]["text"]["text"]

        url = (
            "https://us-east-1.console.aws.amazon.com/ecr/repositories/private/268558157000/staging/mongodb-agent"
            f"/_/image/{digest}/details?region=us-east-1"
        )
        assert f"- <{url}|mongodb-agent:1.0>: 2 vulnerabilities: 2 fixable" in text

    def test_each_item_links_to_the_json_report_of_its_image(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "some_task_id_123")
        monkeypatch.setenv("execution", "2")
        monkeypatch.setenv("TRIVY_REPORTS_AWS_KEY", "AKIAEXAMPLE")
        monkeypatch.setenv("TRIVY_REPORTS_AWS_SECRET", "secret-value")
        a = make_result(image_ref="a:1.0", fixable=1)
        a.report_files = {"linux/amd64": "trivy-a-1.0-linux-amd64.json"}
        b = make_result(image_ref="b:1.0", fixable=2)
        b.report_files = {"linux/amd64": "trivy-b-1.0-linux-amd64.json"}

        message = format_slack_message(BuildScenario.RELEASE, [a, b])

        line_a, line_b = message["blocks"][1]["text"]["text"].splitlines()[1:]
        assert line_a.startswith("- a:1.0: 1 vulnerabilities: 1 fixable (<https://operator-e2e-artifacts.s3")
        assert "/trivy-reports/some_task_id_123/2/trivy-a-1.0-linux-amd64.json?" in line_a
        assert line_b.startswith("- b:1.0: 2 vulnerabilities: 2 fixable (<https://operator-e2e-artifacts.s3")
        assert "/trivy-reports/some_task_id_123/2/trivy-b-1.0-linux-amd64.json?" in line_b
        assert line_a.endswith("|report>)") and line_b.endswith("|report>)")
        assert "secret-value" not in str(message)

    def test_links_to_the_task_files_without_credentials(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "some_task_id_123")
        monkeypatch.setenv("execution", "2")
        result = make_result(image_ref="a:1.0", fixable=1)
        result.report_files = {"linux/amd64": "trivy-a.json"}

        line = format_slack_message(BuildScenario.RELEASE, [result])["blocks"][1]["text"]["text"].splitlines()[1]

        url = "https://spruce.mongodb.com/task/some_task_id_123/files?execution=2"
        assert line == f"- a:1.0: 1 vulnerabilities: 1 fixable (<{url}|report>)"

    def test_no_report_link_without_task_id(self):
        message = format_slack_message(BuildScenario.RELEASE, [make_result(image_ref="a:1.0", fixable=1)])

        assert message["blocks"][1]["text"]["text"].splitlines()[1] == "- a:1.0: 1 vulnerabilities: 1 fixable"


class TestSlackMessageSize:
    @pytest.fixture(autouse=True)
    def long_report_url(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "mongodb_kubernetes_init_test_run_cve_scan_images_patch_" + "a" * 100)
        monkeypatch.setenv("execution", "0")

    def test_many_images_are_split_in_sections_within_slack_limit(self):
        results = [make_result(image_ref=f"mongodb-agent:{i}.0.0.1-1", fixable=3, unfixable=1) for i in range(25)]

        message = format_slack_message(BuildScenario.STAGING, results)

        sections = [b["text"]["text"] for b in message["blocks"] if b["type"] == "section"]
        assert len(sections) > 1
        assert all(len(text) <= 3000 for text in sections)
        # a single message, every image listed exactly once, in order
        lines = [line for text in sections for line in text.splitlines() if line.startswith("- ")]
        assert [line.split(" ")[1] for line in lines] == [f"mongodb-agent:{i}.0.0.1-1:" for i in range(25)]

    def test_presigned_links_do_not_exceed_slack_limit(self, monkeypatch):
        monkeypatch.setenv("TRIVY_REPORTS_AWS_KEY", "AKIAEXAMPLE")
        monkeypatch.setenv("TRIVY_REPORTS_AWS_SECRET", "secret-value")
        results = []
        for i in range(14):
            result = make_result(
                image_ref=f"268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent:{i}.0.0.1-1", fixable=3
            )
            result.image_digest = "sha256:" + "b" * 64
            result.report_files = {"linux/amd64": f"trivy-mongodb-agent-{i}.0.0.1-1-linux-amd64.json"}
            results.append(result)

        message = format_slack_message(BuildScenario.STAGING, results)

        sections = [b["text"]["text"] for b in message["blocks"] if b["type"] == "section"]
        assert len(sections) > 1
        assert all(len(text) <= 3000 for text in sections)
        assert sum(text.count("|report>") for text in sections) == 14

    def test_the_intro_is_only_in_the_first_section(self):
        results = [make_result(image_ref=f"image-{i}:1.0", fixable=1) for i in range(30)]

        sections = [b["text"]["text"] for b in format_slack_message(BuildScenario.STAGING, results)["blocks"][1:]]

        assert sections[0].startswith("Fixable vulnerabilities found in newly built images")
        assert all(not text.startswith("Fixable vulnerabilities found") for text in sections[1:])

    def test_few_images_fit_in_a_single_section(self):
        results = [make_result(image_ref=f"image-{i}:1.0", fixable=1) for i in range(3)]

        message = format_slack_message(BuildScenario.STAGING, results)

        assert len([b for b in message["blocks"] if b["type"] == "section"]) == 1

    def test_split_into_sections_keeps_lines_intact(self):
        sections = split_into_sections("intro", ["a" * 10, "b" * 10, "c" * 10], max_chars=30)

        assert sections == ["intro\n" + "a" * 10 + "\n" + "b" * 10, "c" * 10]


class TestTaskFilesUrl:
    @pytest.fixture(autouse=True)
    def clean_env(self, monkeypatch):
        monkeypatch.delenv("TASK_ID", raising=False)
        monkeypatch.delenv("execution", raising=False)

    def test_empty_without_task_id(self):
        assert get_task_files_url() == ""

    def test_links_to_task_files(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "task_1")
        assert get_task_files_url() == "https://spruce.mongodb.com/task/task_1/files"

    def test_includes_execution(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "task_1")
        monkeypatch.setenv("execution", "0")
        assert get_task_files_url() == "https://spruce.mongodb.com/task/task_1/files?execution=0"


class TestReportUrl:
    @pytest.fixture(autouse=True)
    def clean_env(self, monkeypatch):
        for name in (
            "TASK_ID",
            "execution",
            "TRIVY_REPORTS_AWS_KEY",
            "TRIVY_REPORTS_AWS_SECRET",
            "TRIVY_REPORTS_REGION",
        ):
            monkeypatch.delenv(name, raising=False)

    def _credentials(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "task_1")
        monkeypatch.setenv("execution", "3")
        monkeypatch.setenv("TRIVY_REPORTS_AWS_KEY", "AKIAEXAMPLE")
        monkeypatch.setenv("TRIVY_REPORTS_AWS_SECRET", "secret-value")

    def test_presigned_link_to_the_json_report(self, monkeypatch):
        self._credentials(monkeypatch)

        url = get_report_url("trivy-image-linux-amd64.json")

        assert url.startswith("https://operator-e2e-artifacts.s3.amazonaws.com/trivy-reports/task_1/3/")
        assert "/trivy-image-linux-amd64.json?" in url
        assert "X-Amz-Expires=604800" in url
        assert "X-Amz-Signature=" in url
        assert "secret-value" not in url

    def test_falls_back_to_the_task_files_without_credentials(self, monkeypatch):
        monkeypatch.setenv("TASK_ID", "task_1")
        monkeypatch.setenv("execution", "3")

        assert get_report_url("trivy-image.json") == "https://spruce.mongodb.com/task/task_1/files?execution=3"

    def test_falls_back_to_the_task_files_without_report_file(self, monkeypatch):
        self._credentials(monkeypatch)

        assert get_report_url("") == "https://spruce.mongodb.com/task/task_1/files?execution=3"

    def test_falls_back_to_the_task_files_when_signing_fails(self, monkeypatch):
        self._credentials(monkeypatch)

        with patch("scripts.release.trivy_scan.boto3.client", side_effect=RuntimeError("boom")):
            url = get_report_url("trivy-image.json")

        assert url == "https://spruce.mongodb.com/task/task_1/files?execution=3"

    def test_empty_without_task_id(self):
        assert get_report_url("trivy-image.json") == ""


class TestRegistryUrl:
    DIGEST = "sha256:" + "c" * 64

    def test_ecr_image_with_digest(self):
        url = get_registry_url("268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-agent:1.0", self.DIGEST)

        assert url == (
            "https://us-east-1.console.aws.amazon.com/ecr/repositories/private/268558157000/dev/mongodb-agent"
            f"/_/image/{self.DIGEST}/details?region=us-east-1"
        )

    def test_ecr_repository_without_digest(self):
        url = get_registry_url("268558157000.dkr.ecr.eu-west-1.amazonaws.com/staging/mongodb-agent:1.0")

        assert url == (
            "https://eu-west-1.console.aws.amazon.com/ecr/repositories/private/268558157000/staging/mongodb-agent"
            "?region=eu-west-1"
        )

    def test_quay_image_with_digest(self):
        url = get_registry_url("quay.io/mongodb/mongodb-kubernetes:1.5.0", self.DIGEST)

        assert url == f"https://quay.io/repository/mongodb/mongodb-kubernetes/manifest/{self.DIGEST}"

    def test_quay_tag_without_digest(self):
        url = get_registry_url("quay.io/mongodb/mongodb-kubernetes:1.5.0")

        assert url == "https://quay.io/repository/mongodb/mongodb-kubernetes?tab=tags&tag=1.5.0"

    def test_unknown_registry_has_no_link(self):
        assert get_registry_url("registry.example.com/mongodb/image:1.0", self.DIGEST) == ""
        assert get_registry_url("image:tag") == ""


class TestImageDigest:
    def test_digest_from_repo_digests(self):
        report = {"Metadata": {"RepoDigests": ["reg/repo/image@sha256:abc"]}}

        assert get_image_digest(report) == "sha256:abc"

    def test_missing_metadata_or_digests(self):
        assert get_image_digest({}) == ""
        assert get_image_digest({"Metadata": None}) == ""
        assert get_image_digest({"Metadata": {"RepoDigests": []}}) == ""
        assert get_image_digest({"Metadata": {"RepoDigests": ["no-digest-here"]}}) == ""

    def test_parse_trivy_report_keeps_the_digest(self):
        report = {**SAMPLE_TRIVY_REPORT, "Metadata": {"RepoDigests": ["reg/repo/image@sha256:abc"]}}

        assert parse_trivy_report(report, "reg/repo/image:1.0", "linux/amd64").image_digest == "sha256:abc"


class TestShortImageName:
    def test_strips_registry_and_repository(self):
        assert short_image_name("268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent:1.0") == (
            "mongodb-agent:1.0"
        )

    def test_keeps_names_without_registry(self):
        assert short_image_name("image:tag") == "image:tag"


class TestResolveImageVersions:
    @pytest.fixture(autouse=True)
    def clean_env(self, monkeypatch):
        for name in (
            "OPERATOR_VERSION",
            "OM_VERSION",
            "OM_VERSIONS",
            "READINESS_PROBE_VERSION",
            "VERSION_UPGRADE_HOOK_VERSION",
            "AGENT_VERSION_OVERRIDE",
            "AGENT_VERSION",
        ):
            monkeypatch.delenv(name, raising=False)

    @pytest.mark.parametrize(
        "image,env_name",
        [
            ("operator", "OPERATOR_VERSION"),
            ("init-database", "OPERATOR_VERSION"),
            ("database", "OPERATOR_VERSION"),
            ("init-ops-manager", "OPERATOR_VERSION"),
            ("ops-manager", "OM_VERSION"),
            ("readiness-probe", "READINESS_PROBE_VERSION"),
            ("upgrade-hook", "VERSION_UPGRADE_HOOK_VERSION"),
            ("agent", "AGENT_VERSION"),
        ],
    )
    def test_resolves_version_from_environment(self, monkeypatch, image, env_name):
        monkeypatch.setenv(env_name, "v1")

        assert resolve_image_versions(image) == ["v1"]

    def test_agent_version_override_takes_precedence(self, monkeypatch):
        monkeypatch.setenv("AGENT_VERSION", "all")
        monkeypatch.setenv("AGENT_VERSION_OVERRIDE", "current")

        assert resolve_image_versions("agent") == ["current"]

    def test_ops_manager_resolves_several_versions(self, monkeypatch):
        monkeypatch.setenv("OM_VERSIONS", " 7.0.23 8.0.27  9.0.1")

        assert resolve_image_versions("ops-manager") == ["7.0.23", "8.0.27", "9.0.1"]

    def test_ops_manager_versions_take_precedence_over_single_version(self, monkeypatch):
        monkeypatch.setenv("OM_VERSION", "7.0.23")
        monkeypatch.setenv("OM_VERSIONS", "8.0.27 9.0.1")

        assert resolve_image_versions("ops-manager") == ["8.0.27", "9.0.1"]

    def test_raises_when_version_is_not_set(self):
        with pytest.raises(ValueError, match="Cannot resolve the version of image 'operator'"):
            resolve_image_versions("operator")

    def test_blank_value_is_not_a_version(self, monkeypatch):
        monkeypatch.setenv("OM_VERSIONS", "  ")

        with pytest.raises(ValueError, match="Cannot resolve the version of image 'ops-manager'"):
            resolve_image_versions("ops-manager")


class TestScanImages:
    def _args(self, **overrides):
        args = {
            "images": ["operator", "init-database"],
            "build_scenario": "staging",
            "version": None,
            "platform": "linux/amd64",
            "severity": "CRITICAL,HIGH",
            "output_dir": "/tmp/opencode/trivy-test-reports",
        }
        args.update(overrides)
        return type("Args", (), args)()

    @patch("scripts.release.trivy_scan.run_trivy_scan")
    @patch("scripts.release.trivy_scan.get_trivy_binary", return_value="trivy")
    def test_scans_every_image_and_returns_all_results(self, _binary, mock_run, monkeypatch):
        monkeypatch.setenv("OPERATOR_VERSION", "abc123")
        mock_run.side_effect = lambda _bin, ref, platform, *_: make_result(image_ref=ref, platform=platform, fixable=1)

        results = scan_images(self._args())

        assert [r.image_ref for r in results] == [
            "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes:abc123",
            "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-init-database:abc123",
        ]

    @patch("scripts.release.trivy_scan.run_trivy_scan")
    @patch("scripts.release.trivy_scan.get_trivy_binary", return_value="trivy")
    def test_scans_every_version_of_an_image(self, _binary, mock_run, monkeypatch):
        monkeypatch.setenv("OM_VERSIONS", "7.0.23 8.0.27 9.0.1")
        mock_run.side_effect = lambda _bin, ref, platform, *_: make_result(image_ref=ref, platform=platform, fixable=1)

        results = scan_images(self._args(images=["ops-manager"]))

        assert [short_image_name(r.image_ref) for r in results] == [
            "mongodb-enterprise-ops-manager-ubi:7.0.23",
            "mongodb-enterprise-ops-manager-ubi:8.0.27",
            "mongodb-enterprise-ops-manager-ubi:9.0.1",
        ]

    @patch("scripts.release.trivy_scan.run_trivy_scan")
    @patch("scripts.release.trivy_scan.get_trivy_binary", return_value="trivy")
    def test_a_failing_scan_does_not_stop_the_other_images(self, _binary, mock_run, monkeypatch):
        monkeypatch.setenv("OPERATOR_VERSION", "abc123")
        mock_run.side_effect = [RuntimeError("trivy scan failed"), make_result(image_ref="b:1", fixable=1)]

        results = scan_images(self._args())

        assert [r.image_ref for r in results] == ["b:1"]

    @patch("scripts.release.trivy_scan.run_trivy_scan")
    @patch("scripts.release.trivy_scan.get_trivy_binary", return_value="trivy")
    def test_an_unresolvable_version_does_not_stop_the_other_images(self, _binary, mock_run, monkeypatch):
        monkeypatch.delenv("OPERATOR_VERSION", raising=False)
        monkeypatch.setenv("READINESS_PROBE_VERSION", "rp1")
        mock_run.return_value = make_result(image_ref="rp:rp1", fixable=1)

        results = scan_images(self._args(images=["operator", "readiness-probe"]))

        assert [r.image_ref for r in results] == ["rp:rp1"]
        assert mock_run.call_count == 1


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


class TestMain:
    def _argv(self, monkeypatch, *extra):
        monkeypatch.setattr(sys, "argv", ["trivy_scan.py", "operator", "-b", "staging", "-v", "1.0", *extra])

    @patch("scripts.release.trivy_scan.send_slack_notification")
    @patch("scripts.release.trivy_scan.scan_images")
    def test_silent_when_no_findings(self, mock_scan, mock_send, monkeypatch, capsys):
        mock_scan.return_value = [ScanResult(image_ref="clean:1.0", platform="linux/amd64")]
        self._argv(monkeypatch)

        rc = main()

        assert rc == 0
        assert capsys.readouterr().out == ""
        mock_send.assert_not_called()

    @patch("scripts.release.trivy_scan.send_slack_notification")
    @patch("scripts.release.trivy_scan.scan_images")
    def test_lists_but_does_not_notify_when_nothing_is_fixable(self, mock_scan, mock_send, monkeypatch, capsys):
        mock_scan.return_value = [make_result(image_ref="init-database:75fa89bc", unfixable=12)]
        self._argv(monkeypatch)
        monkeypatch.setenv("requester", "commit")

        rc = main()

        assert rc == 0
        # still printed to the task log, but no Slack message is sent
        assert "- init-database:75fa89bc: 12 vulnerabilities: 0 fixable, 12 not fixable" in capsys.readouterr().out
        mock_send.assert_not_called()

    @patch("scripts.release.trivy_scan.send_slack_notification")
    @patch("scripts.release.trivy_scan.scan_images")
    def test_dry_run_does_not_print_a_message_when_nothing_is_fixable(self, mock_scan, mock_send, monkeypatch, capsys):
        mock_scan.return_value = [make_result(image_ref="init-database:75fa89bc", unfixable=12)]
        self._argv(monkeypatch, "--dry-run")

        main()

        assert "DRY RUN" not in capsys.readouterr().out

    @patch("scripts.release.trivy_scan.send_slack_notification")
    @patch("scripts.release.trivy_scan.scan_images")
    def test_notifies_when_findings(self, mock_scan, mock_send, monkeypatch):
        mock_scan.return_value = [parse_trivy_report(SAMPLE_TRIVY_REPORT, "image:tag", "linux/amd64")]
        self._argv(monkeypatch)
        monkeypatch.setenv("requester", "commit")

        rc = main()

        assert rc == 0
        mock_send.assert_called_once()

    @patch("scripts.release.trivy_scan.send_slack_notification")
    @patch("scripts.release.trivy_scan.scan_images")
    def test_fail_on_findings_returns_non_zero(self, mock_scan, mock_send, monkeypatch):
        mock_scan.return_value = [make_result(image_ref="image:tag", unfixable=1)]
        self._argv(monkeypatch, "--fail-on-findings")
        monkeypatch.setenv("requester", "commit")

        assert main() == 1
        mock_send.assert_not_called()

    @patch("scripts.release.trivy_scan.send_slack_notification")
    @patch("scripts.release.trivy_scan.scan_images")
    def test_sends_a_single_message_for_all_images(self, mock_scan, mock_send, monkeypatch, capsys):
        mock_scan.return_value = [
            make_result(image_ref="reg/operator:abc", fixable=3, unfixable=1),
            make_result(image_ref="reg/init-database:abc", unfixable=12),
            make_result(image_ref="reg/database:abc"),
            make_result(image_ref="reg/mongodb-agent:1.0", fixable=5),
        ]
        monkeypatch.setattr(
            sys, "argv", ["trivy_scan.py", "operator", "init-database", "database", "agent", "-b", "staging"]
        )
        monkeypatch.setenv("requester", "commit")

        rc = main()

        assert rc == 0
        mock_send.assert_called_once()
        text = mock_send.call_args.args[0]["blocks"][1]["text"]["text"]
        assert "- operator:abc: 4 vulnerabilities: 3 fixable" in text
        assert "- mongodb-agent:1.0: 5 vulnerabilities: 5 fixable" in text
        assert "not fixable" not in text
        # no fixable issues (or no issues at all): not in the Slack message
        assert "init-database" not in text
        assert "database:abc" not in text
        # the task log still lists every image with findings
        stdout_lines = [line for line in capsys.readouterr().out.splitlines() if line.startswith("- ")]
        assert len(stdout_lines) == 3

    def test_version_is_rejected_with_multiple_images(self, monkeypatch):
        monkeypatch.setattr(sys, "argv", ["trivy_scan.py", "operator", "agent", "-b", "staging", "-v", "1.0"])

        with pytest.raises(SystemExit):
            main()
