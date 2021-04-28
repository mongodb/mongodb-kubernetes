#!/bin/bash

set -Eeou pipefail
set -x

function black_formatting()
{
    # installing Black
    if ! command -v "black" > /dev/null; then
        pip3 install -r docker/mongodb-enterprise-tests/requirements-dev.txt
    fi

    # Black formatting of every python file that was changed
    for file in $(git diff --cached --name-only --diff-filter=ACM | grep '\.py$')
    do
        black -q "$file"
        git add "$file"
    done
}

black_formatting

# pre-commit hook will go vet all the files being committed
# and also format them with go imports

export GO111MODULE=on

if [[ $(git diff --cached --name-only --diff-filter=ACM | grep -c '^go.*') == "1" ]]; then
  echo 'ERROR: Should change both or neither of go.mod and go.sum'
  exit 1
fi

if find . -name "Makefile"|grep -v vendor|xargs grep "\${"
then
    echo 'ERROR: Makefiles should NEVER contain curly brackets variables'
    exit 1
fi

mkdir -p "$(go env GOPATH)/bin"

# important to turn off modules to ensure a global install
if ! [[ -x "$(command -v goimports)" ]]; then
    echo "installing goimports..."
    GO111MODULE=off go get golang.org/x/tools/cmd/goimports
fi

if ! [[ -x "$(command -v staticcheck)" ]]; then
    echo "installing go tools..."
    GO111MODULE=off go get -u honnef.co/go/tools/...
fi

# check for dead code
staticcheck -checks U1000,SA4006,ST1019,S1005,S1019 ./controllers/...

if ! command -v "shellcheck" > /dev/null; then
    echo "Please install shellcheck"
    exit 1
fi

# Makes sure there are not erroneous kubebuilder annotations that can
# end up in CRDs as descriptions.
if grep "// kubebuilder" ./* -r --include=\*.go ; then
    echo "Found an erroneous kubebuilder annotation"
    exit 1
fi

# run shellcheck on all modified shell scripnts
for file in $(git diff --cached --name-only --diff-filter=ACM | grep -v '\.go$' | grep -v '\.py' | grep -v '\.yaml' | grep -v '\.json')
do
    # check if bash script
    if head -1 "${file}" | grep "#!/usr/bin/env bash" > /dev/null
    then
        # see https://vaneyckt.io/posts/safer_bash_scripts_with_set_euxo_pipefail/
        if ! grep "set -Eeou pipefail" "${file}" > /dev/null
        then
            echo "set opts not set on ${file}"
            exit 1
        fi
        if ! shellcheck -x "${file}"; then
            echo "shellcheck failed on ${file}"
            exit 1
        fi
    fi
done

# Run goimports and go vet on all go modified files
exitcode=0
for file in $(git diff --cached --name-only --diff-filter=ACM | grep '\.go$')
do
    # goimports
    to_fix=$(goimports -l "${file}")
    if [[ -n "${to_fix}" ]]
    then
        echo "formatting ${to_fix}"
        goimports -w "${to_fix}"
        git add "${to_fix}"
        exitcode=1
    fi
    # govet: build list of packages to analyze
    packages_to_analyze+=("$(dirname "${file}")" )
    git add "$file"
done

# go vet is ran on whole directories as it can't be run on individual files
# If a package is split into multiple files go vet has no knowledge of it
# and will complain about undefined names that are instead defined in other files
packages_to_analyze=()
repo_root=$(git rev-parse --show-toplevel)
if [ ${#packages_to_analyze[@]} -ne 0 ]; then
    # Remove duplicate directories
    packages_to_analyze=($(echo "${packages_to_analyze[@]}" | tr ' ' '\n' | sort -u | tr '\n' ' '))
    for directory in $packages_to_analyze
    do
        output=$(go vet "${repo_root}/${directory}")
        if test -n "$output"
        then
            echo "$output"
            exitcode=1
        fi
    done
fi

exit $exitcode
