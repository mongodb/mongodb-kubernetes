import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "dev"))

from validate_jsonc import strip_jsonc  # noqa: E402


def _load(raw: str):
    return json.loads(strip_jsonc(raw))


def test_line_and_block_comments_and_trailing_comma():
    assert _load('{"a": 1, // line\n "b": 2, /* block */ "c": [1, 2,],}') == {
        "a": 1,
        "b": 2,
        "c": [1, 2],
    }


def test_comment_markers_inside_strings_are_preserved():
    assert _load('{"url": "https://example.com/*x*/", "p": "a // b"}') == {
        "url": "https://example.com/*x*/",
        "p": "a // b",
    }


def test_escaped_quote_in_string():
    assert _load(r'{"q": "he said \"hi\" /* not a comment */"}') == {"q": 'he said "hi" /* not a comment */'}


def test_unterminated_block_comment_raises():
    with pytest.raises(ValueError, match="unterminated"):
        strip_jsonc('{"a": 1} /* oops')
