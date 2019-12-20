import boto3
from kubetester.kubetester import get_env_var_or_fail


class AwsS3Client:
    def __init__(self, region: str):
        # these variables are not used in connection as boto3 client uses the env variables though
        # it makes sense to fail fast if the env variables are not specified
        self.aws_access_key = get_env_var_or_fail("AWS_ACCESS_KEY_ID")
        self.aws_secret_access_key = get_env_var_or_fail("AWS_SECRET_ACCESS_KEY")

        self.s3_client = boto3.client("s3", region_name=region)

    def create_s3_bucket(self, name: str):
        self.s3_client.create_bucket(ACL="private", Bucket=name)

    def delete_s3_bucket(self, name: str):
        v = self.s3_client.list_objects_v2(Bucket=name)
        if v is not None and "Contents" in v:
            for x in v["Contents"]:
                self.s3_client.delete_object(Bucket=name, Key=x["Key"])

        self.s3_client.delete_bucket(Bucket=name)


def s3_endpoint(aws_region: str) -> str:
    return f"s3.{aws_region}.amazonaws.com"
