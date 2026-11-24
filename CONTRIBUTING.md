# Contributing to MongoDB Controllers for Kubernetes (MCK)

MCK is developed and maintained by a dedicated MongoDB engineering team. We are not accepting pull requests from
external contributors at this time.

Bug reports, questions, and ideas for changes are welcome as
[GitHub issues](https://github.com/mongodb/mongodb-kubernetes/issues/new/choose). We read every issue, and an issue is
the best place to discuss a change with the team.

The rest of this document describes the development and release process for MongoDB engineers working in this
repository.

## PR Prerequisites (MongoDB engineers)

* Please ensure that all commits are signed.
* Create a changelog file that will describe the changes you made. Use the `skip-changelog` label if your changes do not
  require a changelog entry.

## Stale Pull Requests

A daily workflow marks pull requests with no activity for 30 days as `stale` and adds a comment. If there is still no
activity 30 days later, the pull request is closed; it can be reopened at any time. Any comment or push removes the
`stale` label and restarts the clock. Drafts and stacked pull requests are included. To keep a long-running pull request
open, add the `do-not-stale` label, or ask a maintainer to add it.

## Kubernetes and OpenShift versions (maintainers)

Declared Kubernetes support and the OpenShift version we track for engineering checks live in **`kubernetes-versions.json`** at the repo root. Maintainer-facing details, how **`release.json`** differs from that file, and how to run the policy script are documented in **[docs/dev/kubernetes-version-support.md](docs/dev/kubernetes-version-support.md)**.

## Changelog files and Release Notes

Each Pull Request usually has a changelog file that describes the changes made in the PR using Markdown syntax.
Changelog files are placed in the `changelog/` directory and used to generate the Release Notes for the
upcoming release. Preview of the Release Notes is automatically added as comment to each Pull Request.
The changelog file needs to follow the naming convention
`YYYYMMDD-<change_kind>-<short-title>.md`. To create changelog file please use the
`scripts/release/create_changelog.py` script. Example usage:

```console
python3 -m scripts.release.create_changelog --kind fix "Fix that I want to describe in the changelog"
```

For more options, run the script with `--help`:

```console
python3 -m scripts.release.create_changelog --help
usage: create_changelog.py [-h] [-c ] [-d ] [-e] -k  title

Utility to easily create a new changelog entry file.

positional arguments:
  title                 Title used in changelog filename and passed as initial file contents

options:
  -h, --help            show this help message and exit
  -c, --changelog-path
                        Path to the changelog directory relative to a current working directory. Default is 'changelog/'
  -d, --date            Date in 'YYYY-MM-DD' format to use for the changelog entry. Default is today's date
  -e, --editor          Open the created changelog entry in the default editor (if set, otherwise uses 'vi'). Default is True
  -k, --kind            Kind of the changelog entry:
                          - 'prelude' for prelude entries
                          - 'breaking' for breaking change entries
                          - 'feature' for feature entries
                          - 'fix' for bugfix entries
                          - 'other' for other entries
```
