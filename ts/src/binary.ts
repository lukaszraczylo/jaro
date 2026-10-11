import { classify, decodeColumn, encodeColumn, Kind, MIN_COLUMN, type ColDecHost, type ColEncHost } from "./columns.js";
import { Dedup } from "./dedup.js";
import { Candidate, ShapeIndex } from "./shapes.js";
import { sparseKeys, tableData } from "./tables.js";
import { plainText } from "./text.js";
import {
  BIN_PRESENT,
  ByteWriter,
  decodeUtf8,
  DEFAULT_MIN_DEDUP,
  deepCopy,
  hasKey,
  hasLoneSurrogate,
  intValue,
  isObj,
  JaroError,
  MAX_DEPTH,
  MAX_EXPAND,
  numKind,
  setKey,
  sortedKeys,
  utf8Length,
  wellFormed,
  type EncodeOptions,
  type Obj,
  type Row,
} from "./util.js";

const T_NULL = 0x00;
const T_FALSE = 0x01;
const T_TRUE = 0x02;
const T_INT = 0x03;
const T_FLOAT = 0x04;
const T_STR = 0x05;
const T_REF = 0x06;
const T_ARR = 0x07;
const T_OBJ = 0x08;
const T_TABLE = 0x09;
const T_ANCHOR = 0x0a;
const T_ANCHOR_REF = 0x0b;
const T_COL = 0x0c;
const T_SPARSE = 0x0d;
const T_SHORT_STR = 0x40;
const T_SMALL_INT = 0x80;
const MAGIC = 0x0f;
const VERSION = 0x01;
const MIN_POOLED = 2;
const MAX_I32 = 0x7fffffff;
const NODES_BASE = 1 << 22;
const NODES_PER_BYTE = 32;

class BinEnc implements ColEncHost {
  readonly w = new ByteWriter();
  private readonly pool = new Map<string, number>();

  constructor(private readonly dd: Dedup | null) {}

  str(s: string): void {
    let pooled = s.length >= MIN_POOLED || utf8Length(s) >= MIN_POOLED;
    if (pooled && this.ref(s)) return;
    if (hasLoneSurrogate(s)) {
      s = wellFormed(s);
      pooled = s.length >= MIN_POOLED || utf8Length(s) >= MIN_POOLED;
      if (pooled && this.ref(s)) return;
    }
    if (pooled) this.pool.set(s, this.pool.size);
    const n = utf8Length(s);
    if (n < 32) this.w.byte(T_SHORT_STR | n);
    else {
      this.w.byte(T_STR);
      this.w.uvarint(n);
    }
    this.w.utf8(s);
  }

  private ref(s: string): boolean {
    const idx = this.pool.get(s);
    if (idx === undefined) return false;
    this.w.byte(T_REF);
    this.w.uvarint(idx);
    return true;
  }

  value(v: unknown, depth: number): void {
    if (depth > MAX_DEPTH) throw new JaroError("nesting too deep");
    const w = this.w;
    if (this.dd !== null) {
      const r = this.dd.ref(v);
      if (r !== null) {
        if (r.use) {
          w.byte(T_ANCHOR_REF);
          w.uvarint(r.idx);
          return;
        }
        w.byte(T_ANCHOR);
      }
    }
    if (v === null) w.byte(T_NULL);
    else if (typeof v === "boolean") w.byte(v ? T_TRUE : T_FALSE);
    else if (typeof v === "string") this.str(v);
    else if (Array.isArray(v)) this.array(v, depth);
    else if (isObj(v)) {
      const keys = sortedKeys(v);
      w.byte(T_OBJ);
      w.uvarint(keys.length);
      for (const k of keys) {
        this.str(k);
        this.value(v[k], depth + 1);
      }
    } else {
      const kd = typeof v === "number" || typeof v === "bigint" ? numKind(v) : 0;
      if (kd === 0) throw new JaroError(`unsupported type ${v === undefined ? "undefined" : typeof v}`);
      if (kd === 2) {
        w.byte(T_FLOAT);
        w.f64(v as number);
      } else {
        const n = v as number | bigint;
        if (n >= 0 && n < 128) w.byte(T_SMALL_INT | Number(n));
        else {
          w.byte(T_INT);
          w.varint(n);
        }
      }
    }
  }

