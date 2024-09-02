from unittest.mock import Mock, patch

from ..sonar import process_image


@patch("sonar.sonar.render", return_value="")
def test_key_error_is_not_raised_on_empty_inputs(patched_render: Mock):
    process_image(
        image_name="image1",
        skip_tags=[],
        include_tags=[],
        build_args={},
        build_options={},
        inventory="lib/sonar/test/yaml_scenario10.yaml",
    )
    patched_render.assert_called()
