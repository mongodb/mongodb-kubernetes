# -*- coding: utf-8 -*-

from unittest.mock import mock_open, patch

import pytest

from ..sonar import (
    Context,
    append_output_in_context,
    build_context,
    find_skip_tags,
    should_skip_stage,
)


# yaml_scenario0
@pytest.fixture()
def ys0():
    return open("lib/sonar/test/yaml_scenario0.yaml").read()


@pytest.fixture()
def cs0(ys0):
    with patch("builtins.open", mock_open(read_data=ys0)) as mock_file:
        ctx = build_context(
            image_name="image0",
            skip_tags=[],
            include_tags=[],
            build_args={},
        )
        ctx.stage = ctx.image["stages"][0]

    return ctx


# yaml_scenario1
@pytest.fixture()
def ys1():
    return open("lib/sonar/test/yaml_scenario1.yaml").read()


@pytest.fixture()
def cs1(ys1):
    with patch("builtins.open", mock_open(read_data=ys1)) as mock_file:
        ctx = build_context(
            image_name="image0",
            skip_tags=[],
            include_tags=[],
            build_args={},
        )
        ctx.stage = ctx.image["stages"][0]

    return ctx


# yaml_scenario2
@pytest.fixture()
def ys2():
    return open("lib/sonar/test/yaml_scenario2.yaml").read()


@pytest.fixture()
def cs2(ys2):
    with patch("builtins.open", mock_open(read_data=ys2)) as mock_file:
        ctx = build_context(
            image_name="image0",
            skip_tags=[],
            include_tags=[],
            build_args={
                "image_input0": "🐳",
                "image_input1": "🎄",
                "non_defined_in_inventory": "yes",
            },
        )
        ctx.stage = ctx.image["stages"][0]

    return ctx


def test_skip_tags():
    params = {
        "some": "thing",
        "skip_tags": "ubi,rhel",
    }

    tags = find_skip_tags(params)
    assert len(tags) == 2
    assert tags[0] == "ubi"
    assert tags[1] == "rhel"
    assert "skip_tags" in params

    tags = find_skip_tags()
    assert tags == []

    params = {
        "some": "thing",
        "skip_tags": "ubi",
    }

    tags = find_skip_tags(params)
    assert len(tags) == 1
    assert tags[0] == "ubi"
    assert "skip_tags" in params
    assert "some" in params


def test_should_skip_tags():
    stage = {
        "name": "something",
        "tags": ["tag0", "tag1"],
    }

    assert should_skip_stage(stage, ["tag0"])
    assert should_skip_stage(stage, ["tag1"])
    assert not should_skip_stage(stage, ["another-tag"])

    stage = {
        "name": "something",
    }
    assert not should_skip_stage(stage, ["tag0"])

    stage = {
        "name": "something",
        "tags": ["ubi"],
    }

    assert not should_skip_stage(stage, ["ubuntu"])


def test_build_context(cs0):
    ctx = cs0
    assert ctx.image_name == "image0"
    assert ctx.skip_tags == None
    assert ctx.parameters == {}


def test_build_context(ys0):
    with patch("builtins.open", mock_open(read_data=ys0)) as mock_file:
        with pytest.raises(ValueError, match="Image image1 not found"):
            build_context(image_name="image1", skip_tags=[], include_tags=[])


def test_variable_interpolation0(cs1):
    ctx = cs1

    assert ctx.I("$(inputs.params.registry)/something") == "somereg/something"
    with pytest.raises(KeyError):
        ctx.I("$(inputs.params.input0)")


def test_variable_interpolation1(cs2):
    ctx = cs2

    # Inventory variables
    assert ctx.I("$(inputs.params.inventory_var0)") == "inventory_var_value0"
    assert ctx.I("$(inputs.params.inventory_var1)") == "inventory_var_value1"
    with pytest.raises(ValueError):
        ctx.I("$(inputs.params.inventory_var_non_existing)")

    # Parameters passed to function
    assert ctx.I("$(inputs.params.image_input0)") == "🐳"
    assert ctx.I("$(inputs.params.image_input1)") == "🎄"
    with pytest.raises(ValueError):
        ctx.I("$(inputs.params.image_input_non_existing)")

    # Image variables
    assert ctx.I("$(inputs.params.image_var0)") == "image_var_value0"
    assert ctx.I("$(inputs.params.image_var1)") == "image_var_value1"
    with pytest.raises(ValueError):
        ctx.I("$(inputs.params.image_var_non_existing)")

    # Stage variables
    assert ctx.I("$(inputs.params.stage_var0)") == "stage_value0"
    assert ctx.I("$(inputs.params.stage_var1)") == "stage_value1"
    with pytest.raises(ValueError):
        assert ctx.I("$(inputs.params.stage_var_non_existing)") == "stage_value2"

    # Parameters passed but not defined in inventory
    assert ctx.I("$(inputs.params.non_defined_in_inventory)") == "yes"

    with pytest.raises(ValueError):
        assert ctx.I("$(inputs.params.defined_nowhere)")


