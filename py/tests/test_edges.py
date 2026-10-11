from __future__ import annotations

import math
from collections import OrderedDict
from typing import Any

import pytest
from conftest import same

import jaro
from jaro import _core, text


def nest_list(n: int) -> Any:
    x: Any = None
    for _ in range(n):
        x = [x]
    return x


def nest_dict(n: int) -> Any:
    x: Any = 1
    for _ in range(n):
        x = {"a": x}
    return x


class TestDataModel:
    def test_bool_is_not_int(self) -> None:
        for v in (True, False, [True, 1, False, 0], {"a": True, "b": 1}):
            assert same(jaro.loads(jaro.dumps(v)), v)
            assert same(jaro.loads_binary(jaro.dumps_binary(v)), v)
        got = jaro.loads("[true 1 false 0]")
        assert [type(x) for x in got] == [bool, int, bool, int]

    def test_bool_columns_and_tables(self) -> None:
        rows = [{"on": i % 2 == 0, "n": i} for i in range(9)]
        assert same(jaro.loads_binary(jaro.dumps_binary(rows)), rows)
        flags = [True, False, True, True, False, False, True, False, True]
        assert jaro.loads_binary(jaro.dumps_binary(flags)) == flags

    @pytest.mark.parametrize("v", [2**63, -(2**63) - 1, 2**200])
    def test_int_outside_int64_rejected(self, v: int) -> None:
        for enc in (jaro.dumps, jaro.dumps_binary):
            with pytest.raises(jaro.JaroError):
                enc(v)
            with pytest.raises(jaro.JaroError):
                enc([1, {"a": v}])

    def test_int64_bounds_round_trip(self) -> None:
        v = [2**63 - 1, -(2**63), 0, -1]
        assert jaro.loads(jaro.dumps(v)) == v
        assert jaro.loads_binary(jaro.dumps_binary(v)) == v

    @pytest.mark.parametrize("v", [math.nan, math.inf, -math.inf])
    def test_non_finite_rejected(self, v: float) -> None:
        for enc in (jaro.dumps, jaro.dumps_binary):
            with pytest.raises(jaro.JaroError):
                enc([1.5, v])

    def test_integral_floats_become_ints(self) -> None:
        for v in (3.0, -7.0, 0.0, -0.0, 2.0**52):
            for out in (
                jaro.loads(jaro.dumps(v)),
                jaro.loads_binary(jaro.dumps_binary(v)),
            ):
                assert type(out) is int and out == int(v)
        big = 2.0**53
        assert type(jaro.loads(jaro.dumps(big))) is float
        assert type(jaro.loads_binary(jaro.dumps_binary(big))) is float

    def test_zero_is_integer(self) -> None:
        assert jaro.dumps(-0.0) == "0"
        assert jaro.dumps(0.0) == "0"
        for src in ("-0", "-0.0", "0e5", ".0", "1e-999"):
            out = jaro.loads(src)
            assert type(out) is int and out == 0, src

    def test_non_str_keys_rejected(self) -> None:
        for enc in (jaro.dumps, jaro.dumps_binary):
            with pytest.raises(jaro.JaroError):
                enc({1: "a"})
            with pytest.raises(jaro.JaroError):
                enc({"a": 1, 2: 3})

    def test_unsupported_types_rejected(self) -> None:
        for v in ((1, 2), {1, 2}, b"x", object()):
            for enc in (jaro.dumps, jaro.dumps_binary):
                with pytest.raises(jaro.JaroError):
                    enc(v)

    def test_lone_surrogate_rejected(self) -> None:
        for enc in (jaro.dumps, jaro.dumps_binary):
            with pytest.raises(jaro.JaroError):
                enc("a\ud800b")
            with pytest.raises(jaro.JaroError):
                enc({"a\udc00": 1})

    def test_dict_subclass_and_keys_sorted(self) -> None:
        v = OrderedDict([("b", 1), ("a", 2)])
        assert jaro.dumps(v) == "a:2 b:1"
        assert jaro.loads_binary(jaro.dumps_binary(v)) == {"a": 2, "b": 1}

    def test_unicode_round_trip(self) -> None:
        v = {"é": "héllo ✓ 😀", "": "", "a b": "line\nbreak\ttab\x01ctl \x7f del"}
        assert jaro.loads(jaro.dumps(v)) == v
        assert jaro.loads_binary(jaro.dumps_binary(v)) == v


