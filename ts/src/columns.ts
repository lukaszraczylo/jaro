import { tableData } from "./tables.js";
import {
  intValue,
  isObj,
  JaroError,
  MAX_DEPTH,
  numKind,
  setKey,
  sortedKeys,
  TWO53,
  type ByteWriter,
  type Row,
} from "./util.js";

export enum Kind {
  Generic = 0,
  Delta = 1,
  Decimal = 2,
  Dict = 3,
  Bool = 4,
  Object = 5,
}

export const MAX_DECIMALS = 6;
export const MIN_COLUMN = 4;
const POW10 = [1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15];
const SMALL = 2 ** 51;

export type ColPlan =
  | { kind: Kind.Generic | Kind.Delta | Kind.Bool }
  | { kind: Kind.Decimal; k: number }
  | { kind: Kind.Dict; dict: string[]; idx: Map<string, number> }
  | { kind: Kind.Object; keys: string[]; cols: unknown[][] };

const GENERIC: ColPlan = { kind: Kind.Generic };

/** What a column encoder needs from the value encoder. */
export interface ColEncHost {
  w: ByteWriter;
  value(v: unknown, depth: number): void;
  str(s: string): void;
}

/** What a column decoder needs from the binary reader. */
export interface ColDecHost {
  s: Uint8Array;
  pos: number;
  value(depth: number): unknown;
  key(): string;
  count(): number;
  uvarint(): number;
  varint(): number | bigint;
  charge(n: number): void;
}

function uvarLen(u: number): number {
  let n = 1;
  while (u >= 0x80) {
    u = Math.floor(u / 0x80);
    n++;
  }
  return n;
}

const zigzagLen = (v: number): number => uvarLen(v >= 0 ? v * 2 : -v * 2 - 1);

function zigzagLenBig(v: bigint): number {
  const b = BigInt.asIntN(64, v);
  let u = BigInt.asUintN(64, (b << 1n) ^ (b >> 63n));
  let n = 1;
  while (u >= 0x80n) {
    u >>= 7n;
    n++;
  }
  return n;
}

/** Rounds half away from zero. */
function goRound(x: number): number {
  return x < 0 ? -Math.round(-x) : Math.round(x);
}

/** Fewest decimal places (<= 6) that represent f exactly as m/10^k, or -1. */
function decimals(f: number): number {
  for (let k = 0; k <= MAX_DECIMALS; k++) {
    const m = goRound(f * (POW10[k] as number));
    if (Math.abs(m) < TWO53 && m / (POW10[k] as number) === f) return k;
  }
  return -1;
}

/** True when sums and differences of vals are exact as doubles. */
function isSmall(ints: (number | bigint)[]): boolean {
  for (const v of ints) if (typeof v === "bigint" || Math.abs(v) >= SMALL) return false;
  return true;
}

function classifyStrings(vals: unknown[]): ColPlan {
  const idx = new Map<string, number>();
  const dict: string[] = [];
  for (const v of vals) {
    if (typeof v !== "string") return GENERIC;
    if (!idx.has(v)) {
      idx.set(v, dict.length);
      dict.push(v);
      if (dict.length * 2 > vals.length) return GENERIC;
    }
  }
  if (vals.length >= MIN_COLUMN && dict.length * 2 <= vals.length) return { kind: Kind.Dict, dict, idx };
  return GENERIC;
}

function classifyNumbers(vals: unknown[]): ColPlan {
  const nums: (number | bigint)[] = [];
  let allInt = true;
  let k = 0;
  for (const v of vals) {
    const kd = typeof v === "number" || typeof v === "bigint" ? numKind(v) : 0;
    if (kd === 0) return GENERIC;
    nums.push(v as number | bigint);
    if (kd !== 2) continue;
    allInt = false;
    const kk = decimals(v as number);
    if (kk < 0) return GENERIC;
    k = Math.max(k, kk);
  }
  if (allInt) {
    let plain = 0;
    let delta = 0;
    if (isSmall(nums)) {
      let prev = 0;
      for (const v of nums as number[]) {
        plain += zigzagLen(v);
        delta += zigzagLen(v - prev);
        prev = v;
      }
    } else {
      let prev = 0n;
      for (const v of nums) {
        const b = BigInt(v);
        plain += zigzagLenBig(b);
        delta += zigzagLenBig(b - prev);
        prev = b;
      }
    }
    return delta < plain ? { kind: Kind.Delta } : { kind: Kind.Decimal, k: 0 };
  }
  const scale = POW10[k] as number;
  for (const v of nums) {
    if (numKind(v) !== 2) {
      if (Math.abs(Number(v)) * scale >= TWO53) return GENERIC;
      continue;
    }
    // The shared scale can push a value past 2^53 or off its exact decimal.
    const m = goRound((v as number) * scale);
    if (Math.abs(m) >= TWO53 || m / scale !== v) return GENERIC;
  }
  return { kind: Kind.Decimal, k };
}

/** Picks the cheapest lossless column kind for vals. */
export function classify(vals: unknown[]): ColPlan {
  const n = vals.length;
  if (n === 0) return GENERIC;
  const first = vals[0];
  if (typeof first === "boolean") {
    for (const v of vals) if (typeof v !== "boolean") return GENERIC;
    return { kind: Kind.Bool };
  }
  if (typeof first === "string") return classifyStrings(vals);
  if (isObj(first)) {
    let td = tableData(vals);
    if (td === null && n === 1) {
      const keys = sortedKeys(first);
      if (keys.length > 0) td = { keys, cols: keys.map((k) => [first[k]]) };
    }
    return td === null ? GENERIC : { kind: Kind.Object, keys: td.keys, cols: td.cols };
  }
  return classifyNumbers(vals);
}

