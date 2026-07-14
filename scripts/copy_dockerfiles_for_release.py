#!/usr/bin/env python3
"""
Copy Dockerfiles from docker/ to public/dockerfiles/ for a release.

Reads versions from release.json automatically. Only copies files that do not
already exist, so it is safe to run multiple times.

Images handled:
  - mongodb-kubernetes-database
  - mongodb-kubernetes-init-database
  - mongodb-kubernetes-init-appdb        (derived from init-database, different LABELs)
  - mongodb-kubernetes-init-ops-manager
  - mongodb-kubernetes                   (from docker/mongodb-kubernetes-operator/)
  - mongodb-agent                        (uses agentVersion from release.json)
  - mongodb-enterprise-ops-manager       (uses supportedImages.ops-manager.versions,
                                          adds only versions not already present)

Usage:
  python scripts/copy_dockerfiles_for_release.py
  python scripts/copy_dockerfiles_for_release.py --dry-run
"""

import argparse
import json
import os
import shutil
import subprocess


def get_repo_root() -> str:
    out = subprocess.check_output(["git", "rev-parse", "--show-toplevel"])
    return out.decode().strip()


def copy_dockerfile(src: str, dest_dir: str, dry_run: bool) -> None:
    dest = os.path.join(dest_dir, "Dockerfile")
    if not os.path.exists(src):
        print(f"  ERROR source not found: {src}")
        return
    if os.path.exists(dest):
        print(f"  SKIP (exists): {dest}")
        return
    if dry_run:
        print(f"  [dry-run] {src}")
        print(f"         -> {dest}")
        return
    os.makedirs(dest_dir, exist_ok=True)
    shutil.copy2(src, dest)
    print(f"  COPIED -> {dest}")


def derive_appdb_dockerfile(init_db_src: str, dest_dir: str, dry_run: bool) -> None:
    """Copy init-database Dockerfile, replacing its LABELs with init-appdb ones."""
    dest = os.path.join(dest_dir, "Dockerfile")
    if not os.path.exists(init_db_src):
        print(f"  ERROR source not found: {init_db_src}")
        return
    if os.path.exists(dest):
        print(f"  SKIP (exists): {dest}")
        return

    with open(init_db_src) as f:
        content = f.read()

    content = (
        content.replace('name="MongoDB Kubernetes Init Database"', 'name="MongoDB Kubernetes Init AppDB"')
        .replace(
            'version="mongodb-kubernetes-init-database-${version}"',
            'version="mongodb-kubernetes-init-appdb-${version}"',
        )
        .replace('summary="MongoDB Kubernetes Database Init Image"', 'summary="MongoDB Kubernetes AppDB Init Image"')
        .replace(
            'description="Startup Scripts for MongoDB Enterprise Database"',
            'description="Startup Scripts for MongoDB Enterprise Application Database for Ops Manager"',
        )
    )

    if dry_run:
        print(f"  [dry-run] derived from {init_db_src}")
        print(f"         -> {dest}")
        return
    os.makedirs(dest_dir, exist_ok=True)
    with open(dest, "w") as f:
        f.write(content)
    print(f"  DERIVED -> {dest}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--dry-run", action="store_true", help="Preview without writing any files")
    args = parser.parse_args()

    repo = get_repo_root()
    release = json.load(open(os.path.join(repo, "release.json")))

    docker_base = os.path.join(repo, "docker")
    public_base = os.path.join(repo, "public", "dockerfiles")
    operator_ver = release["mongodbOperator"]

    if args.dry_run:
        print("DRY RUN — no files will be written\n")

    print(f"Operator version: {operator_ver}\n")

    # ── Images versioned by the operator version ──────────────────────────────
    operator_versioned = [
        ("mongodb-kubernetes-database", "mongodb-kubernetes-database"),
        ("mongodb-kubernetes-init-database", "mongodb-kubernetes-init-database"),
        ("mongodb-kubernetes-init-ops-manager", "mongodb-kubernetes-init-ops-manager"),
        ("mongodb-kubernetes-operator", "mongodb-kubernetes"),
    ]
    for docker_dir, public_name in operator_versioned:
        src = os.path.join(docker_base, docker_dir, "Dockerfile")
        dest_dir = os.path.join(public_base, public_name, operator_ver, "ubi")
        print(f"{public_name}  ({operator_ver}):")
        copy_dockerfile(src, dest_dir, args.dry_run)

    # ── init-appdb: derived from init-database ────────────────────────────────
    print(f"mongodb-kubernetes-init-appdb  ({operator_ver}):")
    init_db_src = os.path.join(docker_base, "mongodb-kubernetes-init-database", "Dockerfile")
    derive_appdb_dockerfile(
        init_db_src, os.path.join(public_base, "mongodb-kubernetes-init-appdb", operator_ver, "ubi"), args.dry_run
    )

    # ── mongodb-agent ─────────────────────────────────────────────────────────
    agent_ver = release["agentVersion"]
    print(f"\nmongodb-agent  ({agent_ver}):")
    agent_src = os.path.join(docker_base, "mongodb-agent", "Dockerfile")
    copy_dockerfile(agent_src, os.path.join(public_base, "mongodb-agent", agent_ver, "ubi"), args.dry_run)

    # ── mongodb-enterprise-ops-manager ────────────────────────────────────────
    om_src = os.path.join(docker_base, "mongodb-enterprise-ops-manager", "Dockerfile")
    om_versions = release["supportedImages"]["ops-manager"]["versions"]
    existing_om = set(os.listdir(os.path.join(public_base, "mongodb-enterprise-ops-manager")))
    new_om_versions = [v for v in om_versions if v not in existing_om]

    if new_om_versions:
        print(f"\nmongodb-enterprise-ops-manager ({len(new_om_versions)} new versions):")
        for v in new_om_versions:
            dest_dir = os.path.join(public_base, "mongodb-enterprise-ops-manager", v, "ubi")
            print(f"  version {v}:")
            copy_dockerfile(om_src, dest_dir, args.dry_run)
    else:
        print("\nmongodb-enterprise-ops-manager: all versions already present, nothing to add")

    print("\nDone.")


if __name__ == "__main__":
    main()
