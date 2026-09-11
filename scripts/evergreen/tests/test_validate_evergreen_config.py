"""Tests for validate_evergreen_config.py (KUBE-443 evergreen-validate gate)."""

from scripts.evergreen.validate_evergreen_config import (
    UNUSED_TASK_ALLOWLIST,
    format_report,
    main,
    parse_validation_output,
)

STRICT_LINE = (
    "WARNING: strict unmarshalling YAML: load project error(s): unmarshalling parser project "
    "from YAML: error unmarshalling yaml strict: yaml: unmarshal errors:\n"
    "  line 2023: field max_hosts not found in type model.copyType"
)


def _unused_line(task: str) -> str:
    return f"WARNING: task '{task}' defined but not used by any variants; consider using or disabling"


def _allowlist_output() -> str:
    """Reproduce the real validator output: one 'unused' warning per allowlisted task + the OK footer."""
    lines = [_unused_line(task) for task in UNUSED_TASK_ALLOWLIST]
    lines.append(".evergreen.yml is valid with warnings")
    return "\n".join(lines)


class TestParseValidationOutput:
    def test_clean_output_with_empty_allowlist_is_ok(self):
        result = parse_validation_output(".evergreen.yml is valid\n", allowlist={})
        assert result.ok

    def test_clean_output_with_nonempty_allowlist_is_stale(self):
        # A clean output but a non-empty allowlist means every entry is stale -> gate fails
        # until the allowlist is emptied. Keeps the accepted-warning set honest.
        result = parse_validation_output(".evergreen.yml is valid\n")
        assert not result.ok
        assert result.stale_allowlist == sorted(UNUSED_TASK_ALLOWLIST)

    def test_full_allowlist_output_is_ok(self):
        result = parse_validation_output(_allowlist_output())
        assert result.ok, format_report(result)
        assert result.strict_errors == []
        assert result.unexpected_unused == []
        assert result.stale_allowlist == []
        assert result.other_warnings == []

    def test_strict_error_fails(self):
        result = parse_validation_output(_allowlist_output() + "\n" + STRICT_LINE)
        assert not result.ok
        assert any("max_hosts" in e for e in result.strict_errors)

    def test_unlisted_unused_task_fails(self):
        output = _allowlist_output() + "\n" + _unused_line("e2e_brand_new_task")
        result = parse_validation_output(output)
        assert not result.ok
        assert result.unexpected_unused == ["e2e_brand_new_task"]

    def test_allowlisted_task_is_expected(self):
        # A single allowlisted task warning, but the rest of the allowlist is then "stale".
        one = next(iter(UNUSED_TASK_ALLOWLIST))
        result = parse_validation_output(_unused_line(one))
        assert result.unexpected_unused == []
        assert one not in result.stale_allowlist

    def test_stale_allowlist_entry_is_fatal(self):
        dropped = "e2e_standalone_groups"
        assert dropped in UNUSED_TASK_ALLOWLIST
        lines = [_unused_line(t) for t in UNUSED_TASK_ALLOWLIST if t != dropped]
        result = parse_validation_output("\n".join(lines))
        assert not result.ok
        assert result.stale_allowlist == [dropped]

    def test_unrecognized_warning_fails(self):
        output = _allowlist_output() + "\nWARNING: build variant 'x' has duplicate display name 'y'"
        result = parse_validation_output(output)
        assert not result.ok
        assert len(result.other_warnings) == 1
        assert "duplicate display name" in result.other_warnings[0]

    def test_custom_allowlist_argument(self):
        result = parse_validation_output(_unused_line("only_task"), allowlist={"only_task": "reason"})
        assert result.ok


class TestFormatReport:
    def test_ok_report_mentions_ok(self):
        assert "OK" in format_report(parse_validation_output(_allowlist_output()))

    def test_failing_report_lists_offenders(self):
        report = format_report(parse_validation_output(_unused_line("e2e_rogue")))
        assert "e2e_rogue" in report


class TestMain:
    def _stub_cli(self, monkeypatch):
        monkeypatch.setattr(
            "scripts.evergreen.validate_evergreen_config.shutil.which",
            lambda _name: "/usr/bin/evergreen",
        )

    def _stub_validate(self, monkeypatch, output, returncode):
        monkeypatch.setattr(
            "scripts.evergreen.validate_evergreen_config.run_evergreen_validate",
            lambda: (output, returncode),
        )

    def test_missing_cli_is_fatal(self, monkeypatch):
        monkeypatch.setattr(
            "scripts.evergreen.validate_evergreen_config.shutil.which",
            lambda _name: None,
        )
        assert main() == 1

    def test_nonzero_validator_exit_is_fatal(self, monkeypatch):
        self._stub_cli(monkeypatch)
        # otherwise-clean output, but the validator exits 1 -> gate must fail on the return code
        self._stub_validate(monkeypatch, _allowlist_output(), 1)
        assert main() == 1

    def test_auth_absent_skips_gate(self, monkeypatch):
        # CI buildhosts have no ~/.evergreen.yml, so `evergreen validate` exits 1 with a
        # config-file error; the gate must skip (0) rather than fail CI, while local devs
        # with auth still get real validation.
        self._stub_cli(monkeypatch)
        self._stub_validate(monkeypatch, "could not find client configuration file on the local system", 1)
        assert main() == 0

    def test_clean_output_with_zero_exit_returns_zero(self, monkeypatch):
        self._stub_cli(monkeypatch)
        self._stub_validate(monkeypatch, _allowlist_output(), 0)
        assert main() == 0

    def test_strict_error_with_zero_exit_still_fails(self, monkeypatch):
        self._stub_cli(monkeypatch)
        self._stub_validate(monkeypatch, _allowlist_output() + "\n" + STRICT_LINE, 0)
        assert main() == 1