export function encodeColumn(h: ColEncHost, vals: unknown[], depth: number, plan?: ColPlan): void {
  if (depth > MAX_DEPTH) throw new JaroError("nesting too deep");
  const p = plan ?? classify(vals);
  const w = h.w;
  w.byte(p.kind);
  switch (p.kind) {
    case Kind.Generic:
      for (const v of vals) h.value(v, depth + 1);
      break;
    case Kind.Delta: {
      if (isSmall(vals as number[])) {
        let prev = 0;
        for (const v of vals as number[]) {
          w.varint(v - prev);
          prev = v;
        }
      } else {
        let prev = 0n;
        for (const v of vals as (number | bigint)[]) {
          const b = BigInt(v);
          w.varint(BigInt.asIntN(64, b - prev));
          prev = b;
        }
      }
      break;
    }
    case Kind.Decimal:
      w.byte(p.k & 7);
      for (const v of vals as (number | bigint)[]) {
        w.varint(p.k === 0 ? v : goRound(Number(v) * (POW10[p.k] as number)));
      }
      break;
    case Kind.Dict:
      w.uvarint(p.dict.length);
      for (const s of p.dict) h.str(s);
      for (const v of vals as string[]) w.uvarint(p.idx.get(v) as number);
      break;
    case Kind.Bool: {
      const bits = new Uint8Array((vals.length + 7) >> 3);
      for (let i = 0; i < vals.length; i++) if (vals[i] === true) bits[i >> 3] = (bits[i >> 3] as number) | (1 << (i & 7));
      w.bytes(bits);
      break;
    }
    case Kind.Object:
      w.uvarint(p.keys.length);
      for (const k of p.keys) h.str(k);
      for (const col of p.cols) encodeColumn(h, col, depth + 1);
      break;
  }
}

function decimalValue(m: number | bigint, k: number): number | bigint {
  if (k === 0) return m;
  const f = Number(m) / (POW10[k] as number);
  return f === 0 ? 0 : f;
}

export function decodeColumn(h: ColDecHost, n: number, depth: number): unknown[] {
  if (depth > MAX_DEPTH) throw new JaroError("nesting too deep", h.pos);
  h.charge(n + 1);
  if (h.pos >= h.s.length) throw new JaroError("truncated input", h.pos);
  const kind = h.s[h.pos++] as number;
  const out = new Array<unknown>(n);
  switch (kind) {
    case Kind.Generic:
      for (let i = 0; i < n; i++) out[i] = h.value(depth + 1);
      break;
    case Kind.Delta: {
      let prev: number | bigint = 0;
      for (let i = 0; i < n; i++) {
        const v = h.varint();
        if (typeof prev === "number" && typeof v === "number" && Math.abs(prev + v) <= Number.MAX_SAFE_INTEGER) {
          prev += v;
        } else {
          prev = intValue(BigInt.asIntN(64, BigInt(prev) + BigInt(v)));
        }
        out[i] = prev;
      }
      break;
    }
    case Kind.Decimal: {
      const k = h.s[h.pos];
      if (k === undefined || k > MAX_DECIMALS) throw new JaroError("bad decimal scale", h.pos);
      h.pos++;
      for (let i = 0; i < n; i++) out[i] = decimalValue(h.varint(), k);
      break;
    }
    case Kind.Dict: {
      const nd = h.count();
      const dict = new Array<string>(nd);
      for (let i = 0; i < nd; i++) dict[i] = h.key();
      for (let i = 0; i < n; i++) {
        const idx = h.uvarint();
        if (idx >= nd) throw new JaroError("bad dictionary index", h.pos);
        out[i] = dict[idx];
      }
      break;
    }
    case Kind.Bool: {
      const nb = (n + 7) >> 3;
      if (h.s.length - h.pos < nb) throw new JaroError("truncated input", h.pos);
      for (let i = 0; i < n; i++) out[i] = (((h.s[h.pos + (i >> 3)] as number) >> (i & 7)) & 1) === 1;
      h.pos += nb;
      break;
    }
    case Kind.Object:
      return decodeObjectColumn(h, n, depth);
    default:
      throw new JaroError("bad column kind", h.pos - 1);
  }
  return out;
}

function decodeObjectColumn(h: ColDecHost, n: number, depth: number): unknown[] {
  const nk = h.count();
  if (nk === 0) throw new JaroError("empty object column", h.pos);
  const keys = new Array<string>(nk);
  const seen = new Set<string>();
  for (let i = 0; i < nk; i++) {
    const k = h.key();
    if (seen.has(k)) throw new JaroError(`duplicate key ${JSON.stringify(k)}`, h.pos);
    seen.add(k);
    keys[i] = k;
  }
  const rows: Row[] = new Array<Row>(n);
  for (let i = 0; i < n; i++) rows[i] = {};
  for (const k of keys) {
    const col = decodeColumn(h, n, depth + 1);
    for (let i = 0; i < n; i++) setKey(rows[i] as Row, k, col[i]);
  }
  return rows;
}

