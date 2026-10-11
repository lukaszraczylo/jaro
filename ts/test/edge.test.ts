import { describe, expect, it } from "vitest";
import { decodeBinary, encodeBinary, JaroError, parse, stringify } from "../src/index.js";
import { bytesToHex, hexToBytes, same } from "./helpers.js";

const nestedArrays = (n: number): unknown => {
  let v: unknown = [];
  for (let i = 1; i < n; i++) v = [v];
  return v;
};

function offsetOf(fn: () => unknown): number {
  try {
    fn();
  } catch (e) {
    expect(e).toBeInstanceOf(JaroError);
    return (e as JaroError).offset;
  }
  throw new Error("did not throw");
}

describe("numbers", () => {
  it("decodes integers beyond 2^53 as bigint and keeps safe ones as number", () => {
    expect(parse("9223372036854775807")).toBe(9223372036854775807n);
    expect(parse("-9223372036854775808")).toBe(-9223372036854775808n);
    expect(parse("9007199254740992")).toBe(9007199254740992n);
    expect(parse("9007199254740991")).toBe(9007199254740991);
    expect(parse("9223372036854775808")).toBe(9223372036854775808);
  });
  it("encodes bigint as int and rejects int64 overflow", () => {
    expect(parse(stringify(2n ** 62n))).toBe(2n ** 62n);
    expect(decodeBinary(encodeBinary(-(2n ** 63n)))).toBe(-(2n ** 63n));
    expect(bytesToHex(encodeBinary(5n))).toBe(bytesToHex(encodeBinary(5)));
    expect(() => stringify(2n ** 63n)).toThrow(JaroError);
    expect(() => encodeBinary(-(2n ** 63n) - 1n)).toThrow(JaroError);
  });
  it("writes zero as the integer 0 and integral floats as ints", () => {
    expect(stringify(-0)).toBe("0");
    expect(stringify(3.0)).toBe("3");
    expect(Object.is(parse("-0.0"), 0)).toBe(true);
    expect(Object.is(decodeBinary(encodeBinary(-0)), 0)).toBe(true);
    expect(bytesToHex(encodeBinary(-0))).toBe("0f0180");
  });
  it("formats floats like Go's shortest 'g'", () => {
    const cases: [number, string][] = [
      [1.5, "1.5"],
      [123456.5, "123456.5"],
      [1234567.5, "1.2345675e+06"],
      [0.0001234, "0.0001234"],
      [0.00001234, "1.234e-05"],
      [1e21, "1e+21"],
      [2 ** 53, "9.007199254740992e+15"],
      [-1.5e300, "-1.5e+300"],
    ];
    for (const [f, s] of cases) {
      expect(stringify(f)).toBe(s);
      expect(parse(s)).toBe(f);
    }
  });
  it("rejects NaN and Infinity on encode", () => {
    for (const f of [NaN, Infinity, -Infinity]) {
      expect(() => stringify(f)).toThrow(JaroError);
      expect(() => encodeBinary(f)).toThrow(JaroError);
    }
  });
  it("treats malformed number-like tokens as strings", () => {
    expect(parse("[1..5 1e 0x10 - -. 1e999]")).toEqual(["1..5", "1e", "0x10", "-", "-.", "1e999"]);
  });
  it("rejects non-finite floats in binary", () => {
    expect(() => decodeBinary(hexToBytes("0f0104000000000000f07f"))).toThrow(JaroError);
  });
});

describe("objects", () => {
  it("does not pollute prototypes through __proto__ keys", () => {
    const v = parse('"__proto__":{polluted:1} a:2') as Record<string, unknown>;
    expect(({} as Record<string, unknown>)["polluted"]).toBeUndefined();
    expect(Object.getPrototypeOf(v)).toBe(Object.prototype);
    expect(Object.keys(v)).toEqual(["__proto__", "a"]);
    const b = decodeBinary(encodeBinary(v)) as Record<string, unknown>;
    expect(Object.keys(b)).toEqual(["__proto__", "a"]);
    expect(Object.getPrototypeOf(b)).toBe(Object.prototype);
    const rows = parse('[|"__proto__" b| 1 2 3 4]') as Record<string, unknown>[];
    expect(Object.keys(rows[0] as object)).toEqual(["__proto__", "b"]);
    expect(Object.getPrototypeOf(rows[0])).toBe(Object.prototype);
  });
  it("sorts keys on encode, by code point", () => {
    expect(stringify({ b: 1, a: 2, "￿": 3, "\u{10000}": 4 })).toBe('a:2 b:1 "￿":3 "\u{10000}":4'.replaceAll('"', ""));
  });
  it("rejects duplicate keys", () => {
    expect(() => parse("a:1 a:2")).toThrow(JaroError);
    expect(() => parse("{a:1 a:2}")).toThrow(JaroError);
    expect(() => parse("[|a a| 1 2]")).toThrow(JaroError);
    expect(() => decodeBinary(hexToBytes("0f01080243610143610 2".replaceAll(" ", "")))).toThrow(JaroError);
  });
  it("lets spreads and later members override earlier ones", () => {
    expect(parse("b:&b {x:1 y:2} c:{*b y:3}")).toEqual({ b: { x: 1, y: 2 }, c: { x: 1, y: 3 } });
    expect(parse("b:&b {x:1} c:{x:0 *b}")).toEqual({ b: { x: 1 }, c: { x: 1 } });
  });
  it("rejects unsupported input", () => {
    expect(() => stringify(undefined)).toThrow(JaroError);
    expect(() => stringify({ a: () => 1 })).toThrow(JaroError);
    expect(() => encodeBinary(new Date())).toThrow(JaroError);
    expect(() => stringify(Symbol("x"))).toThrow(JaroError);
  });
});

