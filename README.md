# JARO

JARO carries the JSON data model (null, bool, int, float, string, array, object) in two encodings:

- **Text**: fewer LLM tokens and fewer bytes than JSON or YAML.
- **Binary**: smallest and fastest to decode, for transfer.

Both encodings round-trip each other. The format is specified in `SPEC.md` (v0.1, frozen). Example files are in `examples/`. The core package has no dependencies.

Site and benchmarks: <https://jaro.raczylo.com>

## Install

```sh
go get github.com/lukaszraczylo/jaro                 # library
go install github.com/lukaszraczylo/jaro/cmd/jaro@latest   # CLI
npm install @lukaszraczylo/jaro                                  # TypeScript
pip install jaro                                     # Python
pip install jaro-native                              # optional Rust binary decoder
```

The `zstdenc` Go module (`github.com/lukaszraczylo/jaro/zstdenc`) adds zstd. Each release tags it as `zstdenc/vX.Y.Z`.

## Text syntax

```
# comment
name:platform replicas:3 beta:true audit:~
owners:[team-a team-b]
base:&b {host:db port:5432}
replica:{*b port:5433}
users:[|id name tags| 1 ann [a b] 2 "bob smith" []]
```

| Element | Rule |
|---|---|
| Null, bool | `~`, `true`, `false` |
| Number | `42`, `-1`, `1.5`, `1e9` (int64 when integral, else float64) |
| String | bare when it holds no delimiter and is not a number or keyword; otherwise `"..."` with `\" \\ \n \r \t \uXXXX` |
| Object | `{k:v k:v}`, or unbraced at the top level; keys sorted on output |
| Array | `[a b c]`; whitespace separates, no commas |
| Table | `[|k1 k2| v v v v ...]`: arrays of 2+ same-key objects. Keys appear once; rows follow by arity, no row separators |
| Anchor, reference | `&name value` defines, `*name` inserts an independent copy |
| Spread | `{*name k:v}` merges an anchored object; later keys win |
| Comment | `#` to end of line |

Delimiters that force quoting: whitespace, `" { } [ ] | : \ #`; a leading `&` or `*`.

The encoder emits anchors on request (`Options{Dedup: true}`): repeated sub-trees of at least 24 bytes become `&a0` / `*a0`. Decoders cap reference expansion at 2^24 nodes, so a reference bomb returns an error.

## Binary syntax

One tag byte per value. Integers 0-127 take one byte, strings under 32 bytes carry their length in the tag, and every repeated string (keys included) becomes a back-reference of 1-2 bytes. Tables are stored column-wise. `0x0A` / `0x0B` are anchor / anchor reference. Counts are checked against remaining input, so truncated or hostile input cannot force a large allocation.

## Validation

A schema is JARO data. `jaro.Compile(def)` then `Validate(v)` returns every violation with a path (`$.users[0].id`), at most 64.

```
id:"int(1..)"
name:"str(1..64)"
email:"str(/^[^@ ]+@[^@ ]+$/)"
role:enum(admin,user)
age?:int(0..150)        # key ending in ? is optional
nick:str?               # type ending in ? accepts ~
tags:[str 0..10]        # list of str, length 0..10
id2:"either(int,str)"
meta:{...:any}          # ... types extra fields; default rejects them
```

Types: `any str int num bool null`, `enum(a,b)`, `either(t1,t2)`. A non-string scalar in a schema is a literal that the value must equal. Anchors and spreads in schema files reuse fragments. Recursive schemas are not supported.

## CLI

```
jaro enc [-b] [-dedup] [file]            JSON to JARO
jaro dec [-b] [file]                     JARO to JSON
jaro check -s schema.jaro [-b|-j] [file] validate; exit 1 on violations
```

## HTTP, gzip and zstd

`jarohttp` (stdlib only) serves and reads JARO over `net/http`:

