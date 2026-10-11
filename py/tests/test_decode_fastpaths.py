"""Decoder fast paths must match the general path in value, type and error behaviour."""

from __future__ import annotations

from typing import Any

import pytest

import jaro
from jaro import JaroError

ROWS = 200


def strict(v: Any) -> str:
    """Repr that tells 1 from 1.0 and True from 1."""
    return repr(v)


def slow_variant(text: str) -> str:
    """Same document with doubled spaces, which the single-space fast paths do not match."""
    return text.replace(" ", "  ")


class TestTextFastPaths:
    @pytest.mark.parametrize(
        "text",
        [
            "[[1.5 2.5] [3.5 4.5]]",
            "[[1.5 2] [3 4.5]]",
            "[[1.0 2.5] [3.5 4.5]]",
            "[[1.5 2.5] [3.5]]",
            "[[1.5 12345678901234567] [2.5 3.5]]",
            "[[-0.5 -0] [0.25 7]]",
            "[[1.5 2.5.5] [3.5 4.5]]",
            "[[1.5 2.5] [3.5 4.5]",
            "[[1.5 2.5] 3]",
            "[[1.5] [2.5] [3.5]]",
        ],
    )
    def test_float_rows_match_general_path(self, text: str) -> None:
        try:
            want = strict(jaro.loads(slow_variant(text)))
        except JaroError:
            with pytest.raises(JaroError):
                jaro.loads(text)
            return
        assert strict(jaro.loads(text)) == want

    def test_float_rows_types(self) -> None:
        got = jaro.loads("[[1.0 2.5] [3 4.5]]")
        assert strict(got) == "[[1, 2.5], [3, 4.5]]"

    def test_flat_float_array_types(self) -> None:
        assert strict(jaro.loads("[1.5 2.5 3.5]")) == "[1.5, 2.5, 3.5]"
        assert strict(jaro.loads("[1.5 2.0 3.5]")) == "[1.5, 2, 3.5]"

    def test_overflowing_decimal_stays_a_string(self) -> None:
        tok = "1" + "0" * 400 + ".5"
        assert jaro.loads(f"[{tok} 1.5]") == [tok, 1.5]

    def test_object_values_and_keys(self) -> None:
        got = jaro.loads('{a:1 b:2.5 c:3.0 d:"x y" e:[] f:{} g:~ h:true "i j":false}')
        assert strict(got) == strict(
            {
                "a": 1,
                "b": 2.5,
                "c": 3,
                "d": "x y",
                "e": [],
                "f": {},
                "g": None,
                "h": True,
                "i j": False,
            }
        )

    def test_duplicate_key_offset(self) -> None:
        with pytest.raises(JaroError) as e:
            jaro.loads("{a:1 a:2}")
        assert e.value.message == "duplicate key 'a'"
        assert e.value.offset == 5

    def test_bool_word_is_not_a_key(self) -> None:
        with pytest.raises(JaroError):
            jaro.loads("{true:1}")

    def test_table_cells(self) -> None:
        got = jaro.loads('[|a b c| 1 ^ "p q" 2.5 x [] ^ {} 3.0]')
        want = [{"a": 1, "c": "p q"}, {"a": 2.5, "b": "x", "c": []}, {"b": {}, "c": 3}]
        assert strict(got) == strict(want)

    def test_escaped_cell_falls_back(self) -> None:
        assert jaro.loads(r'[|a b| "x\ny" 1 "q" 2]') == [
            {"a": "x\ny", "b": 1},
            {"a": "q", "b": 2},
        ]


class TestBinaryFastPaths:
    def rows(self) -> list[list[float]]:
        return [[i + 0.5, i + 0.25] for i in range(ROWS)]

    def test_float_rows_round_trip(self) -> None:
        v = self.rows()
        assert strict(jaro.loads_binary(jaro.dumps_binary(v))) == strict(v)

    def test_integral_row_inside_a_chunk(self) -> None:
        v = self.rows()
        v[70] = [1.0, 2.5]
        want = strict(jaro.loads(jaro.dumps(v)))
        assert strict(jaro.loads_binary(jaro.dumps_binary(v))) == want
        assert "[1, 2.5]" in want

    def test_ragged_rows(self) -> None:
        v = self.rows()
        v[10] = [1.5, 2.5, 3.5]
        assert strict(jaro.loads_binary(jaro.dumps_binary(v))) == strict(v)

    def test_mixed_scalars_in_array(self) -> None:
        v = [1.5, 2, "s", None, True, 3.25, [], {}, 200, -1]
        assert strict(jaro.loads_binary(jaro.dumps_binary(v))) == strict(v)

    def test_truncated_float_rows_raise_jaro_error(self) -> None:
        blob = jaro.dumps_binary(self.rows())
        for cut in (3, 5, 20, len(blob) // 2, len(blob) - 1):
            with pytest.raises(JaroError):
                jaro.loads_binary(blob[:cut])


class TestStrictBinary:
    def test_sparse_padding_bits_must_be_zero(self) -> None:
        rows: list[dict[str, Any]] = [{"a": i, "b": i} for i in range(10)]
        del rows[3]["b"]
        blob = bytearray(jaro.dumps_binary(rows))
        assert strict(jaro.loads_binary(bytes(blob))) == strict(rows)
        bitmap = bytes([0b11110111, 0b11])
        at = bytes(blob).index(b"\x01" + bitmap) + 1 + 1
        blob[at] |= 0b100
        with pytest.raises(JaroError):
            jaro.loads_binary(bytes(blob))
