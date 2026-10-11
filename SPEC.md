# JARO specification v0.1

Status: **frozen for text v1 and binary v1**. Golden vectors in `jaro_test.go` (`TestGoldenVectors`) fail if either format changes. A change needs a new binary version byte or a new media type.

## Data model

null, bool, int64, float64 (finite), string (UTF-8), array, object (string keys, unique). Integral floats below 2^53 are integers. Object key order is not significant; encoders write keys sorted. Keys are unique: a decoder rejects an object, table header, keyed-table row set or binary object that repeats a key. A `*name` spread may override earlier members and a later member may override spread keys.

## Text (`application/jaro`)

Whitespace (space, tab, CR, LF) separates tokens. `#` starts a comment to end of line.

```
document = value | member*            ; a top-level object may omit braces
value    = scalar | object | keyed | array | table | anchor | ref
object   = "{" (member | spread)* "}"
keyed    = "{" "|" column+ "|" (name cell*)* "}"  ; object of row objects; each row has one cell per key

member   = key ":" value
key      = bare | quoted
spread   = "*" name                   ; merges an anchored object, later keys win
array    = "[" value* "]"
table    = "[" "|" column+ "|" cell* "]"   ; cell count must be a multiple of key count
column   = key ("{" column+ "}")?        ; sub-columns: the column holds an object, one cell per sub-column
cell     = value | "^"                 ; "^" alone marks a key the row does not have
anchor   = "&" name value             ; names are unique per document
ref      = "*" name                   ; independent copy of the anchored value
scalar   = "~" | "true" | "false" | number | bare | quoted
```

- `bare` is any run of bytes that contains no delimiter. Delimiters: whitespace, `" { } [ ] | : \ #`. A bare token that parses as a number is a number; `~`, `true`, `false` are reserved; a leading `&` or `*` is reserved; `^` alone is reserved inside tables.
- `number` is decimal: `-`? digits (`.` digits?)? or `-`? `.` digits, then an optional exponent (`e` or `E`, optional sign, digits). Int64 when it parses as one, otherwise float64. Any other token, including `1_000`, `0x10`, `+5`, `1e`, `Inf` and `NaN`, is a string. Encoders write floats in the shortest form that round-trips in the form of Go `strconv.FormatFloat(f, 'g', -1, 64)`: an exponent such as `1.2345675e+06` when the decimal exponent is below -4 or at least 6, and plain digits otherwise, and quote strings that start like a number.
- `quoted` escapes: `\" \\ \n \r \t \uXXXX` with exactly four hex digits. A high surrogate must be followed by `\uXXXX` with a low surrogate and the pair is one code point. A lone surrogate is an error. Encoders quote any string that is empty, reserved, number-like, starts with `&` or `*`, or contains a delimiter.
- Encoders emit a table for an array of 2 or more objects with identical, non-empty key sets, with keys sorted.
- Encoders also emit a table when the objects differ in keys, if at least half of the cells would be present. The header lists the union of keys and each missing key becomes the bare cell `^`. A decoder omits that key from the row. A string equal to `^` is quoted.
- When every row holds an object under a key, and those objects form a table with at least two keys, the column is written as `key{sub1 sub2}` and each row gives one cell per sub-column. Nesting stops at 8 levels. A row whose nested object lacks a sub-key writes `^` for it.
- Encoders write an object as a keyed table `{|k1 k2| name cell cell name cell cell}` when it has at least two entries, every value is an object, the union of keys has at least two keys, and at least half of the cells would be present. Text written with this rule is not readable by a decoder that predates it.
- The binary encoder emits a sparse table instead of an object array when at least 80% of the cells would be present and the keys differ.
- Decoders limit nesting to 512 and reference expansion to 2^24 nodes.

## Binary (`application/jaro-bin`)

Document = `0x0F 0x01` value. Unknown version bytes are rejected.

| Tag | Meaning |
|---|---|
| `0x00` `0x01` `0x02` | null, false, true |
| `0x03` | int: zigzag varint |
| `0x04` | float64, 8 bytes little endian |
| `0x05` | string: uvarint length, bytes |
| `0x06` | string reference: uvarint pool index |
| `0x07` | array: uvarint n, n values |
| `0x08` | object: uvarint n, n pairs of (string, value) |
| `0x09` | table: uvarint columns, key strings, uvarint rows, one column per key |
| `0x0A` | anchor: the next value gets the next anchor index (pre-order) |
| `0x0B` | anchor reference: uvarint index; the anchor must be complete |
| `0x0C` | column array: uvarint n, column |
| `0x0D` | sparse table: uvarint columns, key strings, uvarint rows, then per column a flag byte (0 = every row has the key, 1 = a bitmap of `ceil(rows/8)` bytes follows, bit i set when row i has it) and a column of the present values |
| `0x40`-`0x5F` | string of length tag&31 |
| `0x80`-`0xFF` | integer tag&127 |

String pool: every string of 2 or more bytes (keys and values) is appended to the pool when first read; a later equal string is written as `0x06`.

Column (n values): kind byte, then

| Kind | Payload |
|---|---|
| 0 generic | n tagged values |
| 1 delta | n zigzag varints, each the difference from the previous (start 0, wrapping int64) |
| 2 decimal | scale k (0-6), n zigzag varints m; value = m / 10^k, integer when integral |
| 3 dict | uvarint d, d strings, n uvarint indexes |
| 4 bool | ceil(n/8) bytes, bit i = value i (LSB first) |
| 5 object | uvarint k, k key strings, k columns of n values |

Floats are finite: a decoder rejects NaN and Inf. In a sparse-table bitmap the bits past the last row must be zero. Decoders check counts against remaining input (rows and column values may be at most 8x the remaining bytes, and a node budget of 4M + 32 per input byte caps allocation).

## Schema

A schema is JARO data; see README. Type strings: `any str int num bool null enum(a,b) either(t1,t2)`, arguments `(lo..hi)` or, for `str`, `(/regex/)`; trailing `?` allows null. Object keys ending in `?` are optional; key `...` types extra fields. `[T]` and `[T lo..hi]` are lists. Other scalars are literals.
