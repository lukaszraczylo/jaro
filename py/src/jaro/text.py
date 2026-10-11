"""JARO text encoder and decoder."""

from __future__ import annotations

import json
import math
import re
from collections.abc import Callable
from functools import lru_cache
from itertools import chain
from typing import Any, NoReturn

from ._core import (ABSENT, INT64_MAX, INT64_MIN, MAX_DEPTH, MAX_EXACT,
                    MAX_EXPAND, deep_copy, norm_float, sorted_keys)
from .dedup import Dedup
from .errors import JaroError
from .tables import (MAX_NEST, ColSpec, build_row, flat_keys, keyed_specs,
                     leaf_cells, leaf_count, table_specs)

_BARE = re.compile(r'[^\x00-\x20"{}\[\]|:\\#]+')
_WS = re.compile(r"(?:[ \t\r\n]+|#[^\n]*)*")
_QPLAIN = re.compile(r'[^"\\]*')
_QKEY = re.compile(r'"(?:[^"\\]|\\.)*"', re.S)
_BAD_ESC = re.compile(r'\\(?!["\\nrt]|u[0-9a-fA-F]{4})')
_SURROGATE_ESC = re.compile(r"\\u[dD][89a-fA-F]")
_HEX4 = re.compile(r"[0-9a-fA-F]{4}")
_LOW_SURROGATE = re.compile(r"\\u([dD][c-fC-F][0-9a-fA-F]{2})")
_INT = re.compile(r"-?[0-9]+")
_NUMTOK = re.compile(r"(-?[0-9]+)|-?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?")
_PLAIN_FLOAT = re.compile(r"-?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?")
_PLAIN = re.compile(
    r'[A-Za-z_][^\x00-\x20"{}\[\]|:\\#]*'
)  # bare, and cannot read as a number or marker
_SURROGATE = re.compile("[\ud800-\udfff]")
_DELIMS = frozenset(' \t\n\r"{}[]|:\\#')
_NUM_START = frozenset("-.0123456789")
_KEY_START_NOT_PLAIN = frozenset("-.0123456789~")
_ws_match: Callable[[str, int], re.Match[str]] = _WS.match  # type: ignore[assignment]  # matches "", never None
_bare_match = _BARE.match
_ITEM_MATCH = re.compile(
    r'(-?[0-9]{1,17}\.[0-9]{1,300})(?![^\x00-\x20"{}\[\]|:\\#])|[^\x00-\x20"{}\[\]|:\\#]+'
).match
_FLAT_FLOATS = re.compile(
    r"\[(-?[0-9]{1,17}\.[0-9]{1,300}(?: -?[0-9]{1,17}\.[0-9]{1,300})*)\]"
).match
_CELL_MATCH = re.compile(
    r'[ \t\r\n]*(?:"([^"\\]*)"'
    r'|(-?[0-9]{1,17}\.[0-9]{1,300})(?![^\x00-\x20"{}\[\]|:\\#])'
    r'|(?![&*])([^\x00-\x20"{}\[\]|:\\#]+)|(\[\]|\{\}))'
).match
_PAIR_MATCH = re.compile(
    r'[ \t\r\n]*(?:"([^"\\]*)":|(?![-.0-9~&*])([^\x00-\x20"{}\[\]|:\\#]+):)'
    r'[ \t\r\n]*(?:"([^"\\]*)"'
    r'|(-?[0-9]{1,17}\.[0-9]{1,300})(?![^\x00-\x20"{}\[\]|:\\#])'
    r'|(?![&*])([^\x00-\x20"{}\[\]|:\\#]+)|(\[\]|\{\}))'
).match
_KEY_MATCH = re.compile(
    r'[ \t\r\n]*(?:"([^"\\]*)":' r'|(?![-.0-9~&*])([^\x00-\x20"{}\[\]|:\\#]+):)'
).match
_is_integer = float.is_integer
_WS_LEAD = " \t\r\n#"
_HEADER_MATCH = re.compile(
    r'([A-Za-z_][^\x00-\x20"{}\[\]|:\\#]*(?: [A-Za-z_][^\x00-\x20"{}\[\]|:\\#]*)*)\|'
).match
_NUM_SRC = r"-?(?:[0-9]{1,17}\.[0-9]{1,300}|[0-9]{1,15})"
_MAX_ROW = 16


