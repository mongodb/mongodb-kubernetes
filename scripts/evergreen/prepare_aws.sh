#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

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

    echo "##### Removing old s3 buckets"
    # note, to run this on mac you need to install coreutils ('brew install coreutils') and use 'gdate' instead
    if [[ $(uname) == "Darwin" ]]; then
        # Use gdate on macOS
        yesterday=$(gdate +%Y-%m-%dT:%H -d "4 hour ago") # "2021-04-09T04:23:33+00:00"
    else
        # Use date on Linux
        yesterday=$(date +%Y-%m-%dT:%H -d "4 hour ago")
    fi
    for bucket in $(aws s3api list-buckets --query "Buckets[?CreationDate<='${yesterday}'&&starts_with(Name,'test-')]" | jq --raw-output '.[].Name'); do
        # Get the tags for the bucket and check whether the operator generated tags are present.
        tags=$(aws s3api get-bucket-tagging --bucket "${bucket}" --output json || true)
        operatorTagExists=$(echo "$tags" | jq -r '.TagSet[] | select(.Key=="environment" and .Value=="mongodb-enterprise-operator-tests")')
        if [[ -n "$operatorTagExists" ]]
        then
            aws s3 rb s3://"${bucket}" --force || true
        else
            echo "#### Not removing bucket ${bucket} because it was not created by the operator test suite"
        fi
    done
}
prepare_aws
