# jaro (Python)

Pure-Python codec for JARO, a JSON-model data format with a text form and a binary form. The format is specified in [`../SPEC.md`](../SPEC.md). Python 3.10 or later, no runtime dependencies, fully typed.

## Install

```
pip install .            # from this directory
pip install -e '.[dev]'  # tests, mypy and the YAML benchmark
```

## API

```python
import jaro

jaro.dumps({"a": [1, 2.5, None]})            # 'a:[1 2.5 ~]'
jaro.loads("a:[1 2.5 ~]")                    # {'a': [1, 2.5, None]}
jaro.dumps_binary(value, dedup=False)        # bytes
jaro.loads_binary(data)                      # value
```

- `dumps(value, *, dedup=False) -> str` and `loads(text: str | bytes) -> Any`.
- `dumps_binary(value, *, dedup=False) -> bytes` and `loads_binary(data: bytes) -> Any`.
- `dedup=True` replaces repeated sub-trees (24 bytes or more of text) with anchors and references.
- `JaroError` subclasses `ValueError`. Its `offset` attribute is the byte offset in the input, or `None` for encoder errors.

Mapping: `None`, `bool`, `int` (int64), `float` (finite), `str`, `list` and `dict` with `str` keys. Encoders sort keys. Decoders reject duplicate keys. Integral floats below 2^53 decode as `int`, and zero is always `0`. Tuples, sets and bytes are rejected.

Supported: text and binary forms, anchors, spreads, comments, tables (plain, sparse with `^`, keyed `{|k| ...}`, nested columns `key{sub}`). Not supported: schema validation, the `Parse`/`Doc` arena, HTTP middleware and zstd.

## Conformance

`tests/test_conformance.py` runs `../conformance/vectors.json`: every `text`, `binary`, `badText`, `badTextHex` and `badBinaryHex` case, a round trip of every `trees` entry, and byte-exact parity with the Go reference for every vector that has a tree (text, binary and binary with dedup). The parity allowlists in that file are empty.

```
pytest -q
mypy --strict src
```

## Limits

- Nesting depth 512, reference expansion 2^24 nodes, binary counts checked against the remaining input and a node budget of 4M plus 32 per input byte. Codecs use about one Python frame per nesting level, so depth 512 works under the default recursion limit. A `RecursionError` from a very deep caller stack is reported as `JaroError`.
- Invalid UTF-8 is rejected on decode. A lone surrogate in a `str` is rejected on encode.
- Binary floats that are NaN or infinite are rejected on decode.

## Benchmark

```
python bench/bench.py                 # corpus: $JARO_CORPUS, default ../../corpus
```

It prints encode and decode milliseconds and output bytes against `json` and PyYAML (libyaml loader when available), and writes `bench/results.json`.

## Optional native decoder

`loads_binary` uses the `jaro_native` Rust extension when it is installed (`pip install jaro-native`, built from `native-py/`). Without it, the pure Python decoder runs. Set `JARO_PURE=1` to force the pure decoder. `jaro.binary.NATIVE` reports which one is active. `jaro.binary.loads_binary_py` always runs the pure decoder.
