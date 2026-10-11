import { describe, expect, it } from "vitest";
import { decodeUtf8 } from "../src/util.js";

const strict = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });
const reference = (b: Uint8Array): string | null => {
  try {
    return strict.decode(b);
  } catch {
    return null;
  }
};

describe("decodeUtf8", () => {
  it("matches a strict TextDecoder for every length around the ASCII-run cutoff", () => {
    const samples = ["a", "é", "日", "😀", "x\u0000y", "mixed é text 日本 😀 end"];
    for (const unit of samples) {
      for (let n = 0; n <= 70; n++) {
        const bytes = new TextEncoder().encode(unit.repeat(n));
        expect(decodeUtf8(bytes, 0, bytes.length)).toBe(reference(bytes));
        const padded = new Uint8Array(bytes.length + 5).fill(0x7a);
        padded.set(bytes, 3);
        expect(decodeUtf8(padded, 3, 3 + bytes.length)).toBe(reference(bytes));
      }
    }
  });

  it("puts a non-ASCII byte at every position of a short run", () => {
    for (let n = 1; n <= 20; n++) {
      for (let at = 0; at < n; at++) {
        const text = "a".repeat(at) + "é" + "b".repeat(Math.max(0, n - at - 1));
        const bytes = new TextEncoder().encode(text);
        expect(decodeUtf8(bytes, 0, bytes.length)).toBe(text);
      }
    }
  });

  it("rejects invalid UTF-8", () => {
    for (const bad of [[0xff], [0xc3], [0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0xe2, 0x82], [0xed, 0xa0, 0x80]]) {
      const bytes = Uint8Array.from(bad);
      expect(decodeUtf8(bytes, 0, bytes.length)).toBeNull();
    }
  });

  it("agrees with a strict TextDecoder on random short byte strings", () => {
    let seed = 987654321;
    const next = (n: number): number => {
      seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
      return seed % n;
    };
    const interesting = [0x00, 0x41, 0x7f, 0x80, 0xbf, 0xc0, 0xc2, 0xdf, 0xe0, 0xe1, 0xed, 0xef, 0xf0, 0xf4, 0xf5, 0xff, 0x9f, 0xa0, 0x8f, 0x90];
    for (let i = 0; i < 30000; i++) {
      const len = next(14);
      const bytes = new Uint8Array(len);
      for (let j = 0; j < len; j++) bytes[j] = next(3) === 0 ? next(256) : (interesting[next(interesting.length)] as number);
      expect(decodeUtf8(bytes, 0, len), Array.from(bytes).join(",")).toBe(reference(bytes));
    }
  });
});