class TestFloatFormat:
    @pytest.mark.parametrize(
        ("value", "want"),
        [
            (1234567.5, "1.2345675e+06"),
            (100000.5, "100000.5"),
            (1e21, "1e+21"),
            (1.5e-7, "1.5e-07"),
            (0.0001, "0.0001"),
            (0.00001, "1e-05"),
            (123456789.5, "1.234567895e+08"),
            (2.0**53, "9.007199254740992e+15"),
            (5e-324, "5e-324"),
            (1.7976931348623157e308, "1.7976931348623157e+308"),
            (-2.5, "-2.5"),
            (0.1, "0.1"),
        ],
    )
    def test_matches_go_g_shortest(self, value: float, want: str) -> None:
        assert jaro.dumps(value) == want
        assert jaro.loads(want) == value


class TestTextSyntax:
    def test_duplicate_keys_rejected(self) -> None:
        for src in (
            "a:1 a:2",
            "{a:1 a:2}",
            '{"a":1 a:2}',
            "[|a a| 1 2]",
            "{|a a| x 1 2}",
            "[|u{a a} b| 1 2 3]",
            "{|a b| x 1 2 x 3 4}",
        ):
            with pytest.raises(jaro.JaroError):
                jaro.loads(src)

    def test_spread_overrides_but_explicit_duplicates_do_not(self) -> None:
        assert jaro.loads("b:&b {x:1 y:2} c:{x:9 *b}") == {
            "b": {"x": 1, "y": 2},
            "c": {"x": 1, "y": 2},
        }
        assert jaro.loads("b:&b {x:1} c:{*b x:9}")["c"] == {"x": 9}
        with pytest.raises(jaro.JaroError):
            jaro.loads("b:&b {x:1} c:{*b x:9 x:8}")

    def test_references_are_independent_copies(self) -> None:
        got = jaro.loads("a:&x [1 2] b:*x")
        got["b"].append(3)
        assert got["a"] == [1, 2]

    def test_anchor_rules(self) -> None:
        for src in (
            "a:*x",
            "a:&x 1 b:&x 2",
            "a:&x [*x]",
            "a:&",
            "a:{*x}",
            "b:&x 1 a:{*x}",
        ):
            with pytest.raises(jaro.JaroError):
                jaro.loads(src)

    def test_numbers(self) -> None:
        got = jaro.loads(
            "[1 -2 3.5 .5 -.5 1e3 5. 007 9223372036854775807 -9223372036854775808 9223372036854775808 1..5 - -x 1e999]"
        )
        assert got == [
            1,
            -2,
            3.5,
            0.5,
            -0.5,
            1000,
            5,
            7,
            2**63 - 1,
            -(2**63),
            9.223372036854776e18,
            "1..5",
            "-",
            "-x",
            "1e999",
        ]
        assert type(got[10]) is float
        assert jaro.loads("[1_000 0x1p3 0x10 0b1 +5 1e Inf NaN]") == [
            "1_000",
            "0x1p3",
            "0x10",
            "0b1",
            "+5",
            "1e",
            "Inf",
            "NaN",
        ]

    def test_quoted_escapes_and_surrogates(self) -> None:
        assert jaro.loads(r'"\u0041\u00e9\ud83d\ude00"') == "Aé😀"
        for src in (
            r'"\ud83d"',
            r'"\ude00\ud83d"',
            r'"\ud83d\u0041"',
            r'"\ud83dx"',
            r'"\q"',
            r'"\u+041"',
            r'"\u12"',
            r'"\u12g4"',
            '"abc',
            '"a\\',
        ):
            with pytest.raises(jaro.JaroError):
                jaro.loads(src)

    def test_quoting_rules(self) -> None:
        tricky = [
            "",
            "~",
            "^",
            "true",
            "false",
            "123",
            "-1",
            ".5",
            "-.5",
            "&x",
            "*x",
            "a b",
            "a:b",
            "a#b",
            'a"b',
            "a\\b",
            "a|b",
            "{",
            "]",
        ]
        assert jaro.loads(jaro.dumps(tricky)) == tricky
        for s in ("-", "-a", "a-1", "1a", "é", "x.y"):
            assert jaro.loads(jaro.dumps([s])) == [s]
        assert jaro.dumps(["-", "a-1", "x"]) == "[- a-1 x]"
        assert jaro.dumps(["1a"]) == '["1a"]'

    def test_comments_and_whitespace(self) -> None:
        assert jaro.loads("# top\n a:1 # one\n\t\r\n b:[1 #x\n 2]") == {
            "a": 1,
            "b": [1, 2],
        }
        assert jaro.loads('a:"#not"') == {"a": "#not"}

    def test_table_shapes(self) -> None:
        sparse = [{"id": 1, "a": 1}, {"id": 2}, {"id": 3, "a": 3}]
        assert jaro.dumps(sparse) == "[|a id| 1 1 ^ 2 3 3]"
        assert jaro.loads(jaro.dumps(sparse)) == sparse
        assert jaro.dumps([{"a": 1}, {"b": 2}, {"c": 3}]) == "[{a:1} {b:2} {c:3}]"
        assert jaro.dumps([{"a": 1}, {"b": 2}]) == "[|a b| 1 ^ ^ 2]"
        keyed = {"x": {"a": 1, "b": 2}, "y": {"a": 3}}
        assert jaro.dumps({"k": keyed}) == "k:{|a b| x 1 2 y 3 ^}"
        nested = [
            {"id": 1, "u": {"n": "a", "m": 1}},
            {"id": 2, "u": {"n": "b", "m": 2}},
        ]
        assert jaro.dumps(nested) == "[|id u{m n}| 1 1 a 2 2 b]"
        assert jaro.loads(jaro.dumps(nested)) == nested

    def test_table_errors(self) -> None:
        for src in (
            "[|a b| 1 2 3]",
            "[|| 1]",
            "[|a| 1] x",
            "[|a b| 1",
            "[|a{b}",
            "{|a b| x 1}",
            "[|1 b| 1 2]",
            "[|a|{|}]",
        ):
            with pytest.raises(jaro.JaroError):
                jaro.loads(src)

    def test_header_nesting_limit(self) -> None:
        ok = "[|" + "a{" * 8 + "b" + "}" * 8 + "| 1]"
        assert jaro.loads(ok) == [
            {"a": {"a": {"a": {"a": {"a": {"a": {"a": {"a": {"b": 1}}}}}}}}}
        ]
        with pytest.raises(jaro.JaroError):
            jaro.loads("[|" + "a{" * 10 + "b" + "}" * 10 + "| 1]")

    def test_invalid_input(self) -> None:
        for bad in (b"\xff", b"a:\xc3", b"\xed\xa0\x80", "a:\ud800"):
            with pytest.raises(jaro.JaroError):
                jaro.loads(bad)

    def test_error_offset_is_byte_offset(self) -> None:
        with pytest.raises(jaro.JaroError) as ei:
            jaro.loads('é:1 "x" ')
        assert ei.value.offset is not None and ei.value.offset >= 5
        assert isinstance(ei.value, ValueError)

    def test_input_forms(self) -> None:
        for src in ("a:1", b"a:1", bytearray(b"a:1"), memoryview(b"a:1")):
            assert jaro.loads(src) == {"a": 1}

    def test_top_level_forms(self) -> None:
        assert jaro.loads("~") is None
        assert jaro.loads("a:1 b:2") == {"a": 1, "b": 2}
        assert jaro.loads('"a:b"') == "a:b"
        with pytest.raises(jaro.JaroError):
            jaro.loads("1 2")
        assert jaro.dumps({}) == "{}"
        assert jaro.dumps([]) == "[]"