- Media types: `application/jaro` (text), `application/jaro-bin` (binary). Clients that do not name JARO in `Accept` still get JSON, so adding JARO changes nothing for existing callers.
- `jarohttp.Write(w, r, status, v)` negotiates by `Accept` and sets `Vary: Accept`.
- `jarohttp.Decode(r, max)` reads JARO or JSON bodies and undoes `Content-Encoding`. The cap applies to the wire bytes and to the decompressed bytes, so a compression bomb returns `ErrTooLarge`.
- `jarohttp.Compress(minSize, next)` is response middleware. It picks the best encoding from `Accept-Encoding`, sends identity under `minSize` (default 256 B), skips already-encoded and non-text responses, sets `Vary: Accept-Encoding`, drops `Content-Length` and strong `ETag` when it compresses, and supports `Flush` and `http.ResponseController`.
- `jarohttp.Validated(schema, max, handler)` decodes, validates and answers 400, 413, 415 or 422 (errors as a JARO or JSON list of `{path, msg}`).
- gzip is built in. zstd lives in the separate module `zstdenc` so the core keeps zero dependencies: call `zstdenc.Register()` once and `Compress` then prefers zstd for clients that accept it, while gzip-only clients still get gzip.

```go
zstdenc.Register()
mux.Handle("/users", jarohttp.Compress(0, jarohttp.Validated(schema, 0, create)))
```

## Specification

The text below is [SPEC.md](SPEC.md).

Status: **frozen for text v1 and binary v1**. Golden vectors in `jaro_test.go` (`TestGoldenVectors`) fail if either format changes. A change needs a new binary version byte or a new media type.

### Data model

null, bool, int64, float64 (finite), string (UTF-8), array, object (string keys, unique). Integral floats below 2^53 are integers. Object key order is not significant; encoders write keys sorted. Keys are unique: a decoder rejects an object, table header, keyed-table row set or binary object that repeats a key. A `*name` spread may override earlier members and a later member may override spread keys.

### Text (`application/jaro`)

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

### Binary (`application/jaro-bin`)

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

### Schema

A schema is JARO data; see README. Type strings: `any str int num bool null enum(a,b) either(t1,t2)`, arguments `(lo..hi)` or, for `str`, `(/regex/)`; trailing `?` allows null. Object keys ending in `?` are optional; key `...` types extra fields. `[T]` and `[T lo..hi]` are lists. Other scalars are literals.

## Measured results

`bench/` (Go 1.27, darwin/arm64). Tokens use `o200k_base`. All formats round-trip to identical JSON (YAML excepted from the check).

| Dataset | Format | Bytes | Tokens vs JSON | Encode | Decode |
|---|---|---:|---:|---:|---:|
| users (1000 rows) | JSON | 116,287 | 100% | 437 µs | 1225 µs |
| | YAML | 143,283 | 128% | 4332 µs | 5873 µs |
| | JARO text | 60,641 | 55% | 226 µs | 320 µs |
| | JARO binary | 29,227 | n/a | 175 µs | 166 µs |
| events (2000 rows, nested) | JSON | 198,687 | 100% | 914 µs | 2630 µs |
| | JARO text | 111,374 | 64% | 520 µs | 778 µs |
| | JARO binary | 17,532 | n/a | 263 µs | 391 µs |
| config (40 similar services) | JSON | 9,113 | 100% | 37 µs | 116 µs |
| | JARO text + dedup | 1,297 | 26% | 184 µs | 30 µs |
| | JARO binary + dedup | 964 | n/a | 193 µs | 20 µs |

Compressed sizes (bytes, gzip level 6 / zstd default):

| Dataset | JSON | JARO text | JARO binary |
|---|---:|---:|---:|
| users (1000) | 17,374 / 17,843 | 15,516 / 15,194 | 9,782 / 9,626 |
| events (2000) | 27,577 / 30,939 | 25,593 / 26,800 | 11,625 / 11,768 |
| config | 500 / 436 | 460 / 424 | 381 / 362 |

