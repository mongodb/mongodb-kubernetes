from unittest import skip

from ..sonar import process_image

#@skip("This test case is only used to generate the final Dockerfile for ops-manager")
def test_build_om_dockerfile():
    process_image(
        image_name="ops-manager",
        skip_tags=["release"],
        include_tags=["final_dockerfile"],
        build_args={
            "registry": "localhost:5000",
            "version": "8.0.7",
            "om_download_url": "https://downloads.mongodb.com/on-prem-mms/tar/mongodb-mms-8.0.7.500.20250505T1426Z.tar.gz",
        },
        build_options={},
        inventory="inventories/om.yaml",
    )
