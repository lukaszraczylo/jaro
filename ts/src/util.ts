export type JaroValue = null | boolean | number | bigint | string | JaroValue[] | JaroObject;
export interface JaroObject {
  [key: string]: JaroValue;
}

export interface EncodeOptions {
  /** Replace repeated sub-trees with anchors and back-references. */
  dedup?: boolean;
  /** Smallest sub-tree (in text bytes) worth anchoring. */
  minDedup?: number;
}

export class JaroError extends Error {
  readonly offset: number;
  constructor(message: string, offset = -1) {
    super(offset >= 0 ? `jaro: offset ${offset}: ${message}` : `jaro: ${message}`);
    this.name = "JaroError";
    this.offset = offset;
  }
}

export const MAX_DEPTH = 512;
export const MAX_EXPAND = 1 << 24;
export const MAX_NEST = 8;
export const TEXT_PRESENT = 0.5;
export const BIN_PRESENT = 0.8;
export const DEFAULT_MIN_DEDUP = 24;
export const TWO53 = 9007199254740992;
export const MIN_I64 = -(1n << 63n);
export const MAX_I64 = (1n << 63n) - 1n;

export type Obj = Record<string, unknown>;
export type Row = Record<string, unknown>;

export function isObj(v: unknown): v is Obj {
  if (typeof v !== "object" || v === null || Array.isArray(v)) return false;
  const p = Object.getPrototypeOf(v) as unknown;
  return p === Object.prototype || p === null;
}

/** Orders strings by code point, which equals UTF-8 byte order. */
export function cmpKeys(a: string, b: string): number {
  const n = Math.min(a.length, b.length);
  for (let i = 0; i < n; i++) {
    let x = a.charCodeAt(i);
    let y = b.charCodeAt(i);
    if (x !== y) {
      if (x >= 0xd800 && y >= 0xd800) {
        x = x >= 0xe000 ? x - 0x800 : x + 0x2000;
        y = y >= 0xe000 ? y - 0x800 : y + 0x2000;
      }
      return x - y;
    }
  }
  return a.length - b.length;
}

export function sortedKeys(o: Obj): string[] {
  return Object.keys(o).sort(cmpKeys);
}

/** Assigns an own property, safe for "__proto__". */
export function setKey(o: Row, k: string, v: unknown): void {
  if (k === "__proto__") {
    Object.defineProperty(o, k, { value: v, enumerable: true, writable: true, configurable: true });
  } else {
    o[k] = v;
  }
}

export function hasKey(o: Row, k: string): boolean {
  return Object.prototype.hasOwnProperty.call(o, k);
}

const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g;
const ANY_SURROGATE = /[\uD800-\uDFFF]/;

interface WellFormedString {
  isWellFormed?: () => boolean;
}
const NATIVE_WELL_FORMED = typeof (String.prototype as WellFormedString).isWellFormed === "function";

export function hasLoneSurrogate(s: string): boolean {
  if (NATIVE_WELL_FORMED) return !(s as WellFormedString).isWellFormed?.();
  if (!ANY_SURROGATE.test(s)) return false;
  LONE_SURROGATE.lastIndex = 0;
  return LONE_SURROGATE.test(s);
}

/** Replaces each unpaired surrogate with U+FFFD. */
export function wellFormed(s: string): string {
  return ANY_SURROGATE.test(s) ? s.replace(LONE_SURROGATE, "�") : s;
}

export function utf8Length(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n++;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c < 0xdc00 && i + 1 < s.length) {
      n += 4;
      i++;
    } else n += 3;
  }
  return n;
}

export type NumKind = 0 | 1 | 2 | 3; // not a number, int, float, bigint int

/** Classifies a value as the JARO model sees it. Throws on NaN, Infinity or out-of-range bigint. */
export function numKind(v: unknown): NumKind {
  if (typeof v === "number") {
    if (!Number.isFinite(v)) throw new JaroError("NaN/Inf not representable");
    return Number.isInteger(v) && Math.abs(v) < TWO53 ? 1 : 2;
  }
  if (typeof v === "bigint") {
    if (v < MIN_I64 || v > MAX_I64) throw new JaroError("integer out of int64 range");
    return v >= -9007199254740991n && v <= 9007199254740991n ? 1 : 3;
  }
  return 0;
}

/** Normalises a decoded int64 to a number when safe, else a bigint. */
export function intValue(b: bigint): number | bigint {
  return b >= -9007199254740991n && b <= 9007199254740991n ? Number(b) : b;
}

