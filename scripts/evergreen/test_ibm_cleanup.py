import os
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def executable(path, text):
    path.write_text("#!/usr/bin/env bash\nset -eu\n" + text)
    path.chmod(0o755)


def test_minikube_teardown_uses_root_context(tmp_path):
    (tmp_path / "scripts/dev").mkdir(parents=True)
    (tmp_path / "scripts/dev/set_env_context.sh").write_text(
        'export KUBE_ENVIRONMENT_NAME=minikube\nexport PROJECT_DIR="$PWD"\n'
    )
    bindir = tmp_path / "bin"
    bindir.mkdir()
    executable(
        bindir / "sudo",
        'if [ "${1:-}" = "-n" ]; then shift; fi\n' "export TEST_ROOT=1\n" 'exec env "$@"\n',
    )
    executable(bindir / "podman", "exit 1\n")
    executable(bindir / "ip", "exit 0\n")
    executable(
        bindir / "minikube",
        'test "${TEST_ROOT:-}" = 1\ntest "${TMPDIR:-}" = /root/.minikube-tmp\ntest "$*" = delete\n',
    )
    subprocess.run(
        ["bash", str(ROOT / "scripts/evergreen/teardown_kubernetes_environment.sh")],
        cwd=tmp_path,
        env={**os.environ, "PATH": f"{bindir}:{os.environ['PATH']}"},
        check=True,
    )


def test_runtime_failure_does_not_reset_or_kill_host_resources(tmp_path):
    executable(tmp_path / "crun", 'echo "crun version test"\n')
    executable(
        tmp_path / "sudo",
        'echo "$*" >> "$COMMAND_LOG"\n'
        'case "$*" in\n'
        '  "podman run "*) echo "image pull failed" >&2; exit 42 ;;\n'
        '  "tee "*) while read -r line; do :; done ;;\n'
        '  "podman --version") echo "podman version test" ;;\n'
        "esac\n",
    )
    log = tmp_path / "commands"
    result = subprocess.run(
        ["bash", str(ROOT / "scripts/dev/setup_ibm_container_runtime.sh")],
        env={**os.environ, "PATH": f"{tmp_path}:{os.environ['PATH']}", "COMMAND_LOG": str(log)},
        capture_output=True,
        text=True,
    )
    commands = log.read_text()
    assert "tee /etc/containers/containers.conf" in commands
    assert commands.index("tee /etc/containers/containers.conf") < commands.index("podman run")
    assert "system reset" not in commands
    assert "pgrep" not in commands
    assert "prune" not in commands
    assert "/run/crun" not in commands
    assert result.returncode != 0
    assert "image pull failed" in result.stderr
