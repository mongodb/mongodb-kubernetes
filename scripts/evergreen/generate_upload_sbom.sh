#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

platforms=("linux/arm64" "linux/amd64")
image_pull_spec=""
image_name=""
tag_name=""
repo_name=""
bucket_name=""
registry_name=""
s3_path=""

function usage() {
  echo "Generates and uploads an SBOM to an S3 bucket.

Usage:
  generate_upload_sbom.sh [-h]
  generate_upload_sbom.sh -i <image_name> -b <bucket_name>

Options:
  -h                   (optional) Shows this screen.
  -i <image_name>      (required) Image to be processed.
  -b                   (required) S3 bucket name.
  -p                   (optional) An array of platforms, for example 'linux/arm64,linux/amd64'. The script **doesn't** fail if a particular architecture is not found.
"
}

function validate() {
  if ! command -v aws &> /dev/null
  then
    echo "AWS CLI not found. Please install the AWS CLI before running this script."
    exit 1
  fi
  if [ -z "$image_pull_spec" ]; then
    echo "Missing image"
    usage
    exit 1
  fi
  if [ -z "$bucket_name" ]; then
      echo "Missing bucket name"
      usage
      exit 1
    fi
}

function generate_sbom() {
  local image_pull_spec=$1
  local platform=$2
  local file_name=$3
  set +Ee
  docker sbom --platform "$platform" -o "$file_name" --format "cyclonedx-json" "$image_pull_spec"
  docker_sbom_return_code=$?
  set -Ee
  if ((docker_sbom_return_code != 0)); then
      echo "Image $image_pull_spec with platform $platform doesn't exist. Ignoring."
      return 1
  fi
  return 0
}

while getopts ':p:i:b:h' opt; do
  case $opt in
    i) image_pull_spec=$OPTARG ;;
    b) bucket_name=$OPTARG ;;
    p) IFS=',' read -ra platforms <<< "$OPTARG" ;;
    h) usage && exit 0;;
    *) usage && exit 0;;
  esac
done
shift "$((OPTIND - 1))"

validate

# To follow the logic here, please check https://www.gnu.org/software/bash/manual/html_node/Shell-Parameter-Expansion.html
image_name="${image_pull_spec##*/}"
image_name="${image_name%%:*}"

registry_name="${image_pull_spec%%/*}"

repo_name=${image_pull_spec%:*}
repo_name="${repo_name%/*}"
repo_name="${repo_name##*/}"

tag_name=${image_pull_spec##*:}

s3_path="s3://${bucket_name}/sboms/$registry_name/$repo_name/$image_name/$tag_name"

echo "Generating and uploading SBOM for $image_pull_spec"
echo "Image Pull Spec: $image_pull_spec"
echo "Registry: $registry_name"
echo "Repo: $repo_name"
echo "Image: $image_name"
echo "Tag: $tag_name"
echo "Platforms:" "${platforms[@]}"
echo "S3 Path: $s3_path"

for platform in "${platforms[@]}"; do
  s3_path_platform_dependent="$s3_path/${platform////_}"
  file_name="${image_name}_${tag_name}_${platform////_}.json"
  echo "Generating SBOM for $image_pull_spec ($platform) and uploading to $s3_path_platform_dependent"

  if generate_sbom "$image_pull_spec" "$platform" "$file_name"; then
    echo "Enabling S3 Bucket ($bucket_name) versioning"
    aws s3api put-bucket-versioning --bucket "${bucket_name}" --versioning-configuration Status=Enabled

    echo "Copying SBOM file $file_name to $s3_path_platform_dependent"
    aws s3 cp "$file_name" "$s3_path_platform_dependent"
  fi
  echo "Done generating and uploading SBOM for $image_pull_spec"
done
