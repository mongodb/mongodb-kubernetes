"""Guard: overlay-mounted tooling scripts must not reference worktree-local
bootstrap files (wt-ctl, devenv, switch_context.sh, set_env_context.sh).

The devcontainer bind-mounts every ``tooling-overlay.manifest`` entry over the
target checkout, which may be plain master, and deliberately does NOT mount
the bootstrap chain (those files locate each other by path; the orchestrator
drives them from ``/mck-tooling`` explicitly). A reference like
``${PROJECT_DIR}/scripts/dev/wt-ctl`` inside an overlay file therefore breaks
in-container flows on tooling-free worktrees — e.g. ``make prepare-local-e2e``
failed with exit 127 at the end of ``run_multi_cluster_kube_config_creator``.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
MANIFEST = REPO / "scripts" / "dev" / "tooling-overlay.manifest"

_BOOTSTRAP_REF = re.compile(
    r"\$\{PROJECT_DIR\}/scripts/dev/(?:wt-ctl|devenv|switch_context\.sh|set_env_context\.sh)"
)


class OverlayManifestTests(unittest.TestCase):
    def _manifest_entries(self) -> list[str]:
        return [
            line.strip()
            for line in MANIFEST.read_text().splitlines()
            if line.strip() and not line.lstrip().startswith("#")
        ]

    def test_overlay_files_prefer_tooling_mount_over_worktree_local_bootstrap(self) -> None:
        offenders: list[str] = []
        for rel in self._manifest_entries():
            path = REPO / rel
            if not path.is_file():
                continue
            text = path.read_text(errors="replace")
            # Comments explaining the fallback shouldn't count as executable refs.
            code = "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))
            for match in _BOOTSTRAP_REF.finditer(code):
                tooling_ref = code.find("/mck-tooling/scripts/dev/")
                if tooling_ref == -1 or tooling_ref > match.start():
                    offenders.append(f"{rel}: {match.group(0)} (no earlier /mck-tooling preference)")
        self.assertEqual(
            offenders,
            [],
            msg=(
                "overlay files reference worktree-local bootstrap paths without "
                "preferring the /mck-tooling mount first:\n" + "\n".join(offenders)
            ),
        )


if __name__ == "__main__":
    unittest.main()