describe("limits", () => {
  it("allows 513 nested arrays and rejects 514 like the Go decoder", () => {
    const ok = "[".repeat(513) + "]".repeat(513);
    const bad = "[".repeat(514) + "]".repeat(514);
    expect(() => parse(ok)).not.toThrow();
    expect(() => parse(bad)).toThrow(JaroError);
    expect(() => stringify(nestedArrays(513))).not.toThrow();
    expect(() => stringify(nestedArrays(514))).toThrow(JaroError);
    expect(() => decodeBinary(encodeBinary(nestedArrays(513)))).not.toThrow();
    expect(() => encodeBinary(nestedArrays(514))).toThrow(JaroError);
    const bin = new Uint8Array(2 + 514 * 2 + 1);
    bin[0] = 0x0f;
    bin[1] = 1;
    for (let i = 0; i < 514; i++) {
      bin[2 + i * 2] = 0x07;
      bin[3 + i * 2] = 1;
    }
    bin[bin.length - 1] = 0x80;
    expect(() => decodeBinary(bin)).toThrow(JaroError);
  });
  it("caps reference expansion at 2^24 nodes", () => {
    let t = "a:&a0 [1 2 3 4 5 6 7 8 9 10]";
    let prev = "a0";
    for (let i = 1; i <= 8; i++) {
      t += ` b${i}:&a${i} [${Array(10).fill(`*${prev}`).join(" ")}]`;
      prev = `a${i}`;
    }
    expect(() => parse(t)).toThrow(/reference expansion/);
    expect(() => parse("a:&a0 [1 2 3] b:[*a0 *a0]")).not.toThrow();
  });
  it("checks binary counts against remaining input", () => {
    expect(() => decodeBinary(hexToBytes("0f0107ffffffff0f"))).toThrow(JaroError);
    expect(() => decodeBinary(hexToBytes("0f01090101610f0000"))).toThrow(JaroError);
  });
  it("limits table headers to 8 nested levels", () => {
    const hdr = (n: number): string => "a{".repeat(n) + "x y" + "}".repeat(n);
    expect(() => parse(`[|${hdr(8)}| 1 2]`)).not.toThrow();
    expect(() => parse(`[|${hdr(9)}| 1 2]`)).toThrow(JaroError);
  });
});