  private array(x: unknown[], depth: number): void {
    const w = this.w;
    const td = tableData(x);
    if (td !== null) {
      w.byte(T_TABLE);
      w.uvarint(td.keys.length);
      for (const k of td.keys) this.str(k);
      w.uvarint(x.length);
      for (const col of td.cols) encodeColumn(this, col, depth + 1);
      return;
    }
    const sk = sparseKeys(x, BIN_PRESENT);
    if (sk !== null) return this.sparse(x as Obj[], sk, depth);
    if (x.length >= MIN_COLUMN) {
      const plan = classify(x);
      if (plan.kind !== Kind.Generic) {
        w.byte(T_COL);
        w.uvarint(x.length);
        return encodeColumn(this, x, depth + 1, plan);
      }
    }
    w.byte(T_ARR);
    w.uvarint(x.length);
    for (const el of x) this.value(el, depth + 1);
  }

  private sparse(rows: Obj[], keys: string[], depth: number): void {
    const w = this.w;
    w.byte(T_SPARSE);
    w.uvarint(keys.length);
    for (const k of keys) this.str(k);
    w.uvarint(rows.length);
    for (const k of keys) {
      const vals: unknown[] = [];
      const bits = new Uint8Array((rows.length + 7) >> 3);
      for (let i = 0; i < rows.length; i++) {
        const r = rows[i] as Obj;
        if (hasKey(r, k)) {
          vals.push(r[k]);
          bits[i >> 3] = (bits[i >> 3] as number) | (1 << (i & 7));
        }
      }
      if (vals.length === rows.length) w.byte(0);
      else {
        w.byte(1);
        w.bytes(bits);
      }
      encodeColumn(this, vals, depth + 1);
    }
  }
}

/** Encodes a value as JARO binary. Throws JaroError for unsupported input. */
export function encodeBinary(value: unknown, opts: EncodeOptions = {}): Uint8Array {
  const dd = opts.dedup === true ? new Dedup(value, opts.minDedup ?? DEFAULT_MIN_DEDUP, true, plainText) : null;
  const e = new BinEnc(dd);
  e.w.byte(MAGIC);
  e.w.byte(VERSION);
  e.value(value, 0);
  return e.w.result();
}

const popcount = (b: number): number => {
  let n = 0;
  for (; b !== 0; b &= b - 1) n++;
  return n;
};

class BinDec implements ColDecHost {
  pos = 2;
  private nodes: number;
  private readonly pool: string[] = [];
  private readonly anchors: unknown[] = [];
  private readonly filled: boolean[] = [];
  private readonly budget = { n: MAX_EXPAND };
  private readonly dv: DataView;
  private keyRef = -1;
  private readonly vals: unknown[] = [];
  private readonly shapes = new ShapeIndex();

  constructor(readonly s: Uint8Array) {
    this.dv = new DataView(s.buffer, s.byteOffset, s.byteLength);
    this.nodes = NODES_BASE + NODES_PER_BYTE * s.length;
  }

  private err(msg: string, at = this.pos): JaroError {
    return new JaroError(msg, at);
  }

  private short(): JaroError {
    return this.err("truncated input");
  }

  uvarint(): number {
    let n = 0;
    let mul = 1;
    for (let i = 0; i < 10; i++) {
      if (this.pos >= this.s.length) throw this.short();
      const b = this.s[this.pos++] as number;
      if (i === 9 && b > 1) throw this.err("varint overflow");
      n += (b & 0x7f) * mul;
      if (b < 0x80) return n;
      mul *= 128;
    }
    throw this.err("varint overflow");
  }

  /** Reads a zigzag varint exactly, as a number when safe else a bigint. */
  varint(): number | bigint {
    const start = this.pos;
    let end = start;
    for (;;) {
      if (end >= this.s.length || end - start >= 10) throw this.short();
      if (((this.s[end++] as number) & 0x80) === 0) break;
    }
    if (end - start === 10 && (this.s[end - 1] as number) > 1) throw this.err("varint overflow");
    this.pos = end;
    if (end - start <= 7) {
      let u = 0;
      let mul = 1;
      for (let i = start; i < end; i++, mul *= 128) u += ((this.s[i] as number) & 0x7f) * mul;
      return u % 2 === 0 ? u / 2 : -(u + 1) / 2;
    }
    let u = 0n;
    for (let i = end - 1; i >= start; i--) u = (u << 7n) | BigInt((this.s[i] as number) & 0x7f);
    return intValue((u >> 1n) ^ -(u & 1n));
  }