class TestLimits:
    def test_nesting_512_ok_513_rejected_text(self) -> None:
        assert jaro.loads("[" * 513 + "]" * 513) == nest_list_empty(513)
        with pytest.raises(jaro.JaroError):
            jaro.loads("[" * 514 + "]" * 514)
        with pytest.raises(jaro.JaroError):
            jaro.loads("{a:" * 513 + "1" + "}" * 513)
        with pytest.raises(jaro.JaroError):
            jaro.loads("&x " * 513 + "1")
        assert jaro.loads("&x " * 512 + "1") == 1

    def test_nesting_limit_encode(self) -> None:
        for build in (nest_list, nest_dict):
            ok = build(512)
            assert same(jaro.loads(jaro.dumps(ok)), ok)
            assert same(jaro.loads_binary(jaro.dumps_binary(ok)), ok)
            deep = build(514)
            with pytest.raises(jaro.JaroError):
                jaro.dumps(deep)
            with pytest.raises(jaro.JaroError):
                jaro.dumps_binary(deep)

    def test_nesting_limit_binary(self) -> None:
        head = bytes([0x0F, 0x01])
        assert (
            jaro.loads_binary(head + bytes([0x07, 0x01]) * 512 + bytes([0x80]))
            is not None
        )
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(head + bytes([0x07, 0x01]) * 513 + bytes([0x80]))

    def test_deep_input_does_not_hit_recursion_limit(self) -> None:
        with pytest.raises(jaro.JaroError):
            jaro.loads("[" * 100_000)
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(bytes([0x0F, 0x01]) + bytes([0x07, 0x01]) * 100_000)

    def test_text_reference_bomb(self) -> None:
        src = "a0:&x0 [1 2 3 4 5 6 7 8]\n" + "".join(
            f"a{i}:&x{i} [*x{i - 1} *x{i - 1}]\n" for i in range(1, 30)
        )
        with pytest.raises(jaro.JaroError, match="reference expansion"):
            jaro.loads(src)

    def test_reference_budget_is_2_pow_24(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        assert _core.MAX_EXPAND == 1 << 24
        monkeypatch.setattr(text, "MAX_EXPAND", 100)
        src = "a:&x [1 2 3 4 5 6 7 8 9] " + " ".join(f"r{i}:*x" for i in range(9))
        assert len(jaro.loads(src)) == 10
        with pytest.raises(jaro.JaroError, match="reference expansion"):
            jaro.loads(src + " last:*x end:*x")

    def test_binary_reference_bomb(self) -> None:
        anchor = bytes([0x0A, 0x07, 0x7F]) + bytes([0x01]) * 127
        n = 140_000
        refs = bytes([0x0B, 0x00]) * n
        count = bytearray()
        _put(count, n + 1)
        doc = bytes([0x0F, 0x01, 0x07]) + bytes(count) + anchor + refs
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(doc)


def nest_list_empty(n: int) -> Any:
    x: Any = []
    for _ in range(n - 1):
        x = [x]
    return x


def _put(buf: bytearray, n: int) -> None:
    while n >= 0x80:
        buf.append((n & 0x7F) | 0x80)
        n >>= 7
    buf.append(n)


class TestBinary:
    def test_header(self) -> None:
        assert jaro.dumps_binary(None) == bytes([0x0F, 0x01, 0x00])
        for bad in (
            b"",
            b"\x0f",
            b"\x00",
            b"\x0f\x02\x00",
            b"\x0f\x01",
            b"\x0f\x01\x00\x00",
        ):
            with pytest.raises(jaro.JaroError):
                jaro.loads_binary(bad)

    def test_tags(self) -> None:
        assert jaro.dumps_binary(5).hex() == "0f0185"
        assert jaro.dumps_binary(128).hex() == "0f01038002"
        assert jaro.dumps_binary(-1).hex() == "0f010301"
        assert jaro.dumps_binary("hi").hex() == "0f014268" + "69"
        assert jaro.dumps_binary(1.5).hex() == "0f01040000000000" + "00f83f"

    def test_string_pool(self) -> None:
        blob = jaro.dumps_binary(["same", "same", "x", "x"])
        assert jaro.loads_binary(blob) == ["same", "same", "x", "x"]
        assert blob.count(b"same") == 1

    def test_invalid_utf8_rejected(self) -> None:
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(bytes([0x0F, 0x01, 0x42, 0xFF, 0xFE]))

    def test_nan_float_rejected(self) -> None:
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(
                bytes([0x0F, 0x01, 0x04]) + bytes.fromhex("000000000000f87f")
            )

    def test_duplicate_keys_rejected(self) -> None:
        # object {"a": 1, "a": 2}
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(bytes.fromhex("0f0108024161" + "81" + "06" + "00" + "82"))
        # table with columns a, a
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(
                bytes.fromhex(
                    "0f01090241" + "61" + "0600" + "01" + "00" + "81" + "00" + "81"
                )
            )

    def test_counts_checked_against_input(self) -> None:
        for doc in ("0f0107ffffff7f", "0f010bff", "0f010c" + "ffffff7f", "0f010d01"):
            with pytest.raises(jaro.JaroError):
                jaro.loads_binary(bytes.fromhex(doc))

    def test_uvarint_limits(self) -> None:
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(bytes.fromhex("0f0107" + "ff" * 10 + "01"))
        with pytest.raises(jaro.JaroError):
            jaro.loads_binary(bytes.fromhex("0f0107" + "ff" * 9 + "02"))

    def test_delta_wraps_like_int64(self) -> None:
        vals = [2**63 - 1, -(2**63), 2**63 - 1, -(2**63), 0]
        assert jaro.loads_binary(jaro.dumps_binary(vals)) == vals

    def test_mixed_scale_decimals_stay_lossless(self) -> None:
        # Go's decimal column loses digits on this input; the encoder falls back to a generic column.
        vals = [4.380078842591e10, 1, 0.123456, 7.029148279305269e10, 2.5]
        assert jaro.loads_binary(jaro.dumps_binary(vals)) == vals

    def test_column_kinds_round_trip(self) -> None:
        cases: list[list[Any]] = [
            [1, 2, 3, 4, 5, 6],
            [10, 3, 77, 1000, -5],
            [1.5, 2.25, 3, 0.125],
            ["a", "b", "a", "b", "a", "b"],
            [True, False, True, True, False],
            [{"x": 1}, {"x": 2}, {"x": 3}, {"x": 4}],
            [[1], [2], [3], [4]],
            [None, None, None, None],
            [1, "a", None, 2.5],
        ]
        for c in cases:
            assert same(jaro.loads_binary(jaro.dumps_binary(c)), c), c
