import { keyedSpecs, tableKeys, tableSpecs, type ColSpec } from "./tables.js";
import { hasKey, isObj, sortedKeys, utf8Length, type Obj } from "./util.js";

export type Marshal = (v: unknown) => string;

// Anchoring a string replaces each later copy (c tokens) with a reference of two
// tokens and adds a definition of two tokens (` &`, index): it pays when
// (n-1)(c-2) > 2.
const MIN_ANCHOR_STR = 3; // bytes; shorter strings cost under 3 tokens even when quoted
const REF_TOKENS = 2;
const DEF_TOKENS = 2;

/** Estimates the LLM tokens (o200k_base) of s as the text encoder writes it. */
export function tokCost(s: string, quoted: boolean): number {
  let c = quoted ? 2 : 1;
  const n = s.length;
  for (let i = 0; i < n; ) {
    const cp = s.codePointAt(i) as number;
    if (cp >= 0x80) {
      c++;
      i += cp > 0xffff ? 2 : 1;
    } else if (cp >= 48 && cp <= 57) {
      let j = i;
      while (j < n && (s.charCodeAt(j) >= 48 && s.charCodeAt(j) <= 57)) j++;
      c += Math.floor((j - i + 2) / 3);
      i = j;
    } else if ((cp | 32) >= 97 && (cp | 32) <= 122) {
      let j = i;
      while (j < n && ((s.charCodeAt(j) | 32) >= 97 && (s.charCodeAt(j) | 32) <= 122)) j++;
      c += Math.floor((j - i + 5) / 6);
      i = j;
    } else {
      c++;
      i++;
    }
  }
  return c;
}

/** Anchor plan. Its traversal mirrors the encoder's emission order. */
export class Dedup {
  private readonly counts = new Map<string, number>();
  private readonly names = new Map<string, number>();
  private readonly strN = new Map<string, number>();
  private readonly strIdx = new Map<string, number>();
  private next = 0;

  /** quote tells whether the text encoder quotes a string; it enables string anchors (text only). */
  constructor(
    v: unknown,
    private readonly min: number,
    private readonly columnMajor: boolean,
    private readonly marshal: Marshal,
    private readonly quote: ((s: string) => boolean) | null = null,
  ) {
    this.visit(v);
  }

  private key(v: unknown): string | null {
    if (!Array.isArray(v) && !isObj(v)) return null;
    let s: string;
    try {
      s = this.marshal(v);
    } catch {
      return null;
    }
    return utf8Length(s) < this.min ? null : s;
  }

  private visitCells(specs: ColSpec[], row: Obj): void {
    for (const s of specs) {
      if (s.sub !== null) this.visitCells(s.sub, row[s.key] as Obj);
      else if (hasKey(row, s.key)) this.visit(row[s.key]);
    }
  }

  private visit(v: unknown): void {
    if (typeof v === "string") {
      if (this.quote !== null && utf8Length(v) >= MIN_ANCHOR_STR) this.strN.set(v, (this.strN.get(v) ?? 0) + 1);
      return;
    }
    const k = this.key(v);
    if (k !== null) {
      const c = (this.counts.get(k) ?? 0) + 1;
      this.counts.set(k, c);
      if (c > 1) return;
    }
    if (this.quote !== null) {
      // The text encoder writes row cells, not row objects.
      if (Array.isArray(v)) {
        const specs = tableSpecs(v);
        if (specs !== null) {
          for (const r of v) this.visitCells(specs, r as Obj);
          return;
        }
      } else if (isObj(v)) {
        const specs = keyedSpecs(v);
        if (specs !== null) {
          for (const name of sortedKeys(v)) this.visitCells(specs, v[name] as Obj);
          return;
        }
      }
    }
    if (Array.isArray(v)) {
      const cols = tableKeys(v);
      if (cols === null) {
        for (const el of v) this.visit(el);
      } else if (this.columnMajor) {
        for (const c of cols) for (const row of v) this.visit((row as Obj)[c]);
      } else {
        for (const row of v) for (const c of cols) this.visit((row as Obj)[c]);
      }
    } else if (isObj(v)) {
      for (const k2 of sortedKeys(v)) this.visit(v[k2]);
    }
  }

  /** Called once per value at emission time. */
  ref(v: unknown): { idx: number; use: boolean; def: boolean } | null {
    const k = this.key(v);
    if (k === null || (this.counts.get(k) ?? 0) < 2) return null;
    const seen = this.names.get(k);
    if (seen !== undefined) return { idx: seen, use: true, def: false };
    const idx = this.next++;
    this.names.set(k, idx);
    return { idx, use: false, def: true };
  }

  /** Called once per string value at emission time. */
  refStr(s: string): { idx: number; use: boolean; def: boolean } | null {
    if (this.quote === null || utf8Length(s) < MIN_ANCHOR_STR) return null;
    const n = this.strN.get(s) ?? 0;
    if (n < 2 || (n - 1) * (tokCost(s, this.quote(s)) - REF_TOKENS) <= DEF_TOKENS) return null;
    const seen = this.strIdx.get(s);
    if (seen !== undefined) return { idx: seen, use: true, def: false };
    const idx = this.next++;
    this.strIdx.set(s, idx);
    return { idx, use: false, def: true };
  }
}
