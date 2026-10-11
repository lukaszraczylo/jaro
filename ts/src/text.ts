import { Dedup } from "./dedup.js";
import { Candidate } from "./shapes.js";
import { keyedSpecs, tableSpecs, type ColSpec } from "./tables.js";
import {
  DEFAULT_MIN_DEDUP,
  deepCopy,
  hasKey,
  hasLoneSurrogate,
  intValue,
  isObj,
  JaroError,
  MAX_DEPTH,
  MAX_EXPAND,
  MAX_I64,
  MAX_NEST,
  MIN_I64,
  numKind,
  setKey,
  sortedKeys,
  wellFormed,
  type EncodeOptions,
  type Obj,
  type Row,
} from "./util.js";

const ABSENT = "^";
const PREDICT_AFTER = 64;
const MIN_PREDICT_KEYS = 3;
const MAX_PREDICT_KEYS = 64;
const DELIM_RE = /[\u0000- "{}[\]|:\\#]/;

const DELIMS = (() => {
  const t = new Uint8Array(128);
  for (let c = 0; c < 0x20; c++) t[c] = 1;
  for (const ch of ' \t\n\r"{}[]|:\\#') t[ch.charCodeAt(0)] = 1;
  return t;
})();

function isDelim(c: number): boolean {
  return c < 128 && DELIMS[c] === 1;
}

/** Reports whether a bare token would start like a number. */
function numLike(s: string): boolean {
  let i = 0;
  if (s.charCodeAt(0) === 45) i = 1;
  if (i >= s.length) return false;
  const c = s.charCodeAt(i);
  return (c >= 48 && c <= 57) || (c === 46 && i + 1 < s.length);
}

function needsQuote(s: string): boolean {
  if (s === "" || s === "~" || s === ABSENT || s === "true" || s === "false") return true;
  const c = s.charCodeAt(0);
  if (c === 38 || c === 42) return true;
  return DELIM_RE.test(s) || numLike(s);
}

/** Formats a finite non-integral-or-huge float like Go's 'g' with shortest digits. */
export function formatFloat(f: number): string {
  const neg = f < 0;
  const [mant, ex] = Math.abs(f).toExponential().split("e") as [string, string];
  const digits = mant.replace(".", "");
  const exp = Number(ex);
  let out: string;
  if (exp < -4 || exp >= 6) {
    const a = Math.abs(exp);
    out = digits[0] + (digits.length > 1 ? "." + digits.slice(1) : "") + "e" + (exp < 0 ? "-" : "+") + (a < 10 ? "0" : "") + a;
  } else if (exp < 0) {
    out = "0." + "0".repeat(-exp - 1) + digits;
  } else if (digits.length <= exp + 1) {
    out = digits + "0".repeat(exp + 1 - digits.length);
  } else {
    out = digits.slice(0, exp + 1) + "." + digits.slice(exp + 1);
  }
  return neg ? "-" + out : out;
}

class TextEnc {
  out = "";

  constructor(private readonly dd: Dedup | null) {}

  str(s: string): void {
    s = wellFormed(s);
    if (!needsQuote(s)) {
      this.out += s;
      return;
    }
    let q = '"';
    let start = 0;
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i);
      let esc: string | null = null;
      if (c === 34) esc = '\\"';
      else if (c === 92) esc = "\\\\";
      else if (c === 10) esc = "\\n";
      else if (c === 13) esc = "\\r";
      else if (c === 9) esc = "\\t";
      else if (c < 0x20) esc = "\\u00" + (c < 16 ? "0" : "") + c.toString(16);
      if (esc !== null) {
        q += s.slice(start, i) + esc;
        start = i + 1;
      }
    }
    this.out += q + s.slice(start) + '"';
  }

  value(v: unknown, depth: number): void {
    if (depth > MAX_DEPTH) throw new JaroError("nesting too deep");
    if (this.dd !== null) {
      const r = this.dd.ref(v);
      if (r !== null) {
        if (r.use) {
          this.out += "*a" + r.idx;
          return;
        }
        this.out += "&a" + r.idx + " ";
      }
    }
    if (v === null) this.out += "~";
    else if (typeof v === "boolean") this.out += v ? "true" : "false";
    else if (typeof v === "string") this.str(v);
    else if (Array.isArray(v)) this.array(v, depth);
    else if (isObj(v)) {
      const specs = keyedSpecs(v);
      if (specs !== null) return this.keyedTable(v, specs, depth);
      this.out += "{";
      this.objBody(v, depth + 1);
      this.out += "}";
    } else {
      const k = typeof v === "number" || typeof v === "bigint" ? numKind(v) : 0;
      if (k === 0) throw new JaroError(`unsupported type ${v === undefined ? "undefined" : typeof v}`);
      this.out += k === 2 ? formatFloat(v as number) : v === 0 ? "0" : String(v);
    }
  }

  objBody(m: Obj, depth: number): void {
    const keys = sortedKeys(m);
    for (let i = 0; i < keys.length; i++) {
      const k = keys[i] as string;
      if (i > 0) this.out += " ";
      this.str(k);
      this.out += ":";
      this.value(m[k], depth);
    }
  }

  private array(a: unknown[], depth: number): void {
    this.out += "[";
    const specs = tableSpecs(a);
    if (specs !== null) {
      this.out += "|";
      this.specHeader(specs);
      this.out += "|";
      for (const r of a) this.specCells(specs, r as Obj, depth);
    } else {
      for (let i = 0; i < a.length; i++) {
        if (i > 0) this.out += " ";
        this.value(a[i], depth + 1);
      }
    }
    this.out += "]";
  }

  private specHeader(specs: ColSpec[]): void {
    for (let i = 0; i < specs.length; i++) {
      const s = specs[i] as ColSpec;
      if (i > 0) this.out += " ";
      this.str(s.key);
      if (s.sub !== null) {
        this.out += "{";
        this.specHeader(s.sub);
        this.out += "}";
      }
    }
  }

  private specCells(specs: ColSpec[], row: Obj, depth: number): void {
    for (const s of specs) {
      if (s.sub !== null) this.specCells(s.sub, row[s.key] as Obj, depth);
      else if (!hasKey(row, s.key)) this.out += " " + ABSENT;
      else {
        this.out += " ";
        this.value(row[s.key], depth + 1);
      }
    }
  }

  private keyedTable(m: Obj, specs: ColSpec[], depth: number): void {
    this.out += "{|";
    this.specHeader(specs);
    this.out += "|";
    for (const name of sortedKeys(m)) {
      this.out += " ";
      this.str(name);
      this.specCells(specs, m[name] as Obj, depth);
    }
    this.out += "}";
  }
}

