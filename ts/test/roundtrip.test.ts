import { describe, expect, it } from "vitest";
import { decodeBinary, encodeBinary, parse, stringify } from "../src/index.js";
import { same } from "./helpers.js";

/** Small seeded PRNG (mulberry32). */
function rng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const WORDS = ["red", "green", "blue", "amber", "x", "", "^", "~", "true", "false", "1", "-2", ".5", "a b", "a:b", "#h", "&a", "*b", "q\"uote", "back\\slash", "new\nline", "tab\t", "é", "日本語", "😀", "\u0001", "a|b", "{x}", "[y]", "-", "1e5", "007", "__proto__", "constructor"];
const KEYS = ["id", "name", "tag", "on", "loc", "x", "y", "note", "k1", "k2", "k3", "", "1", "a b", "__proto__", "constructor"];

function genScalar(r: () => number): unknown {
  switch (Math.floor(r() * 9)) {
    case 0:
      return null;
    case 1:
      return r() < 0.5;
    case 2:
      return Math.floor(r() * 300) - 100;
    case 3:
      return Math.floor((r() - 0.5) * 2 ** 53);
    case 4:
      return Math.round((r() - 0.5) * 1e7) / 100;
    case 5:
      return (r() - 0.5) * 10 ** Math.floor(r() * 40 - 15);
    case 6:
      return [9223372036854775807n, -9223372036854775808n, 1n << 60n, 9007199254740993n, -(1n << 53n)][Math.floor(r() * 5)];
    case 7:
      return [0, 1e21, 2 ** 53, -(2 ** 53), 0.1, 1e-7, 123456.5, 1234567.5][Math.floor(r() * 8)];
    default:
      return WORDS[Math.floor(r() * WORDS.length)];
  }
}

function setOwn(o: Record<string, unknown>, k: string, v: unknown): void {
  Object.defineProperty(o, k, { value: v, enumerable: true, writable: true, configurable: true });
}

function genRows(r: () => number, depth: number): unknown[] {
  const n = 1 + Math.floor(r() * 12);
  const keys = KEYS.filter(() => r() < 0.5).slice(0, 6);
  if (keys.length === 0) keys.push("id");
  const kinds = keys.map(() => Math.floor(r() * 5));
  const sparse = r() < 0.4;
  const rows: unknown[] = [];
  for (let i = 0; i < n; i++) {
    const row: Record<string, unknown> = {};
    keys.forEach((k, j) => {
      if (sparse && r() < 0.25) return;
      let v: unknown;
      switch (kinds[j]) {
        case 0:
          v = 1000 + i * 3;
          break;
        case 1:
          v = Math.floor(r() * 100000) / 100;
          break;
        case 2:
          v = WORDS[Math.floor(r() * 4)];
          break;
        case 3:
          v = r() < 0.5;
          break;
        default:
          v = depth < 3 ? gen(r, depth + 1) : genScalar(r);
      }
      setOwn(row, k, v);
    });
    rows.push(row);
  }
  return rows;
}

function gen(r: () => number, depth: number): unknown {
  const c = r();
  if (depth > 5 || c < 0.3) return genScalar(r);
  if (c < 0.55) return genRows(r, depth);
  if (c < 0.7) {
    const n = Math.floor(r() * 8);
    return Array.from({ length: n }, () => gen(r, depth + 1));
  }
  if (c < 0.8) {
    const o: Record<string, unknown> = {};
    const n = 2 + Math.floor(r() * 4);
    const rows = genRows(r, depth + 1);
    for (let i = 0; i < n && i < rows.length; i++) setOwn(o, WORDS[i + 4] as string, rows[i]);
    return o;
  }
  if (c < 0.85) {
    const nums = Array.from({ length: 4 + Math.floor(r() * 10) }, () => Math.floor(r() * 50) * 7 + 1000);
    return r() < 0.5 ? nums : nums.map((x) => x / 4);
  }
  if (c < 0.9) {
    const x = genRows(r, depth + 1);
    return { p: x, q: structuredClone(x), r: [structuredClone(x[0])] };
  }
  const o: Record<string, unknown> = {};
  const n = Math.floor(r() * 5);
  for (let i = 0; i < n; i++) setOwn(o, KEYS[Math.floor(r() * KEYS.length)] as string, gen(r, depth + 1));
  return o;
}

describe("seeded random round trips", () => {
  for (let seed = 1; seed <= 400; seed++) {
    it(`seed ${seed}`, () => {
      const v = gen(rng(seed), 0);
      const text = stringify(v);
      expect(same(parse(text), v), `text ${text}`).toBe(true);
      expect(stringify(parse(text))).toBe(text);
      const bin = encodeBinary(v);
      expect(same(decodeBinary(bin), v)).toBe(true);
      expect(same(parse(stringify(v, { dedup: true })), v)).toBe(true);
      expect(same(decodeBinary(encodeBinary(v, { dedup: true })), v)).toBe(true);
    });
  }
});

describe("generator coverage", () => {
  it("exercises tables, sparse tables, keyed tables, anchors and every column kind", () => {
    const text = { table: 0, sparse: 0, keyed: 0, nested: 0, anchor: 0 };
    const tags = new Set<number>();
    for (let seed = 1; seed <= 400; seed++) {
      const v = gen(rng(seed), 0);
      const t = stringify(v);
      if (t.includes("[|")) text.table++;
      if (t.includes(" ^")) text.sparse++;
      if (t.includes("{|")) text.keyed++;
      if (/\|[^|]*\{/.test(t)) text.nested++;
      if (stringify(v, { dedup: true }).includes("&a0")) text.anchor++;
      const b = encodeBinary(v);
      for (const tag of [0x09, 0x0c, 0x0d]) if (b.includes(tag)) tags.add(tag);
    }
    for (const [k, n] of Object.entries(text)) expect(n, k).toBeGreaterThan(0);
    expect(tags.size).toBe(3);
  });
});