@lru_cache(maxsize=_MAX_ROW + 1)
def _matrix_match(k: int) -> Callable[[str, int], re.Match[str] | None]:
    row = rf"\[{_NUM_SRC}(?: {_NUM_SRC}){{{k - 1}}}\]"
    return re.compile(rf"\[({row}(?: {row})*)\]").match


def _no_constant(name: str) -> NoReturn:
    raise ValueError(name)


_json_decode = json.JSONDecoder(parse_constant=_no_constant).decode
_LIMIT = 1e15
_is_float = float.__instancecheck__


def _float_rows(s: str, pos: int) -> tuple[list[list[Any]], int] | None:
    """Decode a `[[n n] [n n]]` array of equal-length rows of plain decimals and short ints at pos, or None."""
    if s.startswith("[", pos + 2):
        return None
    end = s.find("]", pos + 2)
    if end < 0:
        return None
    k = s.count(" ", pos + 2, end) + 1
    if k > _MAX_ROW:
        return None
    stop = s.find("]]", end) + 2
    if stop > 1:
        region = s[pos:stop]
        if "e" not in region and "E" not in region:
            try:
                rows = _json_decode(region.replace("] [", "],[").replace(" ", ","))
                flat = list(chain.from_iterable(rows))
                if (
                    set(map(len, rows)) == {k}
                    and not any(map(_is_integer, filter(_is_float, flat)))
                    and max(flat) < _LIMIT
                    and min(flat) > -_LIMIT
                ):
                    return rows, stop
            except (ValueError, TypeError):
                pass
    return _float_rows_re(s, pos, k)


def _float_rows_re(s: str, pos: int, k: int) -> tuple[list[list[Any]], int] | None:
    m = _matrix_match(k)(s, pos)
    if m is None:
        return None
    vals: list[Any] = list(map(float, m[1][1:-1].replace("] [", " ").split(" ")))
    if any(map(_is_integer, vals)):
        vals = [
            int(f) if f.is_integer() and -MAX_EXACT < f < MAX_EXACT else f for f in vals
        ]
    return list(map(list, zip(*[iter(vals)] * k))), m.end()


_ROW_TOK = (
    r'(?:"[^"\\]*"|(?![&*])[^\x00-\x20"{}\[\]|:\\#]+(?![^\x00-\x20"{}\[\]|:\\#]))'
)
_ROW_CELL = rf"[ \t\r\n]*({_ROW_TOK}|\[(?:[ \t\r\n]*{_ROW_TOK})*[ \t\r\n]*\]|\{{\}})"
_ROW_TOKENS = re.compile(r'"[^"\\]*"|[^\x00-\x20"{}\[\]|:\\#]+').findall
_MAX_ROW_CELLS = 64


@lru_cache(maxsize=_MAX_ROW_CELLS + 1)
def _row_match(k: int) -> Callable[[str, int], re.Match[str] | None]:
    return re.compile(_ROW_CELL * k).match


class _CellMemo(dict[str, Any]):
    def __missing__(self, tok: str) -> Any:
        c = tok[0]
        if c == "[":
            return [_conv_tok(x) for x in _ROW_TOKENS(tok, 1, len(tok) - 1)]
        if c == "{":
            return {}
        v = tok[1:-1] if c == '"' else ABSENT if tok == "^" else _bare_value(tok)
        self[tok] = v
        return v


def _conv_tok(tok: str) -> Any:
    return tok[1:-1] if tok[0] == '"' else _bare_value(tok)


