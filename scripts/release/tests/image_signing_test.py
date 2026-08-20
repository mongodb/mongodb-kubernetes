import json
from subprocess import CalledProcessError
from unittest.mock import MagicMock, patch

import pytest

from scripts.release.build.image_signing import get_platform_manifest_digests, verify_signature

MULTI_ARCH_MANIFEST = {
    "mediaType": "application/vnd.oci.image.index.v1+json",
    "manifests": [
        {"digest": "sha256:amd64digest", "platform": {"architecture": "amd64", "os": "linux"}},
        {"digest": "sha256:arm64digest", "platform": {"architecture": "arm64", "os": "linux"}},
        {"digest": "sha256:attestationdigest", "platform": {"architecture": "unknown", "os": "unknown"}},
    ],
}

SINGLE_ARCH_MANIFEST = {
    "mediaType": "application/vnd.oci.image.manifest.v1+json",
    "config": {"digest": "sha256:configdigest"},
}


@pytest.mark.parametrize(
    "name, manifest, want_digests",
    [
        (
            "multi-arch index returns platform digests, skipping attestation manifests",
            MULTI_ARCH_MANIFEST,
            ["sha256:amd64digest", "sha256:arm64digest"],
        ),
        (
            "single-arch image has nothing to recurse into",
            SINGLE_ARCH_MANIFEST,
            [],
        ),
    ],
)
@patch("scripts.release.build.image_signing.run_command_with_retries")
def test_get_platform_manifest_digests(mock_run, name, manifest, want_digests):
    mock_run.return_value = MagicMock(stdout=json.dumps(manifest))

    digests = get_platform_manifest_digests("myrepo/myimage:1.0.0")

    assert digests == want_digests


OK = MagicMock()
NO_SIGNATURE_ERR = CalledProcessError(1, ["cosign", "verify"], stderr="Error: no signatures found")
INVALID_SIGNATURE_ERR = CalledProcessError(
    1, ["cosign", "verify"], stderr="Error: no matching signatures: invalid signature when validating ASN.1"
)
# _sig_tag_exists calls run_command_with_retries; any CalledProcessError means the tag wasn't found.
SIG_TAG_MISSING = CalledProcessError(1, ["skopeo", "inspect"], stderr="manifest unknown")


@pytest.mark.parametrize(
    "name, platform_digests, run_side_effect, want_err, want_call_count, want_digests_lookup",
    [
        (
            "top-level and all platform digests signed: passes",
            ["sha256:amd64digest", "sha256:arm64digest"],
            [OK, OK, OK, OK, OK],
            "",
            5,
            True,
        ),
        (
            "platform digest missing signature: warns only, does not raise",
            ["sha256:amd64digest"],
            [OK, SIG_TAG_MISSING],
            "",
            2,
            True,
        ),
        (
            "platform digest has invalid signature: raises",
            ["sha256:amd64digest"],
            [OK, OK, INVALID_SIGNATURE_ERR],
            "Failed to verify signature for platform image",
            3,
            True,
        ),
        (
            "top-level signature missing: raises without checking platform digests",
            [],
            NO_SIGNATURE_ERR,
            "Failed to verify signature for image",
            1,
            False,
        ),
    ],
)
@patch("scripts.release.build.image_signing.get_platform_manifest_digests")
@patch("scripts.release.build.image_signing.run_command_with_retries")
@patch("scripts.release.build.image_signing.requests")
def test_verify_signature(
    mock_requests,
    mock_run,
    mock_digests,
    name,
    platform_digests,
    run_side_effect,
    want_err,
    want_call_count,
    want_digests_lookup,
):
    mock_requests.get.return_value = MagicMock(status_code=200, text="fake-public-key")
    mock_digests.return_value = platform_digests
    mock_run.side_effect = run_side_effect

    if want_err:
        with pytest.raises(Exception, match=want_err):
            verify_signature("myrepo/myimage", "1.0.0")
    else:
        verify_signature("myrepo/myimage", "1.0.0")

    assert mock_run.call_count == want_call_count
    assert mock_digests.called == want_digests_lookup
