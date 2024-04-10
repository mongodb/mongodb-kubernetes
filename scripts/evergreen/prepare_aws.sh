#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

delete_buckets_from_file() {
  list_file=$1

  echo "Deleting buckets from file: ${list_file}"

  while IFS= read -r bucket_entry; do
    bucket_name=$(echo "${bucket_entry}" | jq -r '.Name')
    bucket_creation_date=$(echo "${bucket_entry}" | jq -r '.CreationDate')
    echo "[${list_file}/${bucket_name}] Processing bucket name=${bucket_name}, creationDate=${bucket_creation_date})"
      # Get the tags for the bucket and check whether the operator generated tags are present.
    tags=$(aws s3api get-bucket-tagging --bucket "${bucket_name}" --output json || true)
    operatorTagExists=$(echo "$tags" |  jq -r 'select(.TagSet | map({(.Key): .Value}) | add | .evg_task and .environment == "mongodb-enterprise-operator-tests")')
    if [[ -n "$operatorTagExists" ]]
    then
         aws_cmd="aws s3 rb s3://${bucket_name} --force"
         echo "[${list_file}/${bucket_name}] Deleting bucket: ${bucket_name}/${bucket_creation_date} (${aws_cmd}), tags: Tags: $(echo "${tags}" | jq -cr .)"
         ${aws_cmd} || true
    else
        echo "[${list_file}/${bucket_name}] #### Not removing bucket ${bucket_name} because it was not created by the test run in EVG. Tags: $(echo "${tags}" | jq -cr .)"
    fi
  done <<< "$(cat "${list_file}")"
}

remove_old_buckets() {
  echo "##### Removing old s3 buckets"
  # note, to run this on mac you need to install coreutils ('brew install coreutils') and use 'gdate' instead
  if [[ $(uname) == "Darwin" ]]; then
      # Use gdate on macOS
      bucket_date=$(TZ="UTC" gdate +%Y-%m-%dT%H:%M:%S%z -d "2 hour ago") # "2021-04-09T04:23:33+00:00"
  else
      # Use date on Linux
      bucket_date=$(TZ="UTC" date +%Y-%m-%dT%H:%M:%S%z -d "2 hour ago")
  fi
  echo "removing buckets older than ${bucket_date}"

  bucket_list=$(aws s3api list-buckets --query "sort_by(Buckets[?CreationDate<='${bucket_date}'&&starts_with(Name,'test-')], &CreationDate)" | jq -c --raw-output '.[]')
  if [[ -z ${bucket_list} ]]; then
    echo "Bucket list is empty, nothing to do"
    return 0
  fi

  # here we split bucket_list jsons (each bucket json on its own line) to multiple files using split -l
  tmp_dir=$(mktemp -d)
  pushd "${tmp_dir}"
  bucket_list_file="bucket_list.json"
  echo "${bucket_list}" >${bucket_list_file}
  echo "Splitting bucket list ($(wc -l <"${bucket_list_file}") lines) into files in dir: ${tmp_dir}"
  # we split to 30 files, so we execute 30 delete processes in parallel
  split -l $(( $(wc -l <"${bucket_list_file}") / 30)) "${bucket_list_file}" splitted-
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
