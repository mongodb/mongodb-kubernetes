#!/usr/bin/env bash

# Release changed charts with updated version inside Chart.yaml only(!)

# NOTE: assume GNU semantics for tools like awk, grep, etc

set -o errexit
set -o nounset
set -o pipefail

mark_latest=${MARK_LATEST:-true}
skip_execution=${SKIP_EXECUTION:-false}

main() {
    local version=${VERSION:-v1.2.1}
    local charts_dir=${CHART_DIR:-charts}
    local target=("$@")
    local owner=${OWNER:-}
    local repo=${REPO:-}

    : "${CR_TOKEN:?Environment variable CR_TOKEN must be set}"

    print_line_separator
    echo 'Found target folders for release...'
    if [[ -z "${target[*]}" ]]; then
        mapfile -t target< <(find "$charts_dir" -maxdepth 2 -type f -name Chart.yaml | awk -F / '{print $2}')
    fi

    print_line_separator
    echo "Target folders: " "${target[@]}"
    release_charts_inside_folders "${target[@]}"

    # double check targets are released or report the error
    check_charts_released "${target[@]}"
}

print_line_separator() {
    echo "============================================="
}

release_charts_inside_folders() {
    local folders=("$@")
    local changed_charts=()

    prepare_helm_repo

    # form list of folders which was changed
    for folder in "${folders[@]}"; do
        [[ ! -f "$charts_dir/$folder/Chart.yaml" ]] && continue
        print_line_separator
        local chart_name
        local chart_version

        chart_name=$(read_chart_name "${charts_dir}/${folder}")
        chart_version=$(read_chart_version "${charts_dir}/${folder}")
        echo "Checking if \"$charts_dir/$folder\" has been released to the repo"

        # if chart is not released or folder has change, then remember as changed_charts
        if ! chart_released "${chart_name}" "${chart_version}"; then
            changed_charts+=("$folder")
        fi
    done
    echo "changed charts: " "${changed_charts[@]}"

    # continue only with changed charts
    if [[ -n "${changed_charts[*]}" ]]; then
        if [ "${DRYRUN}" == "true" ]; then
            echo "DRYRUN: Would have released charts" "${changed_charts[@]}"
        else
            release_changed_charts "${changed_charts[@]}"
        fi
    else
        echo "Nothing to do. No chart changes detected."
    fi
}

check_charts_released() {
    local folders=("$@")
    local unreleased_charts=()

    prepare_helm_repo

    # form a list of folders which were unreleased
    for folder in "${folders[@]}"; do
        [[ ! -f "$charts_dir/$folder/Chart.yaml" ]] && continue
        print_line_separator
        local chart_name
        local chart_version

        chart_name=$(read_chart_name "${charts_dir}/${folder}")
        chart_version=$(read_chart_version "${charts_dir}/${folder}")
        echo "Checking if \"$charts_dir/$folder\" has been released to the repo"

        if ! check_chart_version_released "${chart_name}" "${chart_version}"; then
            unreleased_charts+=("$chart_name")
        fi
    done

    if [[ -n "${unreleased_charts[*]}" ]]; then
        if [ "${DRYRUN}" == "true" ]; then
            echo "DRYRUN: would have not seen released charts for" "${unreleased_charts[@]}"
        else
            echo "FAIL: found unreleased charts:" "${unreleased_charts[@]}"
            exit 1
        fi
    else
        echo "PASS: all latest helm charts released for" "${folders[@]}"
    fi
}

check_chart_version_released() {
    local chart_name=$1
    local chart_version=$2
    local retries=30
    local pause=10

    echo "Checking helm chart ${chart_name} was released for version ${chart_version}"
    for ((i=0; i<retries; i++)); do
        update_helm_repo
        if chart_released "${chart_name}" "${chart_version}"; then
            return 0
        fi
        echo "Retrying to check on ${chart_name}:${chart_version} in ${pause} seconds..."
        sleep "${pause}"
    done
    return 1
}

read_chart_name() {
    local chart_path=$1
    awk '/^name: /{print $2}' "$chart_path/Chart.yaml"
}

read_chart_version() {
    local chart_path=$1
    awk '/^version: /{print $2}' "$chart_path/Chart.yaml"
}

reset_helm_repo() {
    helm repo remove mongodb
    prepare_helm_repo
}

update_helm_repo(){
    helm repo update mongodb
}

prepare_helm_repo() {
    helm repo add mongodb https://mongodb.github.io/helm-charts
    update_helm_repo
}

chart_released() {
    local chart_name=$1
    local version=$2

    helm search repo "mongodb/${chart_name}" --version "${version}" | grep -q "${chart_name}\s"
}

get_latest_tag(){
    local name=$1

    git fetch --tags > /dev/null 2>&1
    git tag -l --sort=-refname |grep "^$name-[0-9]*\." | head -1
}

release_changed_charts() {
    local changed_charts=("$@")

    helm repo update
    install_chart_releaser
    cleanup_releaser
    package_charts "${changed_charts[@]}"
    release_charts
    update_index
}

install_chart_releaser() {
    print_line_separator
    if [[ ! -d "$RUNNER_TOOL_CACHE" ]]; then
        echo "Cache directory '$RUNNER_TOOL_CACHE' does not exist" >&2
        exit 1
    fi

    local arch
    arch=$(uname -m)

    local cache_dir="$RUNNER_TOOL_CACHE/ct/$version/$arch"
    if [[ ! -d "$cache_dir" ]]; then
        mkdir -p "$cache_dir"

        echo "Installing chart-releaser..."
        curl -sSLo cr.tar.gz "https://github.com/helm/chart-releaser/releases/download/$version/chart-releaser_${version#v}_linux_amd64.tar.gz"
        tar -xzf cr.tar.gz -C "$cache_dir"
        rm -f cr.tar.gz

        echo 'Adding cr directory to PATH...'
    fi
    export PATH="$cache_dir:$PATH"
}

cleanup_releaser() {
    rm -rf .cr-release-packages
    mkdir -p .cr-release-packages

    rm -rf .cr-index
    mkdir -p .cr-index
}

package_charts() {
    print_line_separator
    local changed_charts=("$@")
    for chart in "${changed_charts[@]}"; do
        folder="$charts_dir/$chart"
        if [[ -d "$folder" ]]; then
            local args=("$folder" --package-path .cr-release-packages)

            echo "Packaging chart folder '$folder'..."
            cr package "${args[@]}"
        else
            echo "Chart '$folder' no longer exists in repo. Skipping it..."
        fi
    done
}

release_charts() {
    local args=(-o "$owner" -r "$repo" -c "$(git rev-parse HEAD)" --make-release-latest "${mark_latest}")

    echo 'Releasing charts...'
    cr upload "${args[@]}"
}

update_index() {
    local args=(-o "$owner" -r "$repo" --push --remote origin --pages-branch gh-pages)

    git fetch
    echo 'Updating charts repo index...'
    cr index "${args[@]}"
}

if [[ "${skip_execution}" == "true" ]]; then
  echo "Skipping execution as per SKIP_EXECUTION=${skip_execution}"
else
  mapfile -t target< <(echo "$1" )
  main "${target[@]}"
fi