_DIGITS = "0123456789"
_NO_BARE = re.compile(r'[\x00-\x20"{}\[\]|:\\#]')
_ESCAPES: dict[int, str] = {i: f"\\u{i:04x}" for i in range(0x20)}
_ESCAPES.update({ord('"'): '\\"', ord("\\"): "\\\\", 10: "\\n", 13: "\\r", 9: "\\t"})
_RESERVED = frozenset(("", "~", "^", "true", "false"))


def _num_like(s: str) -> bool:
    i = 1 if s[0] == "-" else 0
    if i >= len(s):
        return False
    c = s[i]
    return c in _DIGITS or (c == "." and i + 1 < len(s))


def _needs_quote(s: str) -> bool:
    if s in _RESERVED or s[0] in "&*" or _NO_BARE.search(s) is not None:
        return True
    return _num_like(s)


def _quote(s: str) -> str:
    if (
        _PLAIN.fullmatch(s) is not None and s != "true" and s != "false"
    ) or not _needs_quote(s):
        return s
    return '"' + s.translate(_ESCAPES) + '"'


def _go_float(f: float) -> str:
    """Format f like Go's strconv.FormatFloat(f, 'g', -1, 64)."""
    r = repr(f)
    if 1e-4 <= f < 1e6 or -1e6 < f <= -1e-4:  # repr and Go agree on plain notation here
        return r
    neg = r[0] == "-"
    if neg:
        r = r[1:]
    mant, _, exp = r.partition("e")
    ip, _, fp = mant.partition(".")
    digits = ip + fp
    dp = len(ip) + (int(exp) if exp else 0)
    stripped = digits.lstrip("0")
    dp -= len(digits) - len(stripped)
    digits = stripped.rstrip("0")
    x = dp - 1
    if x < -4 or x >= 6:
        body = digits[0] + ("." + digits[1:] if len(digits) > 1 else "")
        out = f"{body}e{'-' if x < 0 else '+'}{abs(x):02d}"
    elif dp <= 0:
        out = "0." + "0" * -dp + digits
    elif dp >= len(digits):
        out = digits + "0" * (dp - len(digits))
    else:
        out = digits[:dp] + "." + digits[dp:]
    return "-" + out if neg else out


def _fast(x: Any) -> str | None:
    """Return the text of a plain str or in-range int, or None for anything else."""
    t = type(x)
    if t is str:
        return _quote(x)
    if t is int and INT64_MIN <= x <= INT64_MAX:
        return str(x)
    return None


_FAST_TYPES = frozenset((type(None), bool, int, float, str, dict, list))


def _coerce(v: Any) -> tuple[Any, type]:
    """Map a subclass instance onto the exact type the encoder dispatches on."""
    if isinstance(v, bool):
        return bool(v), bool
    if isinstance(v, int):
        return int(v), int
    if isinstance(v, float):
        return float(v), float
    if isinstance(v, str):
        return str.__str__(v), str
    if isinstance(v, dict):
        return v, dict
    if isinstance(v, list):
        return v, list
    raise JaroError(f"unsupported type {type(v).__name__}")


