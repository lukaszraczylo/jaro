# jaro (TypeScript)

TypeScript codec for JARO text and binary. It has zero runtime dependencies and runs on Node 20+ and in browsers. The format is in `../SPEC.md`. The Go package in `..` is the reference.

## Install

The package is private. Build it from this directory:

```
pnpm install
pnpm build      # writes dist/
```

Node 22.12 or later runs the dev tools (vitest 5). The built library needs Node 20.

## API

```ts
import { stringify, parse, encodeBinary, decodeBinary, JaroError } from "@lukaszraczylo/jaro";

const text = stringify({ users: [{ id: 1, name: "ann" }, { id: 2, name: "bob" }] });
// users:[|id name| 1 ann 2 bob]
parse(text);

const bytes = encodeBinary(value, { dedup: true }); // Uint8Array
decodeBinary(bytes);
```

- `stringify(value, opts?)` and `encodeBinary(value, opts?)` take `{ dedup?: boolean; minDedup?: number }`. `dedup` replaces repeated sub-trees with anchors.
- `parse(text)` and `decodeBinary(bytes)` return `unknown`.
- Every failure throws `JaroError` with an `offset` (`-1` when it has no input position, such as on encode).
- Integers outside the safe range decode to `bigint`. `bigint` input encodes as an int and must fit int64.
- Integral floats below 2^53 are ints. `-0` is `0`. `NaN` and `Infinity` throw.
- Encoders sort keys by code point and replace lone surrogates with U+FFFD.
- Decoders create plain objects. A `__proto__` key becomes an own property.

## Differences from the Go decoder

- Duplicate object keys, table columns and keyed-table row names are errors. A spread or a later member can still override a spread key.
- Binary floats must be finite. A sparse presence bitmap must not set bits beyond the last row.
- `\uXXXX` takes exactly four hex digits. Hex float tokens such as `0x1p3` decode as strings.

## Conformance

```
pnpm exec vitest run
```

`test/conformance.test.ts` reads `../../conformance/vectors.json`. It runs every `text`, `binary`, `badText`, `badTextHex` and `badBinaryHex` case. It round-trips every `trees` entry. It also compares `stringify` and `encodeBinary` byte for byte with the Go output. No case needs an allowlist entry.

## Limits

- Nesting depth 512, as in the Go decoder.
- Reference expansion: 2^24 nodes.
- Binary counts are checked against the remaining input. Column row counts can be at most 8 times the remaining bytes. The node budget is 4M plus 32 per input byte.
- Table headers nest at most 8 levels.
- Not included: schema validation, the Parse/Doc arena, HTTP middleware and zstd.

## Benchmark

```
pnpm bench
```

The script builds, then times every `.json` file in `$JARO_CORPUS` (default `../../corpus`). It compares `JSON`, `yaml`, JARO text and JARO binary. Each cell runs at least 3 times and until about 2 s pass. It prints the minimum and writes `bench/results.json`.