/** Counts nodes for reference expansion and returns an independent copy. */
export function deepCopy(v: unknown, budget: { n: number }): unknown {
  if (Array.isArray(v)) {
    if ((budget.n -= v.length + 1) < 0) throw new JaroError("reference expansion too large");
    const out = new Array<unknown>(v.length);
    for (let i = 0; i < v.length; i++) out[i] = deepCopy(v[i], budget);
    return out;
  }
  if (typeof v === "object" && v !== null) {
    const o = v as Row;
    const keys = Object.keys(o);
    if ((budget.n -= keys.length + 1) < 0) throw new JaroError("reference expansion too large");
    const out: Row = {};
    for (const k of keys) setKey(out, k, deepCopy(o[k], budget));
    return out;
  }
  return v;
}

const encoder = new TextEncoder();

/** Growable output buffer. */
export class ByteWriter {
  buf = new Uint8Array(4096);
  len = 0;

  private grow(extra: number): void {
    let cap = this.buf.length * 2;
    while (cap < this.len + extra) cap *= 2;
    const nb = new Uint8Array(cap);
    nb.set(this.buf.subarray(0, this.len));
    this.buf = nb;
  }

  byte(b: number): void {
    if (this.len === this.buf.length) this.grow(1);
    this.buf[this.len++] = b;
  }

  bytes(b: Uint8Array): void {
    if (this.len + b.length > this.buf.length) this.grow(b.length);
    this.buf.set(b, this.len);
    this.len += b.length;
  }

  uvarint(n: number): void {
    while (n >= 0x80) {
      this.byte((n % 0x80) | 0x80);
      n = Math.floor(n / 0x80);
    }
    this.byte(n);
  }

  /** Writes a zigzag varint of an int64 given as a number or bigint. */
  varint(v: number | bigint): void {
    if (typeof v === "number" && Math.abs(v) < 2 ** 51) {
      this.uvarint(v >= 0 ? v * 2 : -v * 2 - 1);
      return;
    }
    const b = BigInt.asIntN(64, BigInt(v));
    let u = BigInt.asUintN(64, (b << 1n) ^ (b >> 63n));
    while (u >= 0x80n) {
      this.byte(Number(u & 0x7fn) | 0x80);
      u >>= 7n;
    }
    this.byte(Number(u));
  }

  f64(f: number): void {
    if (this.len + 8 > this.buf.length) this.grow(8);
    new DataView(this.buf.buffer, this.buf.byteOffset + this.len, 8).setFloat64(0, f, true);
    this.len += 8;
  }

  /** Writes s as UTF-8; s must be well formed. */
  utf8(s: string): void {
    const max = s.length * 3;
    if (this.len + max > this.buf.length) this.grow(max);
    const b = this.buf;
    let p = this.len;
    for (let i = 0; i < s.length; i++) {
      let c = s.charCodeAt(i);
      if (c < 0x80) b[p++] = c;
      else if (c < 0x800) {
        b[p++] = 0xc0 | (c >> 6);
        b[p++] = 0x80 | (c & 63);
      } else if (c >= 0xd800 && c < 0xdc00) {
        c = 0x10000 + ((c - 0xd800) << 10) + (s.charCodeAt(++i) - 0xdc00);
        b[p++] = 0xf0 | (c >> 18);
        b[p++] = 0x80 | ((c >> 12) & 63);
        b[p++] = 0x80 | ((c >> 6) & 63);
        b[p++] = 0x80 | (c & 63);
      } else {
        b[p++] = 0xe0 | (c >> 12);
        b[p++] = 0x80 | ((c >> 6) & 63);
        b[p++] = 0x80 | (c & 63);
      }
    }
    this.len = p;
  }

  result(): Uint8Array {
    return this.buf.slice(0, this.len);
  }
}

export function utf8Bytes(s: string): Uint8Array {
  return encoder.encode(s);
}

const ASCII_RUN_MAX = 48;
const fatal = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

/** Decodes bytes[start,end) as UTF-8, or returns null when invalid. */
export function decodeUtf8(bytes: Uint8Array, start: number, end: number): string | null {
  if (end - start < ASCII_RUN_MAX) {
    let s = "";
    let i = start;
    let ascii = true;
    for (; i + 8 <= end; i += 8) {
      const a = bytes[i] as number;
      const b = bytes[i + 1] as number;
      const c = bytes[i + 2] as number;
      const d = bytes[i + 3] as number;
      const e = bytes[i + 4] as number;
      const f = bytes[i + 5] as number;
      const g = bytes[i + 6] as number;
      const h = bytes[i + 7] as number;
      if ((a | b | c | d | e | f | g | h) >= 0x80) {
        ascii = false;
        break;
      }
      s += String.fromCharCode(a, b, c, d, e, f, g, h);
    }
    if (ascii) {
      for (; i < end; i++) {
        const c = bytes[i] as number;
        if (c >= 0x80) {
          ascii = false;
          break;
        }
        s += String.fromCharCode(c);
      }
      if (ascii) return s;
    }
  }
  try {
    return fatal.decode(bytes.subarray(start, end));
  } catch {
    return null;
  }
}