export function plainText(v: unknown): string {
  const e = new TextEnc(null);
  if (isObj(v) && Object.keys(v).length > 0) e.objBody(v, 0);
  else e.value(v, 0);
  return e.out;
}

/** Encodes a value as JARO text. Throws JaroError for unsupported input. */
export function stringify(value: unknown, opts: EncodeOptions = {}): string {
  const dd = opts.dedup === true ? new Dedup(value, opts.minDedup ?? DEFAULT_MIN_DEDUP, false, plainText) : null;
  const e = new TextEnc(dd);
  if (isObj(value) && Object.keys(value).length > 0) e.objBody(value, 0);
  else e.value(value, 0);
  return e.out;
}

const DEC = String.raw`\d+`;
const FLOAT_RE = new RegExp(String.raw`^-?(?:${DEC}(?:\.(?:${DEC})?)?|\.${DEC})(?:[eE][+-]?${DEC})?$`);
const INT_RE = /^-?\d+$/;
const HEX4_RE = /^[0-9a-fA-F]{4}$/;

interface Pred {
  cand: Candidate | null;
  misses: number;
}

class TextDec {
  pos = 0;
  private readonly vals: unknown[] = [];
  private readonly preds = new Map<string, Pred>();
  private objects = 0;
  private readonly budget = { n: MAX_EXPAND };
  private readonly anchors = new Map<string, unknown>();