  /** Reads an element count bounded by the bytes left. */
  count(): number {
    const n = this.uvarint();
    if (n > MAX_I32 || n > this.s.length - this.pos) throw this.short();
    return n;
  }

  /** Reads a row count for column data; bool columns take one bit per value. */
  private rowCount(): number {
    const n = this.uvarint();
    if (n > MAX_I32 || n > (this.s.length - this.pos) * 8) throw this.short();
    return n;
  }

  charge(n: number): void {
    if ((this.nodes -= n) < 0) throw this.err("decoded size too large");
  }

  private strBytes(n: number): string {
    if (n > this.s.length - this.pos) throw this.short();
    const s = decodeUtf8(this.s, this.pos, this.pos + n);
    if (s === null) throw this.err("string is not valid UTF-8");
    this.pos += n;
    if (n >= MIN_POOLED) this.pool.push(s);
    return s;
  }

  key(): string {
    this.keyRef = -1;
    if (this.pos < this.s.length) {
      const t = this.s[this.pos] as number;
      if (t === T_REF) {
        this.pos++;
        const idx = this.uvarint();
        if (idx >= this.pool.length) throw this.err("bad string reference");
        this.keyRef = idx;
        return this.pool[idx] as string;
      }
      if (t >= T_SHORT_STR && t < T_SHORT_STR + 32) {
        this.pos++;
        return this.strBytes(t & 31);
      }
    }
    const v = this.value(MAX_DEPTH);
    if (typeof v !== "string") throw this.err("key must be a string");
    return v;
  }

  private header(): string[] {
    const nc = this.count();
    if (nc === 0) throw this.err("empty table header");
    const cols: string[] = [];
    const seen = new Set<string>();
    for (let i = 0; i < nc; i++) {
      const k = this.key();
      if (seen.has(k)) throw this.err(`duplicate column ${JSON.stringify(k)}`);
      seen.add(k);
      cols.push(k);
    }
    return cols;
  }

  value(depth: number): unknown {
    if (depth > MAX_DEPTH) throw this.err("nesting too deep");
    if (this.pos >= this.s.length) throw this.short();
    const t = this.s[this.pos++] as number;
    if (t >= T_SMALL_INT) return t & 0x7f;
    if (t >= T_SHORT_STR && t < T_SHORT_STR + 32) return this.strBytes(t & 31);
    switch (t) {
      case T_NULL:
        return null;
      case T_FALSE:
        return false;
      case T_TRUE:
        return true;
      case T_INT:
        return this.varint();
      case T_FLOAT: {
        if (this.s.length - this.pos < 8) throw this.short();
        const f = this.dv.getFloat64(this.pos, true);
        if (!Number.isFinite(f)) throw this.err("float is not finite");
        this.pos += 8;
        return f === 0 ? 0 : f;
      }
      case T_STR:
        return this.strBytes(this.count());
      case T_REF: {
        const idx = this.uvarint();
        if (idx >= this.pool.length) throw this.err("bad string reference");
        return this.pool[idx];
      }
      case T_ARR: {
        const n = this.count();
        // A double-kind literal avoids a SMI-to-double elements transition for float pairs.
        const out: unknown[] = n === 2 && this.s[this.pos] === T_FLOAT ? [0.5, 0.5] : new Array<unknown>(n);
        for (let i = 0; i < n; i++) out[i] = this.value(depth + 1);
        return out;
      }
      case T_OBJ: {
        const n = this.count();
        if (n === 0) return {};
        return this.object(n, depth);
      }
      case T_TABLE:
        return this.table(depth);
      case T_SPARSE:
        return this.sparse(depth);
      case T_COL:
        return decodeColumn(this, this.rowCount(), depth + 1);
      case T_ANCHOR: {
        const idx = this.anchors.length;
        this.anchors.push(null);
        this.filled.push(false);
        const v = this.value(depth + 1);
        this.anchors[idx] = v;
        this.filled[idx] = true;
        return v;
      }
      case T_ANCHOR_REF: {
        const idx = this.uvarint();
        if (idx >= this.anchors.length || this.filled[idx] !== true) throw this.err("bad anchor reference");
        return deepCopy(this.anchors[idx], this.budget);
      }
    }
    throw this.err(`bad tag 0x${t.toString(16).padStart(2, "0")}`, this.pos - 1);
  }

