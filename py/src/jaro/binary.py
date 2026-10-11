"""JARO binary encoder and decoder."""

from __future__ import annotations

import math
import os
import struct
from functools import lru_cache
from itertools import chain
from typing import Any, NoReturn

from ._core import (INT64_MAX, INT64_MIN, MAX_DEPTH, MAX_EXACT, MAX_EXPAND,
                    as_number, deep_copy, norm_float, sorted_keys, wrap64)
from .columns import (BOOL, DECIMAL, DELTA, DICT, GENERIC, MAX_DECIMALS,
                      MIN_COLUMN, OBJECT, POW10, Plan, classify,
                      round_half_away, zigzag)
from .dedup import Dedup
from .errors import JaroError
from .tables import BIN_PRESENT, sparse_keys, table_data
from .text import _FAST_TYPES, _coerce, _plain_text

MAGIC = 0x0F
VERSION = 0x01
T_NULL, T_FALSE, T_TRUE, T_INT, T_FLOAT, T_STR, T_REF, T_ARR, T_OBJ, T_TABLE = range(10)
T_ANCHOR, T_ANCHOR_REF, T_COL, T_SPARSE = 0x0A, 0x0B, 0x0C, 0x0D
T_SHORT_STR = 0x40  # 0x40|len, len < 32
T_SMALL_INT = 0x80  # 0x80|n, n < 128
MIN_POOLED = 2  # strings shorter than this are never pooled
MAX_COUNT = 0x7FFFFFFF
NODES_BASE = 1 << 22
NODES_PER_BYTE = 32
_unpack_d = struct.Struct("<d").unpack_from
_MAX_ROW = 16
MATRIX_CHUNK = 64
_is_integer = float.is_integer
_isfinite = math.isfinite


@lru_cache(maxsize=_MAX_ROW + 1)
def _row_struct(k: int) -> struct.Struct:
    return struct.Struct("<xx" + "xd" * k)


def _float_matrix(data: bytes, pos: int, cnt: int) -> list[list[float]] | None:
    """Decode cnt consecutive same-length arrays of finite non-integral floats at pos, or None."""
    if pos + 1 >= len(data):
        return None
    k = data[pos + 1]
    if not 0 < k <= _MAX_ROW:
        return None
    stride = 2 + 9 * k
    stop = pos + cnt * stride
    if stop > len(data) or data[pos:stop:stride] != bytes((T_ARR,)) * cnt:
        return None
    if data[pos + 1 : stop : stride] != bytes((k,)) * cnt:
        return None
    floats = bytes((T_FLOAT,)) * cnt
    for j in range(k):
        if data[pos + 2 + 9 * j : stop : stride] != floats:
            return None
    rows = list(map(list, _row_struct(k).iter_unpack(data[pos:stop])))
    vals = list(chain.from_iterable(rows))
    if not all(map(_isfinite, vals)) or any(map(_is_integer, vals)):
        return None
    return rows


def _put_uvarint(buf: bytearray, n: int) -> None:
    while n >= 0x80:
        buf.append((n & 0x7F) | 0x80)
        n >>= 7
    buf.append(n)


def _put_varint(buf: bytearray, v: int) -> None:
    _put_uvarint(buf, zigzag(v))