  constructor(private readonly s: string) {}

  private err(msg: string): JaroError {
    return new JaroError(msg, this.pos);
  }

  private ws(): void {
    const s = this.s;
    while (this.pos < s.length) {
      const c = s.charCodeAt(this.pos);
      if (c === 32 || c === 9 || c === 10 || c === 13) this.pos++;
      else if (c === 35) {
        while (this.pos < s.length && s.charCodeAt(this.pos) !== 10) this.pos++;
      } else return;
    }
  }

  private cur(): number {
    return this.pos < this.s.length ? this.s.charCodeAt(this.pos) : -1;
  }

  parseDocument(): unknown {
    const s = this.s;
    if (hasLoneSurrogate(s)) throw new JaroError("input is not valid UTF-8", 0);
    this.ws();
    if (this.pos >= s.length) throw this.err("empty input");
    if (this.atKey()) return this.objBody(0, -1);
    const v = this.value(0);
    this.ws();
    if (this.pos !== s.length) throw this.err("trailing data");
    return v;
  }

  private atKey(): boolean {
    const s = this.s;
    let p = this.pos;
    if (s.charCodeAt(p) === 34) {
      p++;
      while (p < s.length && s.charCodeAt(p) !== 34) {
        if (s.charCodeAt(p) === 92) p++;
        p++;
      }
      p++;
    } else {
      while (p < s.length && !isDelim(s.charCodeAt(p))) p++;
    }
    return p < s.length && s.charCodeAt(p) === 58;
  }

  private objBody(depth: number, end: number): Row {
    if (end !== 125 || ++this.objects <= PREDICT_AFTER) return this.objRest({}, depth, end, null, "");
    this.ws();
    const first = this.peekKey();
    if (first === null) return this.objRest({}, depth, end, null, "");
    const slot = this.preds.get(first);
    if (slot === undefined) {
      this.preds.set(first, { cand: null, misses: 0 });
      return this.objRest({}, depth, end, null, first);
    }
    if (slot.cand !== null) return this.predicted(slot, first, depth);
    return this.objRest({}, depth, end, [], first);
  }

  /** Returns the bare key at the cursor when a bare member starts here, else null; does not move. */
  private peekKey(): string | null {
    const s = this.s;
    const start = this.pos;
    let p = start;
    while (p < s.length && !isDelim(s.charCodeAt(p))) p++;
    return p > start && s.charCodeAt(p) === 58 ? s.slice(start, p) : null;
  }

  /** Reads an object whose keys are expected to repeat the last same-depth object's keys. */
  private predicted(slot: Pred, first: string, depth: number): Row {
    const cand = slot.cand as Candidate;
    const s = this.s;
    const keys = cand.keys;
    const n = keys.length;
    const vals = this.vals;
    const base = vals.length;
    for (let i = 0; i < n; i++) {
      this.ws();
      const key = keys[i] as string;
      const p = this.pos;
      if (!s.startsWith(key, p) || s.charCodeAt(p + key.length) !== 58) return this.diverge(slot, first, i, base, depth);
      this.pos = p + key.length + 1;
      vals.push(this.value(depth + 1));
    }
    this.ws();
    if (this.cur() !== 125) return this.diverge(slot, first, n, base, depth);
    this.pos++;
    slot.misses = 0;
    const obj = cand.make(vals, base);
    vals.length = base;
    return obj;
  }

  private diverge(slot: Pred, first: string, i: number, base: number, depth: number): Row {
    const cand = slot.cand as Candidate;
    const vals = this.vals;
    const m: Row = {};
    for (let j = 0; j < i; j++) setKey(m, cand.keys[j] as string, vals[base + j]);
    vals.length = base;
    const replace = ++slot.misses >= 2;
    return this.objRest(m, depth, 125, replace ? cand.keys.slice(0, i) : null, first);
  }

