"""Binary string-pool decode fast paths: 1, 2 and 3 byte references, bad references, truncation."""

from __future__ import annotations

from typing import Any

import pytest

import jaro
from jaro import JaroError


def doc(distinct: int) -> list[dict[str, Any]]:
    """Objects whose keys and values repeat, so later uses become pool references."""
    return [
        {f"key{i % distinct}": f"val{i % distinct}", "n": i}
        for i in range(distinct * 2)
    ]


@pytest.mark.parametrize("distinct", [3, 127, 128, 200, 5000, 17000])
def test_pool_reference_widths_round_trip(distinct: int) -> None:
    v = doc(distinct)
    assert jaro.loads_binary(jaro.dumps_binary(v)) == v


def test_reference_widths_in_object() -> None:
    v = {f"k{i}": [f"s{i}", f"s{i}"] for i in range(300)}
    assert jaro.loads_binary(jaro.dumps_binary(v)) == v


@pytest.mark.parametrize(
    "hexdoc",
    [
        "0f010601",  # reference to an empty pool
        "0f0107024141" + "0680",  # incomplete two-byte reference
        "0f0106",  # reference with no index
        "0f010841",  # object whose key tag is cut
    ],
)
def test_bad_references_rejected(hexdoc: str) -> None:
    with pytest.raises(JaroError):
        jaro.loads_binary(bytes.fromhex(hexdoc))


def test_truncated_wide_pool_raises_jaro_error() -> None:
    blob = jaro.dumps_binary(doc(300))
    for cut in range(2, len(blob), max(1, len(blob) // 40)):
        with pytest.raises(JaroError):
            jaro.loads_binary(blob[:cut])


def test_overlong_reference_index_still_resolves() -> None:
    # pool = ["AA"], then reference 0 written as the two bytes 80 00
    blob = bytes.fromhex("0f01" + "07" + "02" + "4241" + "41" + "06" + "8000")
    assert jaro.loads_binary(blob) == ["AA", "AA"]


@pytest.mark.parametrize(
    "values",
    [
        pytest.param(list(range(40)), id="delta-small"),
        pytest.param(
            [0, 1000, 5, 2**40, -(2**40), 2**62, -(2**62), 7, 8, 9], id="delta-wide"
        ),
        pytest.param([2**63 - 1, -(2**63), 2**63 - 1, 0, -1, 1], id="delta-wraps"),
        pytest.param([1.25, 2.5, -3.75, 100.5, 0.125, 1e6 + 0.5], id="decimal"),
        pytest.param([1.5, 2, 3.25, 4, 5.125, 6], id="decimal-mixed-ints"),
        pytest.param(["a", "bb", "a", "bb", "a", "bb", "a", "bb"], id="dict-small"),
        pytest.param([f"s{i % 200}" for i in range(500)], id="dict-wide"),
    ],
)
def test_column_kinds_round_trip(values: list[Any]) -> None:
    assert repr(jaro.loads_binary(jaro.dumps_binary(values))) == repr(
        jaro.loads(jaro.dumps(values))
    )


def test_bad_dictionary_index_offset_is_stable() -> None:
    blob = bytearray(jaro.dumps_binary(["a", "bb", "a", "bb", "a", "bb"]))
    blob[-1] = 0x7F
    with pytest.raises(JaroError) as e:
        jaro.loads_binary(bytes(blob))
    assert e.value.message == "bad dictionary index"
    assert e.value.offset == len(blob)


def test_truncated_columns_raise_jaro_error() -> None:
    for values in (
        list(range(100)),
        [1.5 * i for i in range(100)],
        [f"s{i % 7}" for i in range(100)],
    ):
        blob = jaro.dumps_binary(values)
        for cut in range(2, len(blob)):
            with pytest.raises(JaroError):
                jaro.loads_binary(blob[:cut])


@pytest.mark.parametrize(
    "hexdoc",
    [
        "0f0108010605" + "80",
        "0f01080106" + "8001" + "80",
        "0f010801068081",
        "0f01080106",
    ],
)
def test_bad_object_key_references(hexdoc: str) -> None:
    with pytest.raises(JaroError):
        jaro.loads_binary(bytes.fromhex(hexdoc))


def test_object_keys_through_wide_pool() -> None:
    v = [{f"k{i}": i, f"k{i + 1}": i} for i in range(400)]
    got = jaro.loads_binary(jaro.dumps_binary(v))
    assert got == v
    assert jaro.loads_binary(jaro.dumps_binary({"a": {"b": {"c": [1, {"d": 2}]}}})) == {
        "a": {"b": {"c": [1, {"d": 2}]}}
    }