Compression already removes most of JSON's repeated keys, so JARO text saves only 5-13% on the wire. The transfer gain comes from the binary form, which stores columns together (delta integers, decimal-scaled floats, per-column dictionaries, bit-packed bools, flattened nested objects) so gzip and zstd see long runs of similar bytes: 44% (users), 58% (events) and 24% (config) smaller than gzip JSON, and 46%, 62% and 17% smaller than zstd JSON. Over a live server (`zstdenc/cmd/jaroserve`, 1000 users, curl): JSON 39,784 B, gzip 5,697, zstd 2,990; JARO binary 4,151 B, gzip 1,752, zstd 1,756. On that highly regular synthetic data, zstd on JSON (2,990) beats JARO text with zstd (4,934); binary still wins by 41%.

## Limits

- Floats that are integral and below 2^53 decode as integers. NaN and Inf are rejected.
- Object keys are sorted, so insertion order is not preserved.
- Bare tokens that look like numbers but do not parse (`1..5`) decode as strings; the encoder quotes them.
- Text and binary decoders reject invalid UTF-8; encoders replace invalid bytes with U+FFFD.
- No streaming API yet; encode and decode work on whole buffers.

## Performance work

`experiments-v2.jsonl` records ten metric-gated trials (sum of minimum ns/op over eight encode and decode benchmarks, keep if 1% faster, measured noise 0.05%). Eight were kept and one was reverted. Total time is 21.6% below the starting point, with identical wire output. Run `./measure.sh` to reproduce the metric.

`experiments-allocs.jsonl` records the allocation pass: six trials, four kept (interned strings, shared small integers, decimals parsed without a string). Allocations over the eight benchmarks fell from 64,651 to 35,757 (-45%). A text decode of 1000 users now makes 6,256 allocations against 22,518 for `encoding/json`. The rest are two per row map and one per large number or float, which the `map[string]any` tree requires.

A second pass (`experiments-allocs2.jsonl`) cut allocations from 35,757 to 32,449 (-9%): decoders slice strings from a single copy of the input, and the first occurrence of an interned string is boxed once. Strings decoded this way keep that copy of the input alive for as long as they are referenced. Seven further small ideas (capacity hints, a larger integer table, direct key reads and similar) each saved under 0.5% and were reverted. About 70% of what remains is the two allocations per decoded row map, and about 17% is boxing of floats and large integers.

A speed pass (`experiments-speed.jsonl`) kept two of five trials: a 256-entry lookup table for bare-token delimiters and a local slice in the token scan. Together they cut total time by 5.7% (3,118,179 to 2,941,881 ns). A CPU profile of a 1000-user text decode shows about 13% of samples in decoding. The rest is runtime scheduling and memory release (`kevent`, `madvise`), so bytes allocated now limit speed more than parsing does.

### Build tag `jaro_unsafe`

`go build -tags jaro_unsafe` boxes decoded floats, integers and strings from 256-entry slabs, in the way `go-json` does. It cuts allocations by 37% and total time by about 3%. It uses `unsafe` to build interface values, so the default build leaves it off. Wire output does not change. Run `go test -tags jaro_unsafe .` to test it.

### Real-world files

`bench -data <dir>` runs the codecs over any directory of JSON files, and `measure-real.sh` times them (`JARO_CORPUS=<dir>`). The corpus is `twitter`, `citm_catalog` and `canada` from nativejson-benchmark, `recipe_book` from simdjson, a GitHub issues response and the npm registry document for express.

The first run showed that text tables only helped data with identical keys in every row. Three forms were added: sparse tables (`^` marks a missing key), keyed tables (`{|keys| name cell...}` for an object of rows) and object columns (`user{id name}` inside a header). The binary form gained sparse tables as well. TOON uses similar ideas for nested columns and keyed tables.

Against compact JSON, JARO text now uses 13-44% fewer tokens on five of the six files and the same on `canada` (floats only). After gzip it is 1.8-25% smaller on five files. The binary form is 12-47% smaller after gzip on `canada`, `citm_catalog`, `github_issues` and `twitter`, and larger on `recipe_book` (+11%). `experiments-real.jsonl` and `experiments-real2.jsonl` record the speed trials: total time over the six files fell 6%, from 43.6 ms to 40.9 ms for one pass of all four operations on each file, and allocations fell 22%.

### Parse: a flat document

