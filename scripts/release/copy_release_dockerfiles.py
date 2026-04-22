#!/usr/bin/env python3
"""Copy release Dockerfiles from docker/ into <dest>/<image>/<version>/ubi/Dockerfile.

Source paths come from build_info.json via ``load_build_info(RELEASE)``; the
public dir layout is captured locally in PUBLIC_DIR_BY_KEY.

Usage (from repo root):
    python3 -m scripts.release.copy_release_dockerfiles --version 1.8.1
    python3 -m scripts.release.copy_release_dockerfiles --version 1.8.1 --dest some/other/dir
"""
import argparse
import shutil
from pathlib import Path

from scripts.release.build.build_info import (
    AGENT_IMAGE,
    DATABASE_IMAGE,
    INIT_DATABASE_IMAGE,
    INIT_OPS_MANAGER_IMAGE,
    OPERATOR_IMAGE,
    OPS_MANAGER_IMAGE,
    load_build_info,
)
from scripts.release.build.build_scenario import BuildScenario

# Build-info key → list of public Dockerfile dirs fed by its Dockerfile.
# init-database is also published as init-appdb (same source, two repos).
# REVIEW: the init-database → init-appdb dual-publish is not represented in
# build_info.json today. If it is captured elsewhere in the pipeline, this
# table should defer to that source of truth instead of duplicating it here.
PUBLIC_DIR_BY_KEY: dict[str, list[str]] = {
    OPERATOR_IMAGE: ["mongodb-kubernetes"],
    DATABASE_IMAGE: ["mongodb-kubernetes-database"],
    INIT_DATABASE_IMAGE: ["mongodb-kubernetes-init-database", "mongodb-kubernetes-init-appdb"],
    INIT_OPS_MANAGER_IMAGE: ["mongodb-kubernetes-init-ops-manager"],
    AGENT_IMAGE: ["mongodb-agent"],
    OPS_MANAGER_IMAGE: ["mongodb-enterprise-ops-manager"],
}


def repo_basename_for_public_dir(public_dir: str) -> str:
    """Quay repository basename for a public Dockerfile dir.

    REVIEW: ops-manager publishes under a ``-ubi`` suffix while its public
    Dockerfile dir drops it. This quirk is kept script-local for now; it
    could move into build_info.json as a declarative field.
    """
    if public_dir == "mongodb-enterprise-ops-manager":
        return f"{public_dir}-ubi"
    return public_dir


def copy_dockerfiles(version: str, dest_root: Path) -> list[Path]:
    bi = load_build_info(BuildScenario.RELEASE)
    written: list[Path] = []
    for key, public_dirs in PUBLIC_DIR_BY_KEY.items():
        src = Path(bi.images[key].dockerfile_path)
        if not src.is_file():
            raise FileNotFoundError(f"{key}: source Dockerfile not found at {src}")
        for public_dir in public_dirs:
            dst = dest_root / public_dir / version / "ubi" / "Dockerfile"
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, dst)
            written.append(dst)
    return written


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--version", required=True, help="Release version, e.g. 1.8.1")
    parser.add_argument(
        "--dest",
        default="public/dockerfiles",
        help="Target root under which <image>/<version>/ubi/Dockerfile is written (default: %(default)s)",
    )
    args = parser.parse_args()

    written = copy_dockerfiles(args.version, Path(args.dest))
    for p in written:
        print(f"wrote {p}")
    print(f"{len(written)} Dockerfile(s) copied for version {args.version}")


if __name__ == "__main__":
    main()