  /** Reads members until the closer; track collects the keys when the object may become a prediction. */
  private objRest(m: Row, depth: number, end: number, track: string[] | null, first: string): Row {
    let spreadKeys: Set<string> | null = null;
    for (;;) {
      this.ws();
      const c = this.cur();
      if (c < 0) {
        if (end === -1) return m;
        throw this.err("unterminated object");
      }
      if (end !== -1 && c === end) {
        this.pos++;
        if (track !== null && spreadKeys === null && track.length >= MIN_PREDICT_KEYS && track.length <= MAX_PREDICT_KEYS && !track.some(needsQuote)) {
          this.preds.set(first, { cand: new Candidate([], track), misses: 0 });
        }
        return m;
      }
      if (c === 42) {
        const src = this.deref();
        if (!isObj(src)) throw this.err("spread needs an object anchor");
        spreadKeys ??= new Set();
        for (const k of Object.keys(src)) {
          setKey(m, k, src[k]);
          spreadKeys.add(k);
        }
        continue;
      }
      const k = this.token();
      if (typeof k !== "string" || this.cur() !== 58) throw this.err("expected key:");
      this.pos++;
      if (hasKey(m, k) && !(spreadKeys?.has(k) ?? false)) throw this.err(`duplicate key ${JSON.stringify(k)}`);
      spreadKeys?.delete(k);
      track?.push(k);
      setKey(m, k, this.value(depth + 1));
    }
  }

  private value(depth: number): unknown {
    if (depth > MAX_DEPTH) throw this.err("nesting too deep");
    this.ws();
    const c = this.cur();
    if (c < 0) throw this.err("unexpected end");
    switch (c) {
      case 38:
        return this.anchor(depth);
      case 42:
        return this.deref();
      case 123:
        this.pos++;
        this.ws();
        if (this.cur() === 124) {
          this.pos++;
          return this.keyedTable(depth);
        }
        return this.objBody(depth, 125);
      case 91:
        this.pos++;
        return this.array(depth);
      case 125:
      case 93:
      case 124:
      case 58:
        throw this.err(`unexpected ${JSON.stringify(String.fromCharCode(c))}`);
    }
    return this.token();
  }

  private array(depth: number): unknown {
    this.ws();
    if (this.cur() === 124) {
      this.pos++;
      return this.tableRows(depth, this.specList(124, 0));
    }
    const out: unknown[] = [];
    for (;;) {
      this.ws();
      const c = this.cur();
      if (c < 0) throw this.err("unterminated array");
      if (c === 93) {
        this.pos++;
        return out;
      }
      if (depth < MAX_DEPTH && ((c >= 48 && c <= 57) || c === 45)) out.push(this.token());
      else out.push(this.value(depth + 1));
    }
  }

  /** Reads a quoted or bare scalar. A quoted token is always a string. */
  private token(): unknown {
    const s = this.s;
    if (s.charCodeAt(this.pos) === 34) return this.quoted();
    const start = this.pos;
    const c0 = s.charCodeAt(start);
    if ((c0 >= 48 && c0 <= 57) || c0 === 45 || c0 === 46) {
      const fast = fastNumber(s, start);
      if (fast === fast) {
        this.pos = numEnd;
        return fast;
      }
    }
    let p = start;
    while (p < s.length && !isDelim(s.charCodeAt(p))) p++;
    if (p === start) throw this.err(`unexpected ${JSON.stringify(s[p])}`);
    this.pos = p;
    const tok = s.slice(start, p);
    if (c0 === 126) {
      if (tok === "~") return null;
    } else if (c0 === 116) {
      if (tok === "true") return true;
    } else if (c0 === 102) {
      if (tok === "false") return false;
    } else if ((c0 >= 48 && c0 <= 57) || c0 === 45 || c0 === 46) {
      if (numLike(tok)) {
        const n = parseNumber(tok);
        if (n !== undefined) return n;
      }
    }
    return tok;
  }

