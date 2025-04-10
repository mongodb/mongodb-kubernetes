from types import SimpleNamespace as sn
from unittest.mock import call, mock_open, patch

from ..builders.docker import get_docker_build_cli_args
from ..sonar import find_dockerfile, process_image


@patch("sonar.sonar.docker_push")
@patch("sonar.sonar.docker_tag")
@patch("sonar.sonar.docker_build")
@patch("sonar.sonar.urlretrieve")
@patch("sonar.sonar.create_ecr_repository")
def test_dockerfile_from_url(
    patched_docker_build,
    patched_docker_tag,
    patched_docker_push,
    patched_urlretrive,
    patched_create_ecr_repository,
):
    with open("lib/sonar/test/yaml_scenario6.yaml") as fd:
        with patch("builtins.open", mock_open(read_data=fd.read())) as _mock_file:
            pipeline = process_image(
                image_name="image0",
                skip_tags=[],
                include_tags=["test_dockerfile_from_url"],
                build_args={},
            )

    patched_urlretrive.assert_called_once()
    patched_docker_build.assert_called_once()
    patched_docker_tag.assert_called_once()
    patched_docker_push.assert_called_once()
    patched_create_ecr_repository.assert_called_once()


@patch("sonar.sonar.tempfile.NamedTemporaryFile", return_value=sn(name="random-filename"))
@patch("sonar.sonar.urlretrieve")
def test_find_dockerfile_fetches_file_from_url(patched_urlretrieve, patched_tempfile):
    # If passed a dockerfile which starts with https://
    # make sure urlretrieve and NamedTemporaryFile is called
    dockerfile = find_dockerfile("https://something")

    patched_urlretrieve.assert_called_once()
    patched_tempfile.assert_called_once_with(delete=False)
    assert dockerfile == "random-filename"

    patched_urlretrieve.reset_mock()

    # If dockerfile is a localfile, urlretrieve should not be called.
    dockerfile = find_dockerfile("/localfile/somewhere")
    patched_urlretrieve.assert_not_called()
    assert dockerfile == "/localfile/somewhere"


@patch("sonar.sonar.docker_tag")
@patch("sonar.sonar.docker_build")
def test_labels_are_passed_to_docker_build(_docker_build, _docker_tag):
    _ = process_image(
        image_name="image1",
        skip_tags=[],
        include_tags=[],
        build_args={},
        build_options={},
        inventory="lib/sonar/test/yaml_scenario9.yaml",
    )

    # the labels have been specified in the test scenario and should be passed to docker_build.
    calls = [
        call(
            ".",
            "Dockerfile",
            buildargs={},
            labels={"label-0": "value-0", "label-1": "value-1", "label-2": "value-2"},
            platform=None,
        )
    ]

    _docker_build.assert_has_calls(calls)
    _docker_build.assert_called_once()


@patch("sonar.sonar.docker_tag")
@patch("sonar.sonar.docker_build")
def test_platform_is_passed_to_docker_build(_docker_build, _docker_tag):
    # platform in image is set
    _ = process_image(
        image_name="image1",
        skip_tags=[],
        include_tags=[],
        build_args={},
        build_options={},
        inventory="lib/sonar/test/yaml_scenario11.yaml",
    )

    # platform is not specified
    _ = process_image(
        image_name="image2",
        skip_tags=[],
        include_tags=[],
        build_args={},
        build_options={},
        inventory="lib/sonar/test/yaml_scenario11.yaml",
    )

    calls = [
        call(".", "Dockerfile", buildargs={}, labels={"label-0": "value-0"}, platform="linux/amd64"),
        call(".", "Dockerfile", buildargs={}, labels={"label-1": "value-1"}, platform=None),
    ]

    _docker_build.assert_has_calls(calls)
    _docker_build.assert_called()


def test_get_docker_build_cli_args():
    assert "docker buildx build --load --progress plain . -f dockerfile -t image:latest" == " ".join(
        get_docker_build_cli_args(".", "dockerfile", "image:latest", None, None, None)
    )
    assert (
        "docker buildx build --load --progress plain . -f dockerfile -t image:latest --build-arg a=1 --build-arg long_arg=long_value --label l1=v1 --label l2=v2 --platform linux/amd64"
        == " ".join(
            get_docker_build_cli_args(
                ".",
                "dockerfile",
                "image:latest",
                {"a": "1", "long_arg": "long_value"},
                {"l1": "v1", "l2": "v2"},
                "linux/amd64",
            )
        )
    )
