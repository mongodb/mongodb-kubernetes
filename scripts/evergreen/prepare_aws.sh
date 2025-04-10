#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

calculate_hours_since_creation() {
  if [[ $(uname) == "Darwin" ]]; then
    creation_timestamp=$(gdate -d "$1" +%s)
    current_timestamp=$(TZ="UTC" gdate +%s)
  else
    creation_timestamp=$(date -d "$1" +%s)
    current_timestamp=$(TZ="UTC" date +%s)
  fi
  echo $(( (current_timestamp - creation_timestamp) / 3600 ))
}

delete_buckets_from_file() {
  list_file=$1

  echo "Deleting buckets from file: ${list_file}"

  while IFS= read -r bucket_entry; do
    bucket_name=$(echo "${bucket_entry}" | jq -r '.Name')
    bucket_creation_date=$(echo "${bucket_entry}" | jq -r '.CreationDate')
    echo "[${list_file}/${bucket_name}] Processing bucket name=${bucket_name}, creationDate=${bucket_creation_date})"

    tags=$(aws s3api get-bucket-tagging --bucket "${bucket_name}" --output json || true)
    operatorTagExists=$(echo "${tags}" |  jq -r 'select(.TagSet | map({(.Key): .Value}) | add | .evg_task and .environment == "mongodb-enterprise-operator-tests")')
    if [[ -n "${operatorTagExists}" ]]; then
      # Bucket created by the test run in EVG, check if it's older than 2 hours
      hours_since_creation=$(calculate_hours_since_creation "${bucket_creation_date}")

      if [[ ${hours_since_creation} -ge 2 ]]; then
        aws_cmd="aws s3 rb s3://${bucket_name} --force"
        echo "[${list_file}/${bucket_name}] Deleting e2e bucket: ${bucket_name}/${bucket_creation_date}; age in hours: ${hours_since_creation}; (${aws_cmd}), tags: Tags: $(echo "${tags}" | jq -cr .)"
        ${aws_cmd} || true
      else
        echo "[${list_file}/${bucket_name}] Bucket ${bucket_name} is not older than 2 hours, skipping deletion."
      fi
    else
      # Bucket not created by the test run in EVG, check if it's older than 24 hours and owned by us
      hours_since_creation=$(calculate_hours_since_creation "${bucket_creation_date}")
      operatorOwnedTagExists=$(echo "${tags}" |  jq -r 'select(.TagSet | map({(.Key): .Value}) | add | .environment == "mongodb-enterprise-operator-tests")')
      if [[ ${hours_since_creation} -ge 24 && -n "${operatorOwnedTagExists}" ]]; then
        aws_cmd="aws s3 rb s3://${bucket_name} --force"
        echo "[${list_file}/${bucket_name}] Deleting manual bucket: ${bucket_name}/${bucket_creation_date}; age in hours: ${hours_since_creation}; (${aws_cmd}), tags: Tags: $(echo "${tags}" | jq -cr .)"
        ${aws_cmd} || true
      else
        echo "[${list_file}/${bucket_name}] Bucket ${bucket_name} is not older than 24 hours or not owned by us, skipping deletion."
      fi
    fi
  done <<< "$(cat "${list_file}")"
}

remove_old_buckets() {
  echo "##### Removing old s3 buckets"

  if [[ $(uname) == "Darwin" ]]; then
    # Use gdate on macOS
    bucket_date=$(TZ="UTC" gdate +%Y-%m-%dT%H:%M:%S%z -d "24 hour ago")
  else
    # Use date on Linux
    bucket_date=$(TZ="UTC" date +%Y-%m-%dT%H:%M:%S%z -d "24 hour ago")
  fi

  echo "Removing buckets older than ${bucket_date}"

  bucket_list=$(aws s3api list-buckets --query "sort_by(Buckets[?starts_with(Name,'test-')], &CreationDate)" | jq -c --raw-output '.[]')
  if [[ -z ${bucket_list} ]]; then
    echo "Bucket list is empty, nothing to do"
    return 0
  fi

  # here we split bucket_list jsons (each bucket json on its own line) to multiple files using split -l
  tmp_dir=$(mktemp -d)
  pushd "${tmp_dir}"
  bucket_list_file="bucket_list.json"
  echo "${bucket_list}" >${bucket_list_file}

  # Get the number of lines in the file
  num_lines=$(wc -l < "${bucket_list_file}")

  # Check if file is empty
  if [ "${num_lines}" -eq 0 ]; then
      echo "Error: ${bucket_list_file} is empty."
      exit 1
  fi

  # Calculate the number of lines per split file
  if [ "${num_lines}" -lt 30 ]; then
      # If the file has fewer than 30 lines, split it into the number of lines
      lines_per_split="${num_lines}"
  else
      # Otherwise, set the max at 30
      lines_per_split=30
  fi

  echo "Splitting bucket list ($(wc -l <"${bucket_list_file}") lines) into ${lines_per_split} files in dir: ${tmp_dir}. Processing them in parallel."
  # we split to lines_per_split files, so we execute lines_per_split delete processes in parallel
  split -l $(( $(wc -l <"${bucket_list_file}") / lines_per_split)) "${bucket_list_file}" splitted-
  splitted_files_list=$(ls -1 splitted-*)

  # for each file containing slice of buckets, we execute delete_buckets_from_file
  while IFS= read -r list_file; do
    echo "Deleting buckets from file: ${list_file}"
    echo "Bucket list: ${list_file}: $(cat "${list_file}")}"
    delete_buckets_from_file "${list_file}" &
  done <<< "${splitted_files_list}"
  wait

  popd
  rm -rf "${tmp_dir}"
}

# Does general cleanup work for external sources (currently only aws). Must be called once per Evergreen run
prepare_aws() {
    echo "##### Detaching the EBS Volumes that are stuck"
    for v in $(aws ec2 describe-volumes --filters Name=attachment.status,Values=attaching | grep VolumeId | cut -d "\"" -f 4); do
        set -v
        aws ec2 detach-volume --volume-id "${v}" --force
        set +v
    done

    echo "##### Removing ESB volumes which are not used any more"
    # Seems Openshift (sometimes?) doesn't remove the volumes but marks them as available - we need to clean these volumes
    # manually
    for v in $(aws ec2 describe-volumes --filters Name=status,Values=available | grep VolumeId | cut -d "\"" -f 4); do
        set -v
        aws ec2 delete-volume --volume-id "${v}"
        set +v
    done

    remove_old_buckets
}

prepare_aws
