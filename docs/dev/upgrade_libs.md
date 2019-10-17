# Upgrading libraries

## Prerequisites

- be sure to have activated your pip environment (to use jinja2)

## Upgrading

- Modify `scripts/dev/go.mod.jinja` (only if upgrading non-kubernetes libs)
- Run the upgrade script with the target kubernetes version `scripts/dev/update_k8s_version_go_mod.py 1.15` (use `--debug` for debug output)
    - Note that major.minor version is prefered as the script will upgrade all patch versions in this case
    - So if you want to upgrade from 1.15.3 to 1.15.4, just run the script passing `1.15` as argument as it will get the latest 1.15 version
    - Passing the patch version is supported and does not upgrade patch versions
    - If upgrading non-kubernetes libs, please pass the current kubernetes major.minor version as argument
- `git add .` (so that you do not forget to add go.mod and go.sum)