describe("strings and input", () => {
  it("rejects invalid UTF-8 in binary strings", () => {
    expect(() => decodeBinary(hexToBytes("0f0141ff"))).toThrow(JaroError);
    expect(() => decodeBinary(hexToBytes("0f0141eda080"))).toThrow(JaroError);
    expect(decodeBinary(hexToBytes("0f0143efbbbf" + "00".slice(0, 0)))).toBe("﻿"); // BOM is kept
  });
  it("rejects lone surrogates in text input", () => {
    expect(() => parse('a:"\ud800"')).toThrow(JaroError);
    expect(parse('a:"😀"')).toEqual({ a: "😀" });
  });
  it("replaces lone surrogates on encode", () => {
    expect(parse(stringify("a\ud800b"))).toBe("a�b");
    expect(decodeBinary(encodeBinary("a\ud800b"))).toBe("a�b");
  });
  it("handles every escape and quotes what needs quoting", () => {
    const s = 'q"\\\n\r\t\u0001é';
    expect(parse(stringify(s))).toBe(s);
    expect(stringify(["", "~", "^", "true", "1", "-1", ".5", "&x", "*x", "a b", "-", "1x"])).toBe('["" "~" "^" "true" "1" "-1" ".5" "&x" "*x" "a b" - "1x"]');
    expect(parse('"\\u0041\\ud83d\\ude00"')).toBe("A\u{1f600}");
  });
  it("rejects lone surrogate escapes per the spec", () => {
    for (const bad of ['"\\ud800"', '"\\udc00"', '"\\ud83dx"', '"\\ud83d\\u0041"', '"\\ud83d\\ud83d"']) {
      expect(() => parse(bad), bad).toThrow(JaroError);
    }
  });
  it("matches the decimal number grammar on generated tokens", () => {
    const alphabet = "0123456789.-+eE_x";
    const grammar = /^-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/;
    let seed = 12345;
    const next = (n: number): number => {
      seed = (Math.imul(seed, 1103515245) + 12345) >>> 0;
      return seed % n;
    };
    for (let i = 0; i < 20000; i++) {
      let tok = "";
      for (let j = 1 + next(10); j > 0; j--) tok += alphabet[next(alphabet.length)];
      let want: unknown = tok;
      if (grammar.test(tok)) {
        const f = Number(tok);
        if (Number.isFinite(f)) want = f === 0 ? 0 : f;
      }
      expect(parse(`[${tok}]`), tok).toEqual([want]);
    }
  });
  it("treats underscore and non-decimal tokens as strings", () => {
    expect(parse("[1_000 0x10 +5 1e 1_0.5 1.5e1_0 1e5 1. .5]")).toEqual(["1_000", "0x10", "+5", "1e", "1_0.5", "1.5e1_0", 100000, 1, 0.5]);
  });
  it("keeps strings pooled by byte length, including one non-ASCII character", () => {
    const v = ["é", "é", "a", "a"];
    expect(same(decodeBinary(encodeBinary(v)), v)).toBe(true);
  });
  it("reports error offsets", () => {
    expect(offsetOf(() => parse("a:1 b:["))).toBeGreaterThan(0);
    expect(offsetOf(() => decodeBinary(hexToBytes("0f01ff00")))).toBe(3);
    expect(offsetOf(() => decodeBinary(hexToBytes("0f02")))).toBe(1);
    expect(offsetOf(() => decodeBinary(hexToBytes("0f0120")))).toBe(2);
  });
});

describe("tables", () => {
  it("writes sparse text tables with ^ and quotes the string ^", () => {
    const rows = [{ a: 1, b: 2 }, { a: 3 }, { a: 4, b: "^" }];
    expect(stringify(rows)).toBe('[|a b| 1 2 3 ^ 4 "^"]');
    expect(same(parse(stringify(rows)), rows)).toBe(true);
  });
  it("writes nested columns and keyed tables", () => {
    const rows = [{ id: 1, u: { n: "ann", g: { a: 1, b: 2 } } }, { id: 2, u: { n: "bob", g: { a: 3, b: 4 } } }];
    expect(stringify(rows)).toBe("[|id u{g{a b} n}| 1 1 2 ann 2 3 4 bob]");
    expect(stringify({ k: { x: { a: 1, b: 2 }, y: { a: 3, b: 4 } } })).toBe("k:{|a b| x 1 2 y 3 4}");
  });
  it("keeps arrays of fewer than two objects and empty objects out of tables", () => {
    expect(stringify([{ a: 1 }])).toBe("[{a:1}]");
    expect(stringify([{}, {}])).toBe("[{} {}]");
  });
  it("uses sparse binary tables only at 80% presence", () => {
    const dense = [{ a: 1, b: 2 }, { a: 3, b: 4 }, { a: 5, b: 6 }, { a: 7, b: 8 }, { a: 9 }];
    expect(encodeBinary(dense)[2]).toBe(0x0d);
    const thin = [{ a: 1, b: 2 }, { a: 3 }];
    expect(encodeBinary(thin)[2]).toBe(0x07);
  });
  it("rejects stray bits in a sparse presence bitmap", () => {
    // 3 rows, bitmap 0b1000 has a bit beyond row 2
    const hex = "0f010d01416103010800" + "81";
    expect(() => decodeBinary(hexToBytes(hex))).toThrow(/presence bitmap/);
  });
});

describe("dedup", () => {
  it("anchors repeated sub-trees in text and binary", () => {
    const big = { host: "db.internal", port: 5432, user: "app", tags: ["a", "b"] };
    const v = { x: big, y: structuredClone(big), z: [structuredClone(big)] };
    const t = stringify(v, { dedup: true });
    expect(t).toContain("&a0");
    expect(t).toContain("*a0");
    expect(same(parse(t), v)).toBe(true);
    const b = encodeBinary(v, { dedup: true });
    expect(b.length).toBeLessThan(encodeBinary(v).length);
    expect(same(decodeBinary(b), v)).toBe(true);
  });
  it("rejects references to undefined, duplicate or incomplete anchors", () => {
    expect(() => parse("a:*x")).toThrow(JaroError);
    expect(() => parse("a:&x 1 b:&x 2")).toThrow(JaroError);
    expect(() => decodeBinary(hexToBytes("0f010b00"))).toThrow(JaroError);
    expect(() => decodeBinary(hexToBytes("0f010a0b00"))).toThrow(JaroError);
  });
});