  private quoted(): string {
    const s = this.s;
    const n = s.length;
    for (let p = this.pos + 1; p < n; p++) {
      const c = s.charCodeAt(p);
      if (c === 34) {
        const v = s.slice(this.pos + 1, p);
        this.pos = p + 1;
        return v;
      }
      if (c === 92) break;
    }
    this.pos++;
    let out = "";
    let start = this.pos;
    while (this.pos < s.length) {
      const c = s.charCodeAt(this.pos);
      if (c === 34) {
        out += s.slice(start, this.pos);
        this.pos++;
        return out;
      }
      if (c !== 92) {
        this.pos++;
        continue;
      }
      out += s.slice(start, this.pos);
      this.pos++;
      if (this.pos >= s.length) throw this.err("bad escape");
      const e = s[this.pos++] as string;
      switch (e) {
        case '"':
        case "\\":
          out += e;
          break;
        case "n":
          out += "\n";
          break;
        case "r":
          out += "\r";
          break;
        case "t":
          out += "\t";
          break;
        case "u": {
          const h = s.slice(this.pos, this.pos + 4);
          if (!HEX4_RE.test(h)) throw this.err("bad \\u escape");
          this.pos += 4;
          const n = parseInt(h, 16);
          if (n >= 0xd800 && n <= 0xdfff) {
            const lo = n <= 0xdbff && s.startsWith("\\u", this.pos) ? s.slice(this.pos + 2, this.pos + 6) : "";
            if (!HEX4_RE.test(lo) || parseInt(lo, 16) < 0xdc00 || parseInt(lo, 16) > 0xdfff) throw this.err("lone surrogate escape");
            this.pos += 6;
            out += String.fromCharCode(n, parseInt(lo, 16));
          } else out += String.fromCharCode(n);
          break;
        }
        default:
          throw this.err(`bad escape \\${e}`);
      }
      start = this.pos;
    }
    throw this.err("unterminated string");
  }

  private name(): string {
    this.pos++;
    const start = this.pos;
    while (this.pos < this.s.length && !isDelim(this.s.charCodeAt(this.pos))) this.pos++;
    if (this.pos === start) throw this.err("missing anchor name");
    return this.s.slice(start, this.pos);
  }

  private anchor(depth: number): unknown {
    const n = this.name();
    if (this.anchors.has(n)) throw this.err(`anchor &${n} defined twice`);
    const v = this.value(depth + 1);
    this.anchors.set(n, v);
    return v;
  }

  private deref(): unknown {
    const n = this.name();
    if (!this.anchors.has(n)) throw this.err(`undefined anchor *${n}`);
    return deepCopy(this.anchors.get(n), this.budget);
  }

  private specList(end: number, depth: number): ColSpec[] {
    if (depth > MAX_NEST) throw this.err("table header nested too deep");
    const specs: ColSpec[] = [];
    const seen = new Set<string>();
    for (;;) {
      this.ws();
      const c = this.cur();
      if (c < 0) throw this.err("unterminated table header");
      if (c === end) {
        this.pos++;
        break;
      }
      const k = this.token();
      if (typeof k !== "string") throw this.err("table key must be a string");
      if (seen.has(k)) throw this.err(`duplicate column ${JSON.stringify(k)}`);
      seen.add(k);
      let sub: ColSpec[] | null = null;
      this.ws();
      if (this.cur() === 123) {
        this.pos++;
        sub = this.specList(125, depth + 1);
      }
      specs.push({ key: k, sub });
    }
    if (specs.length === 0) throw this.err("empty table header");
    return specs;
  }

