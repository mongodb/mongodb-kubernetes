#!/usr/bin/env bash
set -Eeou pipefail

export GOPATH=${GOPATH:-$workdir}

if [[ -z "${EVERGREEN_MODE:-}" ]]; then
  git_last_changed=$(git diff --cached --name-only --diff-filter=ACM)
else
  git_last_changed=$(git diff --name-only --diff-filter=ACM origin/master)
fi

if ! [[ -x "$(command -v staticcheck)" ]]; then
    echo "installing staticcheck..."
    go install honnef.co/go/tools/cmd/staticcheck@latest
  else
    echo "staticcheck is already installed"
fi

if ! [[ -x "$(command -v gofumpt)" ]]; then
    echo "installing gofumpt..."
    go install mvdan.cc/gofumpt@latest
  else
    echo "gofumpt is already installed"
fi

# important to turn off modules to ensure a global install
if ! [[ -x "$(command -v goimports)" ]]; then
    echo "installing goimports..."
    go install golang.org/x/tools/cmd/goimports
fi

# format code with gofumpt
echo "Running gofumpt..."
PATH=$GOPATH/bin:$PATH gofumpt -l -w .

# after running gofumpt, gofmt should not modify anything
echo "Running gofmt and comparing the result with gofumpt..."
unformatted_files=$(gofmt -l .)
if [[ -n "$unformatted_files" ]]; then
    echo "The following files need further formatting by gofumpt:"
    echo "$unformatted_files"
    echo "Exiting..."
    exit 1
fi

# lint code with staticcheck, configuration file is ops-manager-kubernetes/staticcheck.conf
echo "Running staticcheck..."
PATH=$GOPATH/bin:$PATH staticcheck ./...

echo "Go Version: $(go version)"

# Run goimports and go vet on all go modified files
for file in $( echo "$git_last_changed" | grep '\.go$')
do
    # goimports
    to_fix=$(PATH=$GOPATH/bin:$PATH  goimports -l "${file}")
    if [[ -n "${to_fix}" ]]
    then
        echo "formatting ${to_fix}"
        PATH=$GOPATH/bin:$PATH  goimports -w "${to_fix}"
        git add "${to_fix}"
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
    # shellcheck disable=SC2207
    packages_to_analyze=($(echo "${packages_to_analyze[@]}" | tr ' ' '\n' | sort -u | tr '\n' ' '))
    # shellcheck disable=SC2128
    for directory in $packages_to_analyze
    do
        output=$(go vet "${repo_root}/${directory}")
        if test -n "$output"
        then
            echo "$output"
            exit 1
        fi
    done
fi

