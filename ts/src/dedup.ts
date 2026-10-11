import { tableKeys } from "./tables.js";
import { isObj, sortedKeys, utf8Length, type Obj } from "./util.js";

export type Marshal = (v: unknown) => string;

/** Anchor plan. Its traversal mirrors the encoder's emission order. */
export class Dedup {
  private readonly counts = new Map<string, number>();
  private readonly names = new Map<string, number>();

  constructor(
    v: unknown,
    private readonly min: number,
    private readonly columnMajor: boolean,
    private readonly marshal: Marshal,
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

  private visit(v: unknown): void {
    const k = this.key(v);
    if (k !== null) {
      const c = (this.counts.get(k) ?? 0) + 1;
      this.counts.set(k, c);
      if (c > 1) return;
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
    const idx = this.names.size;
    this.names.set(k, idx);
    return { idx, use: false, def: true };
  }
}
