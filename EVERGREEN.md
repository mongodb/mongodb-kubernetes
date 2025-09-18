# Evergreen CI/CD Configuration Guide

Each variant needs to be tagged with one or more tags referencing related build scenario:
 - `pr_patch`: for builds triggered by GitHub PRs
 - `staging`: for builds triggered when merging to master or release branch
 - `release`: for builds triggered on operator git tags

For variants that are **only** triggered manually (patch) or by PCT we should use `manual_patch` tag.
Examples: `migrate_all_agents`, `e2e_operator_perf` or `publish_om80_images`.

This configuration allows us to run all the associated tasks for each scenario from evergreen command line.
This is especially helpful when making changes to `staging` or `release` variants and testing them using Evergreen
command line patches. For example there is no other way to trigger tasks that are run on merges to master other than
combining them together using aliases. The same applies for tasks being run on git tags.

See https://docs.devprod.prod.corp.mongodb.com/evergreen/Project-Configuration/Project-and-Distro-Settings#project-aliases

# Running Evergreen build scenarios

Use `BUILD_SCENARIO` parameter to select the build scenario you want to run. Based on the scenario selected the
appropriate `REGISTRY` and `OPERATOR_VERSION` values will be selected. `REGISTRY` and `OPERATOR_VERSION` can be
individually overridden as well. Overriding `OPERATOR_VERSION` is mostly applicable for `release` scenario, where
there is no git tag to pick up.

## Example commands

### Full PR patch scenario:

```shell
evergreen patch -p mongodb-kubernetes -a pr_patch -d "Test PR patch build" -f -y -u --path .evergreen.yml --param BUILD_SCENARIO=pr_patch
```

### Full staging scenario:

```shell
evergreen patch -p mongodb-kubernetes -a staging -d "Test staging build" -f -y -u --path .evergreen.yml --param BUILD_SCENARIO=staging
```

### Full release scenario:

```shell
evergreen patch -p mongodb-kubernetes -a release -d "Test release build" -f -y -u --path .evergreen.yml --param BUILD_SCENARIO=release --param OPERATOR_VERSION=1.3.0-rc
```
