from time import sleep

import boto3
from botocore.exceptions import ClientError
from kubetester.kubetester import get_env_var_or_fail


class AwsS3Client:
    def __init__(self, region: str, **tags):
        # these variables are not used in connection as boto3 client uses the env variables though
        # it makes sense to fail fast if the env variables are not specified
        self.aws_access_key = get_env_var_or_fail("AWS_ACCESS_KEY_ID")
        self.aws_secret_access_key = get_env_var_or_fail("AWS_SECRET_ACCESS_KEY")
        self.s3_client = boto3.client("s3", region_name=region)
        self.tags = tags

    def create_s3_bucket(self, name: str):
        self.s3_client.create_bucket(ACL="private", Bucket=name)
        self.update_bucket_tags(name=name, **self.tags)

    def update_bucket_tags(self, name: str, **tags):
        try:
            existing_tags_response = self.s3_client.get_bucket_tagging(Bucket=name)
        except ClientError as error:
            if error.response["Error"]["Code"] == "NoSuchTagSet":
                print(f"Bucket ({name}) does not have any tags")
                existing_tags_response = {}
            else:
                raise error

        existing_tags = existing_tags_response.get("TagSet", [])
        new_keys = tags.keys()
        new_tags = [{"Key": k, "Value": v} for k, v in tags.items()]
        desired_tags = [tag for tag in existing_tags if tag["Key"] not in new_keys]
        desired_tags.extend(new_tags)
        if len(desired_tags) > 0:
            try:
                self.s3_client.put_bucket_tagging(Bucket=name, Tagging={"TagSet": desired_tags})
            except ClientError as error:
                if error.response["Error"]["Code"] == "InvalidTag":
                    print(f"Tags {desired_tags} failed input validation")
                raise error

    def delete_s3_bucket(self, name: str, attempts: int = 10):
        """Delete an S3 bucket, draining all object versions and delete markers first.

        Versioned and object-lock enabled buckets cannot be deleted while any
        versions remain, so this drains them (bypassing governance retention
        where permitted). Warnings are printed instead of raised so that
        teardown never obscures test results.
        """
        # Drain all object versions and delete markers (also covers unversioned
        # buckets, whose objects appear as a single version).
        to_delete = []
        try:
            paginator = self.s3_client.get_paginator("list_object_versions")
            for page in paginator.paginate(Bucket=name):
                for v in page.get("Versions", []):
                    to_delete.append({"Key": v["Key"], "VersionId": v["VersionId"]})
                for m in page.get("DeleteMarkers", []):
                    to_delete.append({"Key": m["Key"], "VersionId": m["VersionId"]})

            for i in range(0, len(to_delete), 1000):
                self.s3_client.delete_objects(
                    Bucket=name,
                    Delete={"Objects": to_delete[i : i + 1000], "Quiet": True},
                    BypassGovernanceRetention=True,
                )
        except ClientError:
            print(
                f"Warning: could not fully drain versions of bucket ({name}); "
                "object-lock retention may prevent deletion"
            )

        while attempts > 0:
            try:
                self.s3_client.delete_bucket(Bucket=name)
                break
            except ClientError:
                print("Can't delete bucket, will try again in 5 seconds")
            attempts -= 1
            sleep(5)

    def upload_file(self, file_path: str, bucket: str, object_name: str, public_read: bool = False):
        """Upload a file to an S3 bucket.

        Args:
            file_name: File to upload
            bucket: Bucket to upload to
            object_name: S3 object name

        Throws botocore.exceptions.ClientError if upload fails
        """

        extraArgs = {"ACL": "public-read"} if public_read else None
        self.s3_client.upload_file(file_path, bucket, object_name, extraArgs)

    def enable_versioning(self, name: str):
        self.s3_client.put_bucket_versioning(Bucket=name, VersioningConfiguration={"Status": "Enabled"})

    def get_versioning(self, name: str):
        return self.s3_client.get_bucket_versioning(Bucket=name)

    def put_object_lock(self, name: str):
        self.s3_client.put_object_lock_configuration(
            Bucket=name, ObjectLockConfiguration={"ObjectLockEnabled": "Enabled"}
        )


def s3_endpoint(aws_region: str) -> str:
    return f"s3.{aws_region}.amazonaws.com"
