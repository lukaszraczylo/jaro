import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { decodeBinary, encodeBinary, JaroError, parse, stringify } from "../src/index.js";
import { parseJsonExact, same } from "./helpers.js";

// Documents with many objects of repeating shapes exercise the key-sequence prediction and compiled
// object factories; each element is also decoded alone (a small document, where prediction is off)
// and the two decodes must agree.

const NASTY_STRINGS = [
  "",
  " ",
  "~",
  "^",
  "true",
  "false",
  "123",
  "-1.5e3",
  "1_000",
  "&x",
  "*x",
  "#c",
  "a b",
  "a:b",
  'q"uote',
  "back\\slash",
  "line\nbreak\r\ttab",
  "\u0001ctl",
  "é",
  "日本語",
  "😀 emoji",
  "__proto__",
  "constructor",
  "[x]",
  "{y}",
  "|",
];
const KEY_POOL = ["id", "name", "a", "b", "c", "__proto__", "a b", "true", "1", "é", "x:y", "", "k\"q", "toString", "0"];

function rng(seed: number): (n: number) => number {
  let s = seed >>> 0;
  return (n) => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return (s >>> 8) % n;
  };
}

function scalar(r: (n: number) => number): unknown {
  switch (r(9)) {
    case 0:
      return null;
    case 1:
      return r(2) === 0;
    case 2:
      return r(2000) - 1000;
    case 3:
      return (r(100000) - 50000) / 64;
    case 4:
      return BigInt(r(1000)) * 1_000_000_000_000_000n + 10_000_000_000_000_001n;
    default:
      return NASTY_STRINGS[r(NASTY_STRINGS.length)];
  }
}

function value(r: (n: number) => number, depth: number): unknown {
  const c = r(10);
  if (depth > 2 || c < 6) return scalar(r);
  if (c < 8) return Array.from({ length: r(4) }, () => value(r, depth + 1));
  return object(r, KEY_POOL.slice(0, 2 + r(4)), depth + 1);
}

function object(r: (n: number) => number, keys: string[], depth: number): Record<string, unknown> {
  const o: Record<string, unknown> = {};
  for (const k of keys) Object.defineProperty(o, k, { value: value(r, depth), enumerable: true, writable: true, configurable: true });
  return o;
}

function tree(seed: number): unknown[] {
  const r = rng(seed);
  const schemas = Array.from({ length: 1 + r(3) }, () => {
    const n = 2 + r(5);
    const keys = new Set<string>();
    while (keys.size < n) keys.add(KEY_POOL[r(KEY_POOL.length)] as string);
    return [...keys];
  });
  const count = 70 + r(60);
  const out: unknown[] = [];
  for (let i = 0; i < count; i++) {
    const schema = schemas[r(schemas.length)] as string[];
    // Mostly the exact schema, sometimes a drifted key set, so predictions both hit and diverge.
    const keys = r(8) === 0 ? schema.filter(() => r(4) !== 0).concat(r(2) === 0 ? ["extra" + r(3)] : []) : schema;
    out.push(object(r, keys, 0));
  }
  return out;
}

describe("prediction paths agree with plain decoding", () => {
  it("round-trips 2000 seeded documents and matches per-element decoding", () => {
    for (let seed = 1; seed <= 2000; seed++) {
      const t = tree(seed);
      const text = parse(stringify(t)) as unknown[];
      const bin = decodeBinary(encodeBinary(t)) as unknown[];
      expect(same(text, t), `text seed ${seed}`).toBe(true);
      expect(same(bin, t), `binary seed ${seed}`).toBe(true);
      for (let i = 0; i < t.length; i += 7) {
        expect(same(text[i], parse(stringify(t[i]))), `text elem ${seed}/${i}`).toBe(true);
        expect(same(bin[i], decodeBinary(encodeBinary(t[i]))), `binary elem ${seed}/${i}`).toBe(true);
      }
    }
  }, 120_000);

  it("keeps __proto__ as an own key on every object of a repeated shape", () => {
    const rows = Array.from({ length: 200 }, (_, i) => ({ ["__proto__"]: i, a: 1, b: "x" }));
    const rowsOwn = rows.map((r) => Object.defineProperty({ a: 1, b: "x" }, "__proto__", { value: r["__proto__"], enumerable: true, writable: true, configurable: true }));
    for (const decoded of [parse(stringify(rowsOwn)), decodeBinary(encodeBinary(rowsOwn))] as Record<string, unknown>[][]) {
      expect(Object.getPrototypeOf(decoded[150])).toBe(Object.prototype);
      expect(Object.hasOwn(decoded[150] as object, "__proto__")).toBe(true);
      expect(same(decoded, rowsOwn)).toBe(true);
    }
  });

  it("rejects duplicate keys in a repeated shape in text and binary", () => {
    const body = Array.from({ length: 100 }, (_, i) => `{a:${i} b:${i} c:${i}}`).join(" ");
    expect(() => parse(`[${body} {a:1 b:2 c:3 a:4}]`)).toThrow(JaroError);
    expect(() => parse(`[${body} {a:1 b:2 b:3}]`)).toThrow(JaroError);
  });

  it("never throws anything but JaroError on corrupted repeated documents", () => {
    const r = rng(99);
    for (let seed = 1; seed <= 300; seed++) {
      const t = tree(seed);
      const text = stringify(t);
      const bin = encodeBinary(t);
      for (let m = 0; m < 4; m++) {
        const i = r(text.length);
        const bad = text.slice(0, i) + " {}:\"\\#[]|^*&~".charAt(r(14)) + text.slice(i + 1);
        try {
          parse(bad);
        } catch (e) {
          expect(e).toBeInstanceOf(JaroError);
        }
        const copy = bin.slice();
        copy[2 + r(copy.length - 2)] = r(256);
        try {
          decodeBinary(copy);
        } catch (e) {
          expect(e).toBeInstanceOf(JaroError);
        }
      }
    }
  });

  const dir = join(__dirname, "../../../corpus");
  it.skipIf(!existsSync(dir))("decodes every corpus file identically in text and binary", () => {
    for (const f of readdirSync(dir).filter((x) => x.endsWith(".json"))) {
      const t = parseJsonExact(readFileSync(join(dir, f), "utf8"));
      expect(same(parse(stringify(t)), t), f).toBe(true);
      expect(same(decodeBinary(encodeBinary(t)), t), f).toBe(true);
    }
  });
});
