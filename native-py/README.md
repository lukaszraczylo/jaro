# jaro-native

Optional native decoder for the JARO binary format. It is a Rust and PyO3 extension. It exposes one function:

```python
import jaro_native
value = jaro_native.loads_binary(data)  # bytes, bytearray or memoryview
```

The result matches the pure-Python `jaro.binary.loads_binary`: dict, list, str, int (int64), float, bool and None. Integral floats below 2^53, and zero, are ints. Invalid input raises `jaro_native.JaroError`, a subclass of `ValueError`. The error has `.message` and `.offset`, and the text is `jaro: offset N: message`, as in the pure decoder.

This is a prototype. It decodes binary only. There is no encoder and no text decoder.

## Build and install

Use a virtual environment. Do not install into the system Python.

```sh
python3 -m venv .venv
.venv/bin/pip install maturin==1.15.0 pytest
unset CONDA_PREFIX                      # maturin refuses to run when both CONDA_PREFIX and VIRTUAL_ENV are set
VIRTUAL_ENV=$PWD/.venv PATH=$PWD/.venv/bin:$PATH maturin develop --release
```

If `CONDA_PREFIX` is set and `VIRTUAL_ENV` is not, `maturin develop` installs into the conda environment. Activate the venv first.

## Wheels

The crate uses PyO3 with the `abi3-py310` feature. One wheel serves every CPython 3.10 or later on the same platform. `maturin build --release` on macOS arm64 produces `jaro_native-0.1.0-cp310-abi3-macosx_11_0_arm64.whl`, about 208 KB. The wheel has no Python dependencies. PyPy and free-threaded builds are not supported. Build one wheel per OS and CPU, for example with `maturin build --release --target universal2-apple-darwin` or the manylinux images.

The versions pinned for the build: maturin 1.15.0 (released 2026-08-24) and pyo3 0.29.2 (released 2026-08-05). `Cargo.lock` pins the transitive crates.

## Tests and benchmark

```sh
.venv/bin/python -m pytest tests        # needs ../py/src for the pure encoder and decoder (added to sys.path)
.venv/bin/python bench/native_bench.py [rounds]
```

The tests cover the conformance vectors (good and bad), the six corpus files (binaries from the Go CLI when `go` is installed, else from the pure encoder), 2,500 random trees against the pure decoder, 30,000 mutated inputs that must give the same value or the same error message as the pure decoder, depth and size limits, a 512 KB-stack thread, and RSS growth over 10,000 rounds.

## Safety notes

- Every input-dependent read uses checked access (`get`, `checked` sizes). There is no `unwrap` or index on input-dependent paths. Panics stay in the default unwinding mode, so a bug becomes a Python exception and not an abort.
- The decoder checks tags, counts against the remaining input, rows at 8 times the remaining bytes, the node budget (4M plus 32 per input byte), depth 512, UTF-8 validity, anchors, presence bitmaps with padding bits, non-finite floats, duplicate keys and bad pool references.
- Every strong reference lives in an `Owned` guard. An error path drops it exactly once. Lists are filled with `PyList_SetItem`, which also steals the reference on failure.
- Table row dicts are owned by the result list from creation, so an early error frees them with the list.
- The decoder holds the GIL for the whole call.
- Under abi3 the interpreter-internal macros (`PyList_SET_ITEM`, `Py_INCREF` inlines) are not available. Reference counting and list stores are function calls. This costs some speed against a version-specific build.
- Anchored values are copied with the same node budget as the pure decoder (2^24).
