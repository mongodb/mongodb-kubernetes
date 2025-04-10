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
        v = self.s3_client.list_objects_v2(Bucket=name)
        print(v)
        if v is not None and "Contents" in v:
            for x in v["Contents"]:
                self.s3_client.delete_object(Bucket=name, Key=x["Key"])

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


def s3_endpoint(aws_region: str) -> str:
    return f"s3.{aws_region}.amazonaws.com"