class _Encoder:
    def __init__(self, dd: Dedup | None) -> None:
        self.buf = bytearray((MAGIC, VERSION))
        self.pool: dict[str, int] = {}
        self.dd = dd

    def _str(self, s: str) -> None:
        buf = self.buf
        try:
            b = s.encode("utf-8")
        except UnicodeEncodeError:
            raise JaroError("string is not valid UTF-8 (lone surrogate)") from None
        size = len(b)
        if size >= MIN_POOLED:
            idx = self.pool.get(s)
            if idx is not None:
                buf.append(T_REF)
                _put_uvarint(buf, idx)
                return
            self.pool[s] = len(self.pool)
        if size < 32:
            buf.append(T_SHORT_STR | size)
        else:
            buf.append(T_STR)
            _put_uvarint(buf, size)
        buf += b

    def _enc(self, v: Any, depth: int) -> None:
        if depth > MAX_DEPTH:
            raise JaroError("nesting too deep")
        buf = self.buf
        t = type(v)
        if t not in _FAST_TYPES:
            v, t = _coerce(v)
        if t is str:
            self._str(v)
            return
        if t is int:
            if v < INT64_MIN or v > INT64_MAX:
                raise JaroError(f"integer {v} does not fit in int64")
            if 0 <= v < 128:
                buf.append(T_SMALL_INT | v)
            else:
                buf.append(T_INT)
                _put_varint(buf, v)
            return
        if v is None:
            buf.append(T_NULL)
            return
        if t is bool:
            buf.append(T_TRUE if v else T_FALSE)
            return
        if t is float:
            n = norm_float(v)
            if isinstance(n, int):
                if 0 <= n < 128:
                    buf.append(T_SMALL_INT | n)
                else:
                    buf.append(T_INT)
                    _put_varint(buf, n)
            else:
                buf.append(T_FLOAT)
                buf += struct.pack("<d", n)
            return
        dd = self.dd
        if dd is not None:
            ref = dd.ref(v)
            if ref is not None:
                if ref[1]:
                    buf.append(T_ANCHOR_REF)
                    _put_uvarint(buf, ref[0])
                    return
                buf.append(T_ANCHOR)
        if t is dict:
            buf.append(T_OBJ)
            _put_uvarint(buf, len(v))
            for k in sorted_keys(v):
                self._str(k)
                self._enc(v[k], depth + 1)
            return
        td = table_data(v)
        if td is not None:
            keys, cols = td
            buf.append(T_TABLE)
            _put_uvarint(buf, len(keys))
            for k in keys:
                self._str(k)
            _put_uvarint(buf, len(v))
            for col in cols:
                self._column(col, depth + 1)
            return
        sk = sparse_keys(v, BIN_PRESENT)
        if sk is not None:
            buf.append(T_SPARSE)
            _put_uvarint(buf, len(sk))
            for k in sk:
                self._str(k)
            _put_uvarint(buf, len(v))
            nb = (len(v) + 7) // 8
            for k in sk:
                vals = [r[k] for r in v if k in r]
                if len(vals) == len(v):
                    buf.append(0)
                else:
                    bits = bytearray(nb)
                    for i, r in enumerate(v):
                        if k in r:
                            bits[i >> 3] |= 1 << (i & 7)
                    buf.append(1)
                    buf += bits
                self._column(vals, depth + 1)
            return
        if len(v) >= MIN_COLUMN:
            plan = classify(v)
            if plan.kind != GENERIC:
                buf.append(T_COL)
                _put_uvarint(buf, len(v))
                self._column(v, depth + 1, plan)
                return
        buf.append(T_ARR)
        _put_uvarint(buf, len(v))
        for el in v:
            self._enc(el, depth + 1)

    def _column(self, vals: list[Any], depth: int, plan: Plan | None = None) -> None:
        if depth > MAX_DEPTH:
            raise JaroError("nesting too deep")
        buf = self.buf
        p = plan if plan is not None else classify(vals)
        buf.append(p.kind)
        kind = p.kind
        if kind == GENERIC:
            for v in vals:
                self._enc(v, depth + 1)
        elif kind == DELTA:
            prev = 0
            for v in vals:
                i = int(as_number(v))
                _put_varint(buf, wrap64(i - prev))
                prev = i
        elif kind == DECIMAL:
            buf.append(p.k & 7)
            scale = POW10[p.k]
            for v in vals:
                x = as_number(v)
                if p.k == 0:
                    _put_varint(buf, int(x))
                else:
                    _put_varint(buf, round_half_away(float(x) * scale))
        elif kind == DICT:
            _put_uvarint(buf, len(p.dict))
            for s in p.dict:
                self._str(s)
            for v in vals:
                _put_uvarint(buf, p.idx[v])
        elif kind == BOOL:
            bits = bytearray((len(vals) + 7) // 8)
            for i, b in enumerate(vals):
                if b:
                    bits[i >> 3] |= 1 << (i & 7)
            buf += bits
        else:
            _put_uvarint(buf, len(p.keys))
            for k in p.keys:
                self._str(k)
            for col in p.cols:
                self._column(col, depth + 1)


def dumps_binary(value: Any, *, dedup: bool = False) -> bytes:
    """Encode value as JARO binary. dedup replaces repeated sub-trees with anchors."""
    try:
        dd = Dedup(value, _plain_text, True) if dedup else None
        e = _Encoder(dd)
        e._enc(value, 0)
        return bytes(e.buf)
    except RecursionError:
        raise JaroError("nesting too deep") from None


class _Decoder:
    def __init__(self, data: bytes) -> None:
        self.s = data
        self.n = len(data)
        self.pos = 2
        self.pool: list[str] = []
        self.anchors: list[Any] = []
        self.filled: list[bool] = []
        self.budget = MAX_EXPAND
        self.nodes = NODES_BASE + NODES_PER_BYTE * len(data)

    def _err(self, msg: str, pos: int | None = None) -> NoReturn:
        raise JaroError(msg, self.pos if pos is None else pos)

    def _short(self) -> NoReturn:
        self._err("truncated input")

    def _uvarint(self) -> int:
        s = self.s
        pos = self.pos
        try:
            b = s[pos]
        except IndexError:
            self._short()
        if b < 0x80:
            self.pos = pos + 1
            return b
        x = b & 0x7F
        shift = 7
        for i in range(1, 10):
            if pos + i >= self.n:
                self._short()
            b = s[pos + i]
            if b < 0x80:
                if i == 9 and b > 1:
                    self._err("varint overflows 64 bits")
                self.pos = pos + i + 1
                return x | (b << shift)
            x |= (b & 0x7F) << shift
            shift += 7
        self._err("varint overflows 64 bits")

    def _varint(self) -> int:
        u = self._uvarint()
        x = u >> 1
        return ~x if u & 1 else x

    def _count(self) -> int:
        """Read an element count; it cannot exceed the bytes left."""
        n = self._uvarint()
        if n > MAX_COUNT or n > self.n - self.pos:
            self._short()
        return n

    def _row_count(self) -> int:
        """Read a row count; bool columns take one bit per value, so the bound is 8x the bytes left."""
        n = self._uvarint()
        if n > MAX_COUNT or n > (self.n - self.pos) * 8:
            self._short()
        return n

    def _charge(self, n: int) -> None:
        self.nodes -= n
        if self.nodes < 0:
            self._err("decoded size too large")

    def _str_bytes(self, n: int) -> str:
        pos = self.pos
        if n > self.n - pos:
            self._short()
        try:
            s = self.s[pos : pos + n].decode("utf-8")
        except UnicodeDecodeError:
            self._err("string is not valid UTF-8", pos)
        self.pos = pos + n
        if n >= MIN_POOLED:
            self.pool.append(s)
        return s

    def _key(self) -> str:
        """Read a string used as an object key or table column."""
        pos = self.pos
        try:
            t = self.s[pos]
        except IndexError:
            self._short()
        self.pos = pos + 1
        if t == T_REF:
            p = pos + 1
            if p < self.n:
                idx = self.s[p]
                if idx >= 0x80:
                    p += 1
                    idx = (
                        (idx & 0x7F) | (self.s[p] << 7)
                        if p < self.n and self.s[p] < 0x80
                        else -1
                    )
                if 0 <= idx < len(self.pool):
                    self.pos = p + 1
                    return self.pool[idx]
            return self._ref()
        if T_SHORT_STR <= t < T_SHORT_STR + 32:
            return self._str_bytes(t & 31)
        if t == T_STR:
            return self._str_bytes(self._count())
        self._err("key must be a string", pos)

    def _ref(self) -> str:
        idx = self._uvarint()
        if idx >= len(self.pool):
            self._err("bad string reference")
        return self.pool[idx]

    def _header(self) -> list[str]:
        nc = self._count()
        if nc == 0:
            self._err("empty table header")
        cols: list[str] = []
        seen: set[str] = set()
        for _ in range(nc):
            k = self._key()
            if k in seen:
                self._err(f"duplicate column {k!r}")
            seen.add(k)
            cols.append(k)
        return cols

    def _value(self, depth: int) -> Any:
        if depth > MAX_DEPTH:
            self._err("nesting too deep")
        pos = self.pos
        try:
            t = self.s[pos]
        except IndexError:
            self._short()
        self.pos = pos + 1
        if t >= T_SMALL_INT:
            return t & 0x7F
        if T_SHORT_STR <= t < T_SHORT_STR + 32:
            return self._str_bytes(t & 31)
        if t == T_ARR:
            items: list[Any] = []
            cnt = self._count()
            if depth >= MAX_DEPTH:
                for _ in range(cnt):
                    items.append(self._value(depth + 1))
                return items
            data = self.s
            end = self.n
            pos = self.pos
            if cnt > 1 and depth < MAX_DEPTH - 1:
                while cnt > 1 and pos < end and data[pos] == T_ARR:
                    run = min(cnt, MATRIX_CHUNK)
                    rows = _float_matrix(data, pos, run)
                    if rows is not None:
                        items += rows
                        pos += run * (2 + 9 * len(rows[0]))
                    else:
                        for _ in range(run):
                            self.pos = pos
                            items.append(self._value(depth + 1))
                            pos = self.pos
                    cnt -= run
            for _ in range(cnt):
                if pos < end:
                    t = data[pos]
                    if t >= T_SMALL_INT:
                        items.append(t & 0x7F)
                        pos += 1
                        continue
                    if t == T_FLOAT and end - pos >= 9:
                        f = _unpack_d(data, pos + 1)[0]
                        if not (f.is_integer() or f - f != 0):
                            items.append(f)
                            pos += 9
                            continue
                self.pos = pos
                items.append(self._value(depth + 1))
                pos = self.pos
            self.pos = pos
            return items
        if t == T_REF:
            p = pos + 1
            if p < self.n:
                idx = self.s[p]
                if idx >= 0x80:
                    p += 1
                    idx = (
                        (idx & 0x7F) | (self.s[p] << 7)
                        if p < self.n and self.s[p] < 0x80
                        else -1
                    )
                if 0 <= idx < len(self.pool):
                    self.pos = p + 1
                    return self.pool[idx]
            return self._ref()
        if t == T_NULL:
            return None
        if t == T_FALSE:
            return False
        if t == T_TRUE:
            return True
        if t == T_INT:
            return self._varint()
        if t == T_FLOAT:
            if self.n - self.pos < 8:
                self._short()
            f = struct.unpack_from("<d", self.s, self.pos)[0]
            self.pos += 8
            try:
                return norm_float(f)
            except JaroError:
                self._err("float is not finite", self.pos - 8)
        if t == T_STR:
            return self._str_bytes(self._count())
        if t == T_OBJ:
            obj: dict[str, Any] = {}
            data = self.s
            pool = self.pool
            for _ in range(self._count()):
                pos = self.pos
                idx = -1
                if pos + 2 < self.n and data[pos] == T_REF:
                    idx = data[pos + 1]
                    if idx < 0x80:
                        pos += 2
                    elif data[pos + 2] < 0x80:
                        idx = (idx & 0x7F) | (data[pos + 2] << 7)
                        pos += 3
                    else:
                        idx = -1
                if 0 <= idx < len(pool):
                    k = pool[idx]
                    self.pos = pos
                else:
                    k = self._key()
                v = self._value(depth + 1)
                if k in obj:
                    self._err(f"duplicate key {k!r}")
                obj[k] = v
            return obj
        if t == T_TABLE:
            return self._table(depth)
        if t == T_SPARSE:
            return self._sparse(depth)
        if t == T_COL:
            return self._column(self._row_count(), depth + 1)
        if t == T_ANCHOR:
            idx = len(self.anchors)
            self.anchors.append(None)
            self.filled.append(False)
            v = self._value(depth + 1)
            self.anchors[idx] = v
            self.filled[idx] = True
            return v
        if t == T_ANCHOR_REF:
            idx = self._uvarint()
            if idx >= len(self.anchors) or not self.filled[idx]:
                self._err("bad anchor reference")
            v, self.budget = deep_copy(self.anchors[idx], self.budget)
            if self.budget < 0:
                self._err("reference expansion too large")
            return v
        self._err(f"bad tag 0x{t:02x}", pos)

    def _table(self, depth: int) -> list[Any]:
        cols = self._header()
        nr = self._row_count()
        rows: list[dict[str, Any]] = [{} for _ in range(nr)]
        for k in cols:
            col = self._column(nr, depth + 1)
            for m, x in zip(rows, col):
                m[k] = x
        return rows

    def _sparse(self, depth: int) -> list[Any]:
        cols = self._header()
        nr = self._row_count()
        self._charge(nr * 2)
        rows: list[dict[str, Any]] = [{} for _ in range(nr)]
        for k in cols:
            if self.pos >= self.n:
                self._short()
            flag = self.s[self.pos]
            self.pos += 1
            bits: bytes | None = None
            n = nr
            if flag == 1:
                nb = (nr + 7) // 8
                if self.n - self.pos < nb:
                    self._short()
                bits = self.s[self.pos : self.pos + nb]
                self.pos += nb
                n = int.from_bytes(bits, "little").bit_count()
                if n > nr or (nr & 7 and bits[-1] >> (nr & 7)):
                    self._err("bad presence bitmap")
            elif flag != 0:
                self._err("bad presence flag")
            col = self._column(n, depth + 1)
            if bits is None:
                for m, x in zip(rows, col):
                    m[k] = x
            else:
                j = 0
                for i, m in enumerate(rows):
                    if (bits[i >> 3] >> (i & 7)) & 1:
                        m[k] = col[j]
                        j += 1
        return rows

    def _column(self, n: int, depth: int) -> list[Any]:
        if depth > MAX_DEPTH:
            self._err("nesting too deep")
        self._charge(n + 1)
        try:
            kind = self.s[self.pos]
        except IndexError:
            self._short()
        self.pos += 1
        out: list[Any] = []
        if kind == GENERIC:
            for _ in range(n):
                out.append(self._value(depth + 1))
        elif kind == DELTA:
            prev = 0
            data = self.s
            end = self.n
            pos = self.pos
            for _ in range(n):
                if pos < end and data[pos] < 0x80:
                    u = data[pos]
                    pos += 1
                else:
                    self.pos = pos
                    u = self._uvarint()
                    pos = self.pos
                prev += (u >> 1) ^ -(u & 1)
                if prev < INT64_MIN or prev > INT64_MAX:
                    prev = wrap64(prev)
                out.append(prev)
            self.pos = pos
        elif kind == DECIMAL:
            if self.pos >= self.n or self.s[self.pos] > MAX_DECIMALS:
                self._err("bad decimal scale")
            k = self.s[self.pos]
            self.pos += 1
            scale = POW10[k]
            data = self.s
            end = self.n
            pos = self.pos
            for _ in range(n):
                if pos < end and data[pos] < 0x80:
                    u = data[pos]
                    pos += 1
                else:
                    self.pos = pos
                    u = self._uvarint()
                    pos = self.pos
                m = (u >> 1) ^ -(u & 1)
                if k == 0:
                    out.append(m)
                    continue
                f = m / scale
                out.append(int(f) if f.is_integer() and abs(f) < MAX_EXACT else f)
            self.pos = pos
        elif kind == DICT:
            nd = self._count()
            dictionary = [self._key() for _ in range(nd)]
            data = self.s
            end = self.n
            pos = self.pos
            for _ in range(n):
                if pos < end and data[pos] < 0x80:
                    idx = data[pos]
                    pos += 1
                else:
                    self.pos = pos
                    idx = self._uvarint()
                    pos = self.pos
                if idx >= nd:
                    self.pos = pos
                    self._err("bad dictionary index")
                out.append(dictionary[idx])
            self.pos = pos
        elif kind == BOOL:
            nb = (n + 7) // 8
            if self.n - self.pos < nb:
                self._short()
            s = self.s
            base = self.pos
            out = [bool((s[base + (i >> 3)] >> (i & 7)) & 1) for i in range(n)]
            self.pos += nb
        elif kind == OBJECT:
            nk = self._count()
            if nk == 0:
                self._err("empty object column")
            keys: list[str] = []
            seen: set[str] = set()
            for _ in range(nk):
                k2 = self._key()
                if k2 in seen:
                    self._err(f"duplicate key {k2!r}")
                seen.add(k2)
                keys.append(k2)
            maps: list[dict[str, Any]] = [{} for _ in range(n)]
            for k2 in keys:
                col = self._column(n, depth + 1)
                for m2, x in zip(maps, col):
                    m2[k2] = x
            return maps
        else:
            self._err("bad column kind", self.pos - 1)
        return out


def loads_binary_py(data: bytes | bytearray | memoryview) -> Any:
    """Decode JARO binary in pure Python."""
    raw = bytes(data)
    if len(raw) < 2 or raw[0] != MAGIC:
        raise JaroError("not a JARO binary document", 0)
    if raw[1] != VERSION:
        raise JaroError(f"unsupported binary version {raw[1]}", 1)
    d = _Decoder(raw)
    try:
        v = d._value(0)
    except RecursionError:
        raise JaroError("nesting too deep") from None
    if d.pos != d.n:
        raise JaroError("trailing data", d.pos)
    return v


try:
    if os.environ.get("JARO_PURE"):
        raise ImportError
    from jaro_native import \
        JaroError as \
        _NativeError  # type: ignore[import-not-found,unused-ignore]
    from jaro_native import \
        loads_binary as \
        _native_loads  # type: ignore[import-not-found,unused-ignore]
except ImportError:
    NATIVE = False
else:
    NATIVE = True


def loads_binary(data: bytes | bytearray | memoryview) -> Any:
    """Decode JARO binary, using the optional jaro_native extension when installed."""
    if not NATIVE:
        return loads_binary_py(data)
    try:
        return _native_loads(data)
    except _NativeError as e:
        raise JaroError(e.message, e.offset) from None