  private object(n: number, depth: number): Row {
    let k = this.key();
    const ref0 = this.keyRef;
    const slot = ref0 >= 0 ? ShapeIndex.slot(ref0, n) : -1;
    const cand = slot >= 0 ? this.shapes.get(slot) : undefined;
    const vals = this.vals;
    const base = vals.length;
    if (cand) {
      const refs = cand.refs;
      vals.push(this.value(depth + 1));
      let i = 1;
      for (; i < n; i++) {
        k = this.key();
        if (this.keyRef !== refs[i]) break;
        vals.push(this.value(depth + 1));
      }
      if (i === n) {
        const obj = cand.make(vals, base);
        vals.length = base;
        return obj;
      }
      // The key sequence diverged: finish key by key, with duplicate checks from here on.
      const m: Row = {};
      for (let j = 0; j < i; j++) setKey(m, cand.keys[j] as string, vals[base + j]);
      vals.length = base;
      return this.objectRest(m, k, i, n, depth);
    }
    const track = cand === null ? [ref0] : null;
    const keys = cand === null ? [k] : null;
    const m: Row = {};
    if (slot >= 0 && cand === undefined) this.shapes.markSeen(slot);
    setKey(m, k, this.value(depth + 1));
    for (let i = 1; i < n; i++) {
      k = this.key();
      if (hasKey(m, k)) throw this.err(`duplicate key ${JSON.stringify(k)}`);
      if (track !== null) {
        track.push(this.keyRef);
        (keys as string[]).push(k);
      }
      setKey(m, k, this.value(depth + 1));
    }
    if (track !== null && !track.includes(-1)) this.shapes.add(slot, new Candidate(track, keys as string[]));
    return m;
  }

  /** Continues an object whose key i was just read and not yet checked or stored. */
  private objectRest(m: Row, k: string, i: number, n: number, depth: number): Row {
    for (;;) {
      if (hasKey(m, k)) throw this.err(`duplicate key ${JSON.stringify(k)}`);
      setKey(m, k, this.value(depth + 1));
      if (++i >= n) return m;
      k = this.key();
    }
  }

  private table(depth: number): unknown[] {
    const cols = this.header();
    const nr = this.rowCount();
    const rows: Row[] = new Array<Row>(nr);
    for (let i = 0; i < nr; i++) rows[i] = {};
    for (const k of cols) {
      const col = decodeColumn(this, nr, depth + 1);
      for (let i = 0; i < nr; i++) setKey(rows[i] as Row, k, col[i]);
    }
    return rows;
  }

  private sparse(depth: number): unknown[] {
    const cols = this.header();
    const nr = this.rowCount();
    this.charge(nr * 2);
    const rows: Row[] = new Array<Row>(nr);
    for (let i = 0; i < nr; i++) rows[i] = {};
    for (const k of cols) {
      if (this.pos >= this.s.length) throw this.short();
      const flag = this.s[this.pos++] as number;
      let bits: Uint8Array | null = null;
      let n = nr;
      if (flag === 1) {
        const nb = (nr + 7) >> 3;
        if (this.s.length - this.pos < nb) throw this.short();
        bits = this.s.subarray(this.pos, this.pos + nb);
        this.pos += nb;
        n = 0;
        for (const by of bits) n += popcount(by);
        const pad = nr & 7;
        if (n > nr || (pad !== 0 && ((bits[nb - 1] as number) >> pad) !== 0)) throw this.err("bad presence bitmap");
      } else if (flag !== 0) throw this.err("bad presence flag", this.pos - 1);
      const col = decodeColumn(this, n, depth + 1);
      let j = 0;
      for (let i = 0; i < nr; i++) {
        if (bits !== null && (((bits[i >> 3] as number) >> (i & 7)) & 1) === 0) continue;
        setKey(rows[i] as Row, k, col[j++]);
      }
    }
    return rows;
  }
}

/** Decodes JARO binary. Integers beyond 2^53 become bigint. Throws JaroError. */
export function decodeBinary(bytes: Uint8Array): unknown {
  if (bytes.length < 2 || bytes[0] !== MAGIC) throw new JaroError("not a JARO binary document", 0);
  if (bytes[1] !== VERSION) throw new JaroError(`unsupported binary version ${bytes[1]}`, 1);
  const d = new BinDec(bytes);
  const v = d.value(0);
  if (d.pos !== bytes.length) throw new JaroError("trailing data", d.pos);
  return v;
}