class _Encoder:
    def __init__(self, dd: Dedup | None) -> None:
        self.out: list[str] = []
        self.dd = dd

    def run(self, v: Any) -> str:
        self._enc(v, 0, isinstance(v, dict) and len(v) > 0)
        text = "".join(self.out)
        if _SURROGATE.search(text) is not None:
            raise JaroError("string is not valid UTF-8 (lone surrogate)")
        return text

    def _header(self, specs: list[ColSpec]) -> None:
        out = self.out
        for i, s in enumerate(specs):
            if i:
                out.append(" ")
            out.append(_quote(s.key))
            if s.sub is not None:
                out.append("{")
                self._header(s.sub)
                out.append("}")

    # Containers are written inline so nesting costs one Python frame per level.
    def _enc(self, v: Any, depth: int, bare: bool = False) -> None:
        if depth > MAX_DEPTH:
            raise JaroError("nesting too deep")
        out = self.out
        t = type(v)
        if t not in _FAST_TYPES:
            v, t = _coerce(v)
        if t is str:
            out.append(_quote(v))
            return
        if t is int:
            if v < INT64_MIN or v > INT64_MAX:
                raise JaroError(f"integer {v} does not fit in int64")
            out.append(str(v))
            return
        if v is None:
            out.append("~")
            return
        if t is bool:
            out.append("true" if v else "false")
            return
        if t is float:
            n = norm_float(v)
            out.append(str(n) if isinstance(n, int) else _go_float(n))
            return
        dd = self.dd
        if dd is not None and not bare:
            ref = dd.ref(v)
            if ref is not None:
                if ref[1]:
                    out.append(f"*a{ref[0]}")
                    return
                out.append(f"&a{ref[0]} ")
        table: list[ColSpec] | None = None
        names: list[str] | None = None
        rows: list[Any]
        if t is dict:
            if not bare:
                table = keyed_specs(v)
            if table is None:
                if not bare:
                    out.append("{")
                first = True
                for k in sorted_keys(v):
                    if not first:
                        out.append(" ")
                    first = False
                    out.append(_quote(k))
                    out.append(":")
                    x = v[k]
                    r = _fast(x) if depth < MAX_DEPTH else None
                    if r is None:
                        self._enc(x, depth + 1)
                    else:
                        out.append(r)
                if not bare:
                    out.append("}")
                return
            names = sorted_keys(v)
            rows = [v[x] for x in names]
            out.append("{|")
            closer = "}"
        else:
            out.append("[")
            table = table_specs(v)
            if table is None:
                for i, x in enumerate(v):
                    if i:
                        out.append(" ")
                    r = _fast(x) if depth < MAX_DEPTH else None
                    if r is None:
                        self._enc(x, depth + 1)
                    else:
                        out.append(r)
                out.append("]")
                return
            rows = v
            out.append("|")
            closer = "]"
        self._header(table)
        out.append("|")
        keys = flat_keys(table)
        for i, row in enumerate(rows):
            if names is not None:
                out.append(" ")
                out.append(_quote(names[i]))
            if keys is not None:
                cells = [row.get(k, ABSENT) for k in keys]
            else:
                cells = []
                leaf_cells(table, row, cells)
            for c in cells:
                if c is ABSENT:
                    out.append(" ^")
                else:
                    out.append(" ")
                    r = _fast(c) if depth < MAX_DEPTH else None
                    if r is None:
                        self._enc(c, depth + 1)
                    else:
                        out.append(r)
        out.append(closer)


def _plain_text(v: Any) -> str:
    return _Encoder(None).run(v)


def dumps(value: Any, *, dedup: bool = False) -> str:
    """Encode value as JARO text. dedup replaces repeated sub-trees with anchors."""
    try:
        dd = Dedup(value, _plain_text, False) if dedup else None
        return _Encoder(dd).run(value)
    except RecursionError:
        raise JaroError("nesting too deep") from None


def _unescape(s: str, start: int) -> tuple[str, int] | None:
    """Decode the quoted body at start when it uses only plain escapes; return it and the end after the quote."""
    end = s.find('"', start)
    while end >= 0:
        j = end
        while s[j - 1] == "\\":
            j -= 1
        if (end - j) & 1 == 0:
            break
        end = s.find('"', end + 1)
    else:
        return None
    body = s[start:end]
    if _BAD_ESC.search(body) is not None:
        return None
    if "\\u" in body and _SURROGATE_ESC.search(body) is not None:
        return None
    return body.encode("ascii", "backslashreplace").decode("unicode_escape"), end + 1


def _float_token(tok: str) -> float | None:
    """Parse a decimal float token, or None."""
    if _PLAIN_FLOAT.fullmatch(tok) is not None:
        return float(tok)
    return None


