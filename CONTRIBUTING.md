# Summary

Contributing to the Mongodb Controllers for Kubernetes (MCK) project
Pull requests are always welcome, and the MCK dev team appreciates any help the community can give to help make MongoDB
better.

## PR Prerequisites

* Please ensure you have signed our Contributor Agreement. You can find
  it [here](https://www.mongodb.com/legal/contributor-agreement).
* Please ensure that all commits are signed.
* Create a changelog file that will describe the changes you made. Use the `skip-changelog` label if your changes do not
  require a changelog entry.

## Changelog files and Release Notes

Each Pull Request usually has a changelog file that describes the changes made in the PR using Markdown syntax.
Changelog files are placed in the `changelog/` directory and used to generate the Release Notes for the
upcoming release. Preview of the Release Notes is automatically added as comment to each Pull Request.
The changelog file needs to follow the naming convention
`YYYYMMDD-<change_kind>-<short-description>.md`. To create changelog file please use the
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
  title                 Title for the changelog entry

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
