import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { decodeBinary, encodeBinary, parse, stringify } from "../src/index.js";
import { bytesToHex, hexToBytes, parseJsonExact, same } from "./helpers.js";

interface Vectors {
  text: { name: string; jaro: string; json: unknown }[];
  binary: { name: string; hex: string; json: unknown }[];
  trees: { name: string; json: unknown }[];
  badText: string[];
  badTextHex: string[];
  badBinaryHex: string[];
}

// Vector cases where byte parity with the Go encoder is not reached, with the reason.
const TEXT_PARITY_ALLOWLIST: Record<string, string> = {};
const BINARY_PARITY_ALLOWLIST: Record<string, string> = {};

const file = new URL("../../conformance/vectors.json", import.meta.url);
const raw = readFileSync(file, "utf8");
// Vector JSON values hold ints up to 2^63, so decode them exactly.
const vectors = parseJsonExact(raw) as Vectors;

const fatal = new TextDecoder("utf-8", { fatal: true });

describe("decoders", () => {
  for (const c of vectors.text) {
    it(`text/${c.name}`, () => {
      expect(same(parse(c.jaro), c.json)).toBe(true);
    });
  }
  for (const c of vectors.binary) {
    it(`binary/${c.name}`, () => {
      expect(same(decodeBinary(hexToBytes(c.hex)), c.json)).toBe(true);
    });
  }
  vectors.badText.forEach((s, i) => {
    it(`badText/${i} ${JSON.stringify(s)}`, () => {
      expect(() => parse(s)).toThrow();
    });
  });
  for (const h of vectors.badTextHex) {
    it(`badTextHex/${h}`, () => {
      expect(() => parse(fatal.decode(hexToBytes(h)))).toThrow();
    });
  }
  vectors.badBinaryHex.forEach((h, i) => {
    it(`badBinary/${i} ${h}`, () => {
      expect(() => decodeBinary(hexToBytes(h))).toThrow();
    });
  });
});

describe("encoders round-trip trees", () => {
  for (const t of vectors.trees) {
    it(`text/${t.name}`, () => {
      expect(same(parse(stringify(t.json)), t.json)).toBe(true);
    });
    it(`binary/${t.name}`, () => {
      expect(same(decodeBinary(encodeBinary(t.json)), t.json)).toBe(true);
    });
    it(`text-dedup/${t.name}`, () => {
      expect(same(parse(stringify(t.json, { dedup: true })), t.json)).toBe(true);
    });
    it(`binary-dedup/${t.name}`, () => {
      expect(same(decodeBinary(encodeBinary(t.json, { dedup: true })), t.json)).toBe(true);
    });
  }
});

describe("byte parity with Go", () => {
  const trees = new Map(vectors.trees.map((t) => [t.name, t.json]));
  // Hand-written decode-only cases reuse the name "table"; only the first (generated) entry per name is Go output.
  const seen = new Set<string>();
  for (const c of vectors.text) {
    const first = !seen.has(c.name);
    seen.add(c.name);
    const tree = trees.get(c.name);
    if (tree === undefined || !first) continue;
    const reason = TEXT_PARITY_ALLOWLIST[c.name];
    it.skipIf(reason !== undefined)(`text/${c.name}`, () => {
      expect(stringify(tree)).toBe(c.jaro);
    });
  }
  for (const c of vectors.binary) {
    if (c.name.endsWith("-dedup")) continue;
    const tree = trees.get(c.name);
    if (tree === undefined) continue;
    const reason = BINARY_PARITY_ALLOWLIST[c.name];
    it.skipIf(reason !== undefined)(`binary/${c.name}`, () => {
      expect(bytesToHex(encodeBinary(tree))).toBe(c.hex);
    });
  }
});