def _bare_value(tok: str) -> Any:
    """Type a bare token: keyword, number or string."""
    c = tok[0]
    if c not in _NUM_START:
        if c == "~" and len(tok) == 1:
            return None
        if tok == "true":
            return True
        if tok == "false":
            return False
        return tok
    if tok.isdigit() and tok.isascii() and len(tok) <= 18:
        return int(tok)
    m = _NUMTOK.fullmatch(tok)
    if m is not None:
        if m.lastindex:
            if len(tok.lstrip("-").lstrip("0")) <= 19:
                i = int(tok)
                if INT64_MIN <= i <= INT64_MAX:
                    return i
        return _finish_float(float(tok), tok)
    f = _float_token(tok)
    if f is None:
        return tok
    return _finish_float(f, tok)


def _finish_float(f: float, tok: str) -> Any:
    if f in (math.inf, -math.inf):
        return tok
    if f.is_integer() and -MAX_EXACT < f < MAX_EXACT:
        return int(f)
    return f


class _Decoder:
    def __init__(self, s: str) -> None:
        self.s = s
        self.n = len(s)
        self.pos = 0
        self.anchors: dict[str, Any] = {}
        self.budget = MAX_EXPAND
        self.cmemo = _CellMemo()

    def _err(self, msg: str, pos: int | None = None) -> NoReturn:
        p = self.pos if pos is None else pos
        raise JaroError(msg, len(self.s[:p].encode("utf-8")))

    def _ws(self, pos: int) -> int:
        return _ws_match(self.s, pos).end()

    def _at_key(self, pos: int) -> bool:
        """Report whether the token at pos is followed by ':' (an object key)."""
        s = self.s
        if pos < self.n and s[pos] == '"':
            m = _QKEY.match(s, pos)
            end = m.end() if m is not None else -1
        else:
            m = _BARE.match(s, pos)
            end = m.end() if m is not None else pos
        return 0 <= end < self.n and s[end] == ":"

    def _name(self) -> str:
        pos = self.pos + 1
        m = _BARE.match(self.s, pos)
        if m is None:
            self._err("missing anchor name", pos)
        self.pos = m.end()
        return m[0]

    def _deref(self) -> Any:
        name = self._name()
        if name not in self.anchors:
            self._err(f"undefined anchor *{name}")
        v, self.budget = deep_copy(self.anchors[name], self.budget)
        if self.budget < 0:
            self._err("reference expansion too large")
        return v

    def _quoted(self) -> str:
        s = self.s
        n = self.n
        start = pos = self.pos + 1
        m = _QPLAIN.match(s, pos)
        end = m.end() if m is not None else pos
        if end < n and s[end] == '"':
            self.pos = end + 1
            return s[start:end]
        if end < n and s[end] == "\\":
            got = _unescape(s, start)
            if got is not None:
                self.pos = got[1]
                return got[0]
        parts = [s[start:end]]
        pos = end
        while pos < n:
            c = s[pos]
            pos += 1
            if c == '"':
                self.pos = pos
                return "".join(parts)
            if pos >= n:
                self._err("bad escape", pos)
            e = s[pos]
            pos += 1
            if e == '"' or e == "\\":
                parts.append(e)
            elif e == "n":
                parts.append("\n")
            elif e == "r":
                parts.append("\r")
            elif e == "t":
                parts.append("\t")
            elif e == "u":
                h = _HEX4.match(s, pos)
                if h is None:
                    self._err("bad \\u escape", pos)
                pos = h.end()
                cp = int(h[0], 16)
                if 0xD800 <= cp < 0xDC00:
                    low = _LOW_SURROGATE.match(s, pos)
                    if low is not None:
                        cp = (
                            0x10000 + ((cp - 0xD800) << 10) + (int(low[1], 16) - 0xDC00)
                        )
                        pos = low.end()
                if 0xD800 <= cp < 0xE000:
                    self._err("unpaired surrogate escape", pos)
                parts.append(chr(cp))
            else:
                self._err(f"bad escape \\{e}", pos)
            m = _QPLAIN.match(s, pos)
            if m is not None:
                parts.append(m[0])
                pos = m.end()
        self._err("unterminated string", pos)

    def _str_token(self, what: str) -> str:
        """Read a quoted or bare token that must be a string."""
        s = self.s
        if s[self.pos] == '"':
            return self._quoted()
        m = _BARE.match(s, self.pos)
        if m is None:
            self._err(f"unexpected {s[self.pos]!r}")
        self.pos = m.end()
        v = _bare_value(m[0])
        if type(v) is not str:
            self._err(what)
        return v

    def _key(self) -> str:
        k = self._str_token("expected key:")
        if self.pos >= self.n or self.s[self.pos] != ":":
            self._err("expected key:")
        self.pos += 1
        return k

    def _spec_list(self, end: str, depth: int) -> list[ColSpec]:
        """Read column keys up to end, which it consumes. A key followed by '{' has sub-columns."""
        if depth > MAX_NEST:
            self._err("table header nested too deep")
        s = self.s
        if end == "|":
            m = _HEADER_MATCH(s, self.pos)
            if m is not None:
                names = m[1].split(" ")
                if (
                    len(set(names)) == len(names)
                    and "true" not in names
                    and "false" not in names
                ):
                    self.pos = m.end()
                    return [ColSpec(k) for k in names]
        specs: list[ColSpec] = []
        seen: set[str] = set()
        while True:
            pos = self._ws(self.pos)
            self.pos = pos
            if pos >= self.n:
                self._err("unterminated table header")
            if s[pos] == end:
                self.pos = pos + 1
                break
            key = self._str_token("table key must be a string")
            if key in seen:
                self._err(f"duplicate column {key!r}", pos)
            seen.add(key)
            pos = self._ws(self.pos)
            sub = None
            if pos < self.n and s[pos] == "{":
                self.pos = pos + 1
                sub = self._spec_list("}", depth + 1)
            specs.append(ColSpec(key, sub))
        if not specs:
            self._err("empty table header")
        return specs

    # One Python frame per nesting level, so the 512 limit holds under the default recursion limit.
    def _value(self, depth: int, bare: bool = False) -> Any:
        if depth > MAX_DEPTH:
            self._err("nesting too deep")
        s = self.s
        n = self.n
        pos = self.pos
        if bare:
            c = "{"
        else:
            if pos + 1 < n and s[pos] == " " and s[pos + 1] not in _WS_LEAD:
                pos = pos + 1
            else:
                pos = _ws_match(s, pos).end()
            if pos >= n:
                self._err("unexpected end", pos)
            c = s[pos]
            if c == '"':
                self.pos = pos
                return self._quoted()
            if c not in "{[":
                if c == "&":
                    self.pos = pos
                    name = self._name()
                    if name in self.anchors:
                        self._err(f"anchor &{name} defined twice")
                    v = self._value(depth + 1)
                    self.anchors[name] = v
                    return v
                if c == "*":
                    self.pos = pos
                    return self._deref()
                m = _BARE.match(s, pos)
                if m is None:
                    self._err(f"unexpected {c!r}", pos)
                self.pos = m.end()
                return _bare_value(m[0])
        table: list[ColSpec] | None = None
        keyed = False
        if c == "[":
            if depth < MAX_DEPTH and s.startswith("[[", pos):
                got = _float_rows(s, pos)
                if got is not None:
                    self.pos = got[1]
                    return got[0]
            pos = _ws_match(s, pos + 1).end()
            if pos < n and s[pos] == "|":
                self.pos = pos + 1
                table = self._spec_list("|", 0)
            else:
                items: list[Any] = []
                while True:
                    if pos + 1 < n and s[pos] == " " and s[pos + 1] not in _WS_LEAD:
                        pos = pos + 1
                    else:
                        pos = _ws_match(s, pos).end()
                    if pos >= n:
                        self._err("unterminated array", pos)
                    if s[pos] == "]":
                        self.pos = pos + 1
                        return items
                    if depth < MAX_DEPTH and s[pos] not in '"{[&*':
                        m = _ITEM_MATCH(s, pos)
                        if m is not None:
                            pos = m.end()
                            tok = m[0]
                            if m.lastindex:
                                f = float(tok)
                                if not f.is_integer():
                                    items.append(f)
                                    continue
                            items.append(_bare_value(tok))
                            continue
                    if depth < MAX_DEPTH and s[pos] == "[":
                        m = _FLAT_FLOATS(s, pos)
                        if m is not None:
                            vals = list(map(float, m[1].split(" ")))
                            if not any(map(_is_integer, vals)):
                                items.append(vals)
                                pos = m.end()
                                continue
                    self.pos = pos
                    items.append(self._value(depth + 1))
                    pos = self.pos
        else:
            if not bare:
                pos = _ws_match(s, pos + 1).end()
                if pos < n and s[pos] == "|":
                    self.pos = pos + 1
                    table = self._spec_list("|", 0)
                    keyed = True
            if table is None:
                obj: dict[str, Any] = {}
                spread: set[str] | None = None
                while True:
                    if depth < MAX_DEPTH:
                        pm = _PAIR_MATCH(s, pos)
                        if pm is not None:
                            key = pm[1]
                            if key is None:
                                key = pm[2]
                            if pm[1] is not None or (key != "true" and key != "false"):
                                pl = pm.lastindex
                                if pl == 3:
                                    v = pm[3]
                                elif pl == 4:
                                    tok = pm[4]
                                    v = float(tok)
                                    if v.is_integer():
                                        v = _bare_value(tok)
                                elif pl == 5:
                                    v = _bare_value(pm[5])
                                else:
                                    v = [] if pm[6] == "[]" else {}
                                if key in obj and (spread is None or key not in spread):
                                    self._err(
                                        f"duplicate key {key!r}",
                                        _ws_match(s, pos).end(),
                                    )
                                obj[key] = v
                                if spread:
                                    spread.discard(key)
                                self.pos = pos = pm.end()
                                continue
                        km = _KEY_MATCH(s, pos)
                        if km is not None:
                            quoted = km.lastindex == 1
                            key = km.group(1 if quoted else 2)
                            if quoted or (key != "true" and key != "false"):
                                vm = _CELL_MATCH(s, km.end())
                                if vm is None:
                                    self.pos = km.end()
                                    v = self._value(depth + 1)
                                else:
                                    li = vm.lastindex
                                    if li == 1:
                                        v = vm[1]
                                    elif li == 2:
                                        tok = vm[2]
                                        v = float(tok)
                                        if v.is_integer():
                                            v = _bare_value(tok)
                                    elif li == 3:
                                        v = _bare_value(vm[3])
                                    else:
                                        v = [] if vm[4] == "[]" else {}
                                    self.pos = vm.end()
                                if key in obj and (spread is None or key not in spread):
                                    self._err(
                                        f"duplicate key {key!r}",
                                        _ws_match(s, pos).end(),
                                    )
                                obj[key] = v
                                if spread:
                                    spread.discard(key)
                                pos = self.pos
                                continue
                    pos = _ws_match(s, pos).end()
                    if pos >= n:
                        if bare:
                            self.pos = pos
                            return obj
                        self._err("unterminated object", pos)
                    ch = s[pos]
                    if ch == "}" and not bare:
                        self.pos = pos + 1
                        return obj
                    self.pos = pos
                    if ch == "*":
                        src = self._deref()
                        if not isinstance(src, dict):
                            self._err("spread needs an object anchor")
                        obj.update(src)
                        if spread is None:
                            spread = set()
                        spread.update(src)
                        pos = self.pos
                        continue
                    m = _bare_match(s, pos)
                    if (
                        m is not None
                        and ch not in _KEY_START_NOT_PLAIN
                        and m.end() < n
                        and s[m.end()] == ":"
                    ):
                        key = m[0]
                        if key == "true" or key == "false":
                            key = self._key()
                        else:
                            self.pos = m.end() + 1
                    else:
                        key = self._key()
                    v = self._value(depth + 1)
                    if key in obj and (spread is None or key not in spread):
                        self._err(f"duplicate key {key!r}", pos)
                    obj[key] = v
                    if spread:
                        spread.discard(key)
                    pos = self.pos
        closer = "}" if keyed else "]"
        nleaf = leaf_count(table)
        keys = flat_keys(table)
        rows: list[Any] = []
        named: dict[str, Any] = {}
        rm = (
            _row_match(nleaf)
            if depth < MAX_DEPTH - 1 and nleaf <= _MAX_ROW_CELLS
            else None
        )
        while True:
            pos = self.pos
            if pos + 1 < n and s[pos] == " " and s[pos + 1] not in _WS_LEAD:
                pos = pos + 1
            else:
                pos = _ws_match(s, pos).end()
            if pos >= n:
                self._err("unterminated object" if keyed else "unterminated array", pos)
            if s[pos] == closer:
                self.pos = pos + 1
                return named if keyed else rows
            name = ""
            if keyed:
                self.pos = pos
                name = self._str_token("expected row name")
                pos = self.pos
                if name in named:
                    self._err(f"duplicate row name {name!r}", pos)
            cells: list[Any] = []
            m = rm(s, pos) if rm is not None else None
            if m is None:
                rm = None
            else:
                cells = list(map(self.cmemo.__getitem__, m.groups()))
                pos = m.end()
            for _ in range(0 if m is not None else nleaf):
                if depth < MAX_DEPTH:
                    m = _CELL_MATCH(s, pos)
                    if m is not None:
                        li = m.lastindex
                        if li == 1:
                            cells.append(m[1])
                        elif li == 2:
                            tok = m[2]
                            f = float(tok)
                            cells.append(_bare_value(tok) if f.is_integer() else f)
                        elif li == 3:
                            tok = m[3]
                            cells.append(ABSENT if tok == "^" else _bare_value(tok))
                        else:
                            cells.append([] if m[4] == "[]" else {})
                        pos = m.end()
                        continue
                if pos + 1 < n and s[pos] == " " and s[pos + 1] not in _WS_LEAD:
                    pos = pos + 1
                else:
                    pos = _ws_match(s, pos).end()
                if pos >= n:
                    self._err("unterminated table", pos)
                ch = s[pos]
                if ch == closer:
                    self._err("table row is incomplete", pos)
                if ch == "^" and (
                    pos + 1 >= n or s[pos + 1] in _DELIMS or s[pos + 1] < " "
                ):
                    pos += 1
                    cells.append(ABSENT)
                    continue
                if depth < MAX_DEPTH and ch not in '"{[&*':
                    m = _ITEM_MATCH(s, pos)
                    if m is not None:
                        pos = m.end()
                        tok = m[0]
                        if m.lastindex:
                            f = float(tok)
                            if not f.is_integer():
                                cells.append(f)
                                continue
                        cells.append(_bare_value(tok))
                        continue
                self.pos = pos
                cells.append(self._value(depth + 1))
                pos = self.pos
            self.pos = pos
            if keys is not None:
                row = {k: c for k, c in zip(keys, cells) if c is not ABSENT}
            else:
                row = build_row(table, iter(cells))
            if keyed:
                named[name] = row
            else:
                rows.append(row)


def loads(text: str | bytes | bytearray | memoryview) -> Any:
    """Decode JARO text. Bytes must be valid UTF-8."""
    if isinstance(text, str):
        try:
            text.encode("utf-8")
        except UnicodeEncodeError as e:
            raise JaroError("input is not valid UTF-8", e.start) from None
        s = text
    else:
        raw = bytes(text)
        try:
            s = raw.decode("utf-8")
        except UnicodeDecodeError as e:
            raise JaroError("input is not valid UTF-8", e.start) from None
    d = _Decoder(s)
    try:
        d.pos = d._ws(0)
        if d.pos >= d.n:
            d._err("empty input")
        if d._at_key(d.pos):
            return d._value(0, True)
        v = d._value(0)
        d.pos = d._ws(d.pos)
        if d.pos != d.n:
            d._err("trailing data")
        return v
    except RecursionError:
        raise JaroError("nesting too deep") from None