def test_variable_interpolation_stage_parameters(ys1):
    with patch("builtins.open", mock_open(read_data=ys1)) as mock_file:
        ctx = build_context(
            image_name="image0",
            skip_tags=[],
            include_tags=[],
            build_args={"input0": "value0", "input1": "value1"},
        )

    ctx.stage = ctx.image["stages"][0]

    assert ctx.I("$(inputs.params.input0)") == "value0"
    assert ctx.I("$(inputs.params.input1)") == "value1"
    assert ctx.I("$(inputs.params.input0)/$(inputs.params.input1)") == "value0/value1"
    assert ctx.I("$(inputs.params.input1)/$(inputs.params.input0)") == "value1/value0"
    assert (
        ctx.I("some text $(inputs.params.input1)/$(inputs.params.input0) more text")
        == "some text value1/value0 more text"
    )

    assert ctx.I("$(inputs.params.input0) 🐳") == "value0 🐳"
    with pytest.raises(ValueError):
        ctx.I("$(inputs.params.non_existing)")


@pytest.mark.xfail
def test_variable_interpolation_stage_parameters_funny(ys1):
    """This test won't work and I'm not sure why:
    1. Maybe parsing the yaml file won't get the same unicode code?
    2. Regex won't capture it"""
    with patch("builtins.open", mock_open(read_data=ys1)) as mock_file:
        ctx = build_context(
            image_name="image0",
            skip_tags=[],
            include_tags=[],
            build_args={"🐳": "whale", "🎄": "tree"},
        )
    ctx.stage = ctx.image["stages"][0]

    assert ctx.I("$(inputs.params.🐳)") == "whale"
    assert ctx.I("$(inputs.params.🎄)") == "tree"


@patch("sonar.sonar.find_image", return_value={})
def test_build_context_skip_tags_from_str(_patched_find_image):
    ctx = build_context(
        inventory="lib/sonar/inventories/simple.yaml",
        image_name="image-name",
        skip_tags="skip0,skip1",
        include_tags="included0, included1",
        build_args={},
    )

    assert ctx.skip_tags == ["skip0", "skip1"]
    assert ctx.include_tags == ["included0", "included1"]


@patch("sonar.sonar.find_image", return_value={})
def test_build_context_skip_tags_from_empty_str(_patched_find_image):
    ctx = build_context(
        inventory="lib/sonar/inventories/simple.yaml",
        image_name="image-name",
        skip_tags="",
        include_tags="",
        build_args={},
    )

    assert ctx.skip_tags == []
    assert ctx.include_tags == []


@patch("sonar.sonar.find_inventory", return_value={"images": {"name": "image-name"}})
@patch("sonar.sonar.find_image", return_value={"name": "image-name"})
def test_build_context_uses_any_inventory(patched_find_image, patched_find_inventory):
    build_context(
        image_name="image-name",
        skip_tags="",
        include_tags="",
        build_args={},
        inventory="other-inventory.yaml",
    )

    patched_find_image.assert_called_once_with("image-name", "other-inventory.yaml")
    patched_find_inventory.assert_called_once_with("other-inventory.yaml")


def test_use_specific_inventory():
    context = build_context(
        image_name="image0",
        skip_tags="",
        include_tags="",
        build_args={"input0": "my-value"},
        inventory="lib/sonar/test/yaml_scenario0.yaml",
    )

    assert context.image["name"] == "image0"
    assert context.stage is None

    assert context.skip_tags == []
    assert context.include_tags == []

    assert context.I("$(inputs.params.input0)") == "my-value"


def test_can_provide_generic_configuration():
    context = build_context(
        image_name="image0",
        skip_tags=[],
        include_tags=[],
        build_args={},
        inventory="lib/sonar/test/yaml_scenario0.yaml",
        build_options={"invalid_options": False, "continue_on_errors": False},
    )

    assert context.continue_on_errors is False
    assert not hasattr(context, "invalid_options")


def test_can_store_in_context():
    ctx = Context(
        inventory={},
        image="some-image",
        parameters=[],
    )

    append_output_in_context(ctx, "stage0", {"key0": "value0", "key1": "value1", "key2": "value2"})

    append_output_in_context(ctx, "stage0", {"key0": "value0", "key1": "value1", "key2": "value2", "key3": "value3"})

    assert len(ctx.stage_outputs["stage0"]) == 2
    assert len(ctx.stage_outputs["stage0"][0]) == 3
    assert len(ctx.stage_outputs["stage0"][1]) == 4

    assert ctx.stage_outputs["stage0"][0] == {"key0": "value0", "key1": "value1", "key2": "value2"}

    assert ctx.stage_outputs["stage0"][1] == {"key0": "value0", "key1": "value1", "key2": "value2", "key3": "value3"}

    append_output_in_context(ctx, "stage1", {"key0": "value0", "key1": "value1", "key2": "value2", "key3": "value3"})

    assert len(ctx.stage_outputs) == 2
    assert len(ctx.stage_outputs["stage1"][0]) == 4

    assert ctx.stage_outputs["stage1"][0] == {"key0": "value0", "key1": "value1", "key2": "value2", "key3": "value3"}

    assert ctx.I("$(stages['stage0'].outputs[0].key0)") == "value0"
    assert ctx.I("$(stages['stage0'].outputs[1].key3)") == "value3"


def test_stages_output_and_variables(cs2: Context):
    ctx = cs2
    append_output_in_context(ctx, "stage0", {"key0": "value0", "key1": "value1", "key2": "value2"})

    append_output_in_context(ctx, "stage0", {"key0": "value0", "key1": "value1", "key2": "value2", "key3": "value3"})

    assert (
        ctx.I("$(inputs.params.inventory_var0) -- $(stages['stage0'].outputs[0].key2)")
        == "inventory_var_value0 -- value2"
    )

    assert ctx.I("$(inputs.params.image_input0) -- $(stages['stage0'].outputs[1].key3)") == "🐳 -- value3"
