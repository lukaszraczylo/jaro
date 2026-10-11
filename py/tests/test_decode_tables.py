"""Table header fast path must match the general header parser."""

from __future__ import annotations

import pytest

import jaro
from jaro import JaroError


@pytest.mark.parametrize(
    "src, want",
    [
        pytest.param(
            "[|a b| 1 2 3 4]", [{"a": 1, "b": 2}, {"a": 3, "b": 4}], id="plain"
        ),
        pytest.param("[|a| 1 2]", [{"a": 1}, {"a": 2}], id="single-column"),
        pytest.param(
            "[|_x y_2| p q]", [{"_x": "p", "y_2": "q"}], id="underscore-digit"
        ),
        pytest.param('[|"a b" c| 1 2]', [{"a b": 1, "c": 2}], id="quoted-key"),
        pytest.param("[|a  b| 1 2]", [{"a": 1, "b": 2}], id="double-space"),
        pytest.param("[| a b | 1 2]", [{"a": 1, "b": 2}], id="padded"),
        pytest.param("[|a b\n c| 1 2 3]", [{"a": 1, "b": 2, "c": 3}], id="newline"),
        pytest.param(
            "[|a{b c} d| 1 2 3]", [{"a": {"b": 1, "c": 2}, "d": 3}], id="sub-columns"
        ),
        pytest.param("[|é b| 1 2]", [{"é": 1, "b": 2}], id="non-ascii-key"),
        pytest.param("[|a b c| 1 2]", [{"a b": 1, "c": 2}], id="nbsp-inside-key"),
        pytest.param(
            "{|a b| x 1 2 y 3 4}",
            {"x": {"a": 1, "b": 2}, "y": {"a": 3, "b": 4}},
            id="keyed",
        ),
    ],
)
def test_headers(src: str, want: object) -> None:
    assert jaro.loads(src) == want


@pytest.mark.parametrize(
    "src",
    [
        "[|a a| 1 2]",
        "[|true a| 1 2]",
        "[|a false| 1 2]",
        "[|1 a| 1 2]",
        "[|a b 1 2]",
        "[||]",
        "[|a b| 1]",
    ],
)
def test_bad_headers(src: str) -> None:
    with pytest.raises(JaroError):
        jaro.loads(src)


class TestObjectPairs:
    def test_pair_values(self) -> None:
        got = jaro.loads(
            '{a:1 b:2.5 c:3.0 d:"x y" e:[] f:{} g:~ h:true i:word "j k":"" "":x}'
        )
        assert repr(sorted(got.items())) == repr(
            sorted(
                {
                    "a": 1,
                    "b": 2.5,
                    "c": 3,
                    "d": "x y",
                    "e": [],
                    "f": {},
                    "g": None,
                    "h": True,
                    "j k": "",
                    "": "x",
                    "i": "word",
                }.items()
            )
        )

    def test_nested_and_container_values(self) -> None:
        assert jaro.loads("{a:{b:[1 2] c:{d:1}} e:[|x y| 1 2]}") == {
            "a": {"b": [1, 2], "c": {"d": 1}},
            "e": [{"x": 1, "y": 2}],
        }

    @pytest.mark.parametrize(
        "src",
        [
            "{a:1 a:2}",
            '{a:1 "a":2}',
            "{a:1 b:2 a:~}",
            "{true:1}",
            "{false:1}",
            "{a:}",
            "{a 1}",
        ],
    )
    def test_bad_objects(self, src: str) -> None:
        with pytest.raises(JaroError):
            jaro.loads(src)

    def test_quoted_bool_words_are_keys(self) -> None:
        assert jaro.loads('{"true":1 "false":2}') == {"true": 1, "false": 2}

    def test_spread_interplay(self) -> None:
        assert jaro.loads("base:&b {a:1 b:2} x:{a:9 *b}") == {
            "base": {"a": 1, "b": 2},
            "x": {"a": 1, "b": 2},
        }
        assert jaro.loads("base:&b {a:1 b:2} x:{*b a:9}") == {
            "base": {"a": 1, "b": 2},
            "x": {"a": 9, "b": 2},
        }


@pytest.mark.parametrize(
    "src, want",
    [
        pytest.param("[1 2 3]", [1, 2, 3], id="single-spaces"),
        pytest.param("[1  2\t3\n4]", [1, 2, 3, 4], id="mixed-whitespace"),
        pytest.param("[1 #c\n2]", [1, 2], id="comment-after-space"),
        pytest.param("[1\n#c1\n#c2\n2 ]", [1, 2], id="comment-lines"),
        pytest.param("[ 1 2 ]", [1, 2], id="padded"),
        pytest.param(
            "[|a b| 1 2 #x\n 3 4 ]",
            [{"a": 1, "b": 2}, {"a": 3, "b": 4}],
            id="table-comments",
        ),
        pytest.param(
            "[|a b| 1\t2\r\n3 #z\n4]",
            [{"a": 1, "b": 2}, {"a": 3, "b": 4}],
            id="table-tabs",
        ),
        pytest.param("{a:1 #c\n b:2 }", {"a": 1, "b": 2}, id="object-comment"),
        pytest.param("a:1\n#c\nb:2", {"a": 1, "b": 2}, id="bare-object"),
        pytest.param("  # lead\n [1]", [1], id="leading-comment"),
        pytest.param("[1 2] ", [1, 2], id="trailing-space"),
    ],
)
def test_whitespace_and_comments(src: str, want: object) -> None:
    assert jaro.loads(src) == want


@pytest.mark.parametrize(
    "src", ["[1 ", "[|a| 1 ", "{a:1 ", "[|a b| 1 2 3", "[ #only comment"]
)
def test_unterminated_after_space(src: str) -> None:
    with pytest.raises(JaroError):
        jaro.loads(src)
