"""String decode fast paths must match the character-by-character path."""

from __future__ import annotations

import pytest

import jaro
from jaro import JaroError


@pytest.mark.parametrize(
    "src, want",
    [
        pytest.param(r'"a\nb"', "a\nb", id="newline"),
        pytest.param(r'"q\"q"', 'q"q', id="escaped-quote"),
        pytest.param(r'"back\\slash"', "back\\slash", id="backslash"),
        pytest.param(r'"\\n"', "\\n", id="escaped-backslash-then-n"),
        pytest.param(r'"\\\""', '\\"', id="three-backslashes"),
        pytest.param(r'"\\\\"', "\\\\", id="four-backslashes"),
        pytest.param(r'"tab\there\r"', "tab\there\r", id="tab-cr"),
        pytest.param(r'"Aé€"', "Aé€", id="unicode-escapes"),
        pytest.param(r'"😀 \n"', "\U0001f600 \n", id="surrogate-pair"),
        pytest.param(
            '"héllo \U0001f600 \\n 日本"',
            "héllo \U0001f600 \n 日本",
            id="raw-non-ascii",
        ),
        pytest.param('"x\\\\xe9y\\n"', "x\\xe9y\n", id="backslash-x-literal"),
        pytest.param('"line\nbreak\\t"', "line\nbreak\t", id="raw-newline"),
    ],
)
def test_escapes_decode(src: str, want: str) -> None:
    assert jaro.loads(src) == want


@pytest.mark.parametrize(
    "src",
    [
        r'"a\q"',
        r'"a\x41"',
        r'"a\U0001f600"',
        r'"a\u12"',
        r'"\ud83d\n"',
        r'"a\\\q"',
        '"a\\\nb"',
        '"abc\\"',
    ],
)
def test_bad_escapes_rejected(src: str) -> None:
    with pytest.raises(JaroError):
        jaro.loads(src)


def test_escaped_strings_in_containers() -> None:
    doc = r'{a:"x\ny" b:["p\"q" "r\\"] "k\t":1}'
    assert jaro.loads(doc) == {"a": "x\ny", "b": ['p"q', "r\\"], "k\t": 1}
    rows = jaro.loads(r'[|a b| "x\ny" 1 "q\"" 2]')
    assert rows == [{"a": "x\ny", "b": 1}, {"a": 'q"', "b": 2}]


def test_round_trip_awkward_strings() -> None:
    vals = [
        "\\",
        '"',
        "\\\\n",
        'a\\"b',
        " \u0000\u001f",
        "\U0001f600\\u0041",
        "\n" * 50 + "\\" * 7,
    ]
    assert jaro.loads(jaro.dumps(vals)) == vals
    assert jaro.loads(jaro.dumps({"k": vals})) == {"k": vals}