  private readRow(specs: ColSpec[], row: Row, depth: number, closer: number): void {
    for (const s of specs) {
      if (s.sub !== null) {
        const obj: Row = {};
        this.readRow(s.sub, obj, depth, closer);
        setKey(row, s.key, obj);
        continue;
      }
      this.ws();
      const c = this.cur();
      if (c < 0) throw this.err("unterminated table");
      if (c === closer) throw this.err("table row is incomplete");
      if (c === 94 && (this.pos + 1 >= this.s.length || isDelim(this.s.charCodeAt(this.pos + 1)))) {
        this.pos++;
        continue;
      }
      setKey(row, s.key, this.value(depth + 1));
    }
  }

  private keyedTable(depth: number): Row {
    const specs = this.specList(124, 0);
    const out: Row = {};
    for (;;) {
      this.ws();
      const c = this.cur();
      if (c < 0) throw this.err("unterminated object");
      if (c === 125) {
        this.pos++;
        return out;
      }
      const name = this.token();
      if (typeof name !== "string") throw this.err("expected row name");
      if (hasKey(out, name)) throw this.err(`duplicate row name ${JSON.stringify(name)}`);
      const row: Row = {};
      this.readRow(specs, row, depth, 125);
      setKey(out, name, row);
    }
  }

  private tableRows(depth: number, specs: ColSpec[]): unknown[] {
    const rows: unknown[] = [];
    for (;;) {
      this.ws();
      const c = this.cur();
      if (c < 0) throw this.err("unterminated array");
      if (c === 93) {
        this.pos++;
        return rows;
      }
      const row: Row = {};
      this.readRow(specs, row, depth, 93);
      rows.push(row);
    }
  }
}

let numEnd = 0;

/** Scans a plain decimal token without regexes. Returns NaN when the slow path must decide. */
function fastNumber(s: string, start: number): number {
  const end = s.length;
  let p = start;
  const neg = s.charCodeAt(p) === 45;
  if (neg) p++;
  let c = 0;
  let v = 0;
  let nd = 0;
  while (p < end && (c = s.charCodeAt(p)) >= 48 && c <= 57) {
    v = v * 10 + (c - 48);
    p++;
    nd++;
  }
  if (p === end || isDelim(c)) {
    if (nd === 0 || nd > 15) return NaN;
    numEnd = p;
    return v === 0 ? 0 : neg ? -v : v;
  }
  let fd = 0;
  if (c === 46) {
    p++;
    c = s.charCodeAt(p);
    while (p < end && (c = s.charCodeAt(p)) >= 48 && c <= 57) {
      p++;
      fd++;
    }
    if (nd === 0 && fd === 0) return NaN;
  } else if (c !== 101 && c !== 69) return NaN;
  if (p < end && !isDelim(c)) {
    if (c !== 101 && c !== 69) return NaN;
    p++;
    if (p < end && ((c = s.charCodeAt(p)) === 43 || c === 45)) p++;
    let ed = 0;
    while (p < end && (c = s.charCodeAt(p)) >= 48 && c <= 57) {
      p++;
      ed++;
    }
    if (ed === 0) return NaN;
  }
  if (p < end && !isDelim(s.charCodeAt(p))) return NaN;
  const f = Number(s.slice(start, p));
  if (f === 0) {
    numEnd = p;
    return 0;
  }
  if (!Number.isFinite(f)) return NaN;
  numEnd = p;
  return f;
}

/** Parses a number-like token, or returns undefined when it is a string. */
function parseNumber(tok: string): number | bigint | undefined {
  if (INT_RE.test(tok)) {
    if (tok.length <= 15) return Number(tok) + 0;
    const b = BigInt(tok);
    if (b >= MIN_I64 && b <= MAX_I64) return intValue(b);
    return Number(tok);
  }
  if (!FLOAT_RE.test(tok)) return undefined;
  const f = Number(tok);
  if (!Number.isFinite(f)) return undefined;
  return f === 0 ? 0 : f;
}

/** Decodes JARO text. Integers beyond 2^53 become bigint. Throws JaroError. */
export function parse(text: string): unknown {
  return new TextDec(text).parseDocument();
}