`Parse(text)` decodes JARO text into a `Doc`, an array of 32-byte nodes read through `Node` accessors (`Get`, `Index`, `Len`, `String`, `Int`, `Float`, `Bool`, `Kind`). It builds no maps and boxes no values. Anchors and spreads are not supported, and `Unmarshal` stays the default. On the six real files it decodes 21-61% faster than `Unmarshal`, with 36-3,100 allocations instead of 735-168,000:

| File | go-json (`any`, JSON) | Unmarshal | Parse |
|---|---|---|---|
| canada | 4.66 ms | 8.93 ms | 6.85 ms |
| citm_catalog | 1.76 ms | 1.95 ms | 1.01 ms |
| github_issues | 0.55 ms | 1.65 ms | 1.04 ms |
| npm_express | 1.40 ms | 2.89 ms | 1.11 ms |
| recipe_book | 0.036 ms | 0.049 ms | 0.022 ms |
| twitter | 0.66 ms | 1.26 ms | 0.58 ms |

The go-json column comes from `bench/decode_test.go` in the same run. `Parse` beats go-json on citm_catalog, npm_express, recipe_book and twitter, and loses on canada and github_issues. Both lose time to Go memory management: the profile shows 64% in `madvise` and `kevent` on the large files. `canada` also pays for 111,000 long floats that go through `strconv.ParseFloat`.

`Doc.Reparse(data)` parses into an existing `Doc` and reuses its buffers, including the copy of the input. In a loop over the six real files it cuts total time from 10.6 ms to 9.8 ms (-7%), with 1 allocation on `canada` instead of 34 and 17% less time on `citm_catalog` and `npm_express`. Nodes and strings taken before a `Reparse` become invalid, because strings are views of the input copy. `canada` stays at 6.7 ms: 55% of its time is `strconv.ParseFloat` on 111,000 floats with 17 significant digits.

A test checks that `Parse` and `Unmarshal` give the same tree on every sample and every real file, and a fuzz test compares them on random input.

### TOON timing

TOON 4.1.1 (`@toon-format/toon`, Node 22) against `JSON.stringify` and `JSON.parse` on the six real files, minimum of 5 rounds. The reference TOON codec is JavaScript, so these numbers are not comparable with the Go figures above. Only the ratio to native JSON in the same process is meaningful.

| File | JSON parse | TOON decode | TOON encode / JSON stringify |
|---|---|---|---|
| canada | 7.4 ms | 178 ms | 5.4x |
| citm_catalog | 1.5 ms | 48 ms | 21x |
| github_issues | 0.61 ms | 18 ms | 9.8x |
| npm_express | 1.6 ms | 36 ms | 19x |
| recipe_book | 0.035 ms | 0.77 ms | 20x |
| twitter | 0.63 ms | 20 ms | 20x |

`JSON.parse` is native code, so the gap overstates what a native TOON codec would show. The size and token results for TOON are in the section above.

### Fastest JARO form against go-json

Text `Unmarshal` loses to go-json on every file, and `Parse` loses on `canada` and `github_issues`. The binary form decodes into the same `any` tree as go-json and wins on those two. The table takes the minimum of 5 runs of 300 iterations per file. The outputs differ: `Parse` returns a `Doc`, the other two return an `any` tree.

| File | go-json | JARO binary | JARO `Parse` | Fastest JARO form |
|---|---|---|---|---|
| canada | 4.67 ms | 2.11 ms | 6.85 ms | binary, 2.2x faster |
| citm_catalog | 1.74 ms | 1.65 ms | 1.01 ms | `Parse`, 1.7x faster |
| github_issues | 0.56 ms | 0.37 ms | 1.04 ms | binary, 1.5x faster |
| npm_express | 1.37 ms | 1.70 ms | 1.11 ms | `Parse`, 1.2x faster |
| recipe_book | 0.035 ms | 0.028 ms | 0.022 ms | `Parse`, 1.6x faster |
| twitter | 0.66 ms | 0.54 ms | 0.58 ms | binary, 1.2x faster |

## License

MIT. See [LICENSE](LICENSE).
