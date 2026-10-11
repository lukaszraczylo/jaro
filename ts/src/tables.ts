import { cmpKeys, isObj, MAX_NEST, sortedKeys, TEXT_PRESENT, type Obj } from "./util.js";

/** One table column. A column with `sub` holds an object in every row. */
export interface ColSpec {
  key: string;
  sub: ColSpec[] | null;
}

interface ColInfo {
  objs: Obj[];
  n: number;
  flat: boolean;
}

/** Returns the columns for rows (all objects) and whether enough cells are present. */
export function buildSpecs(rows: Obj[], depth: number): ColSpec[] | null {
  const info = new Map<string, ColInfo>();
  let present = 0;
  for (const m of rows) {
    const keys = Object.keys(m);
    present += keys.length;
    for (const k of keys) {
      let c = info.get(k);
      if (c === undefined) {
        c = { objs: [], n: 0, flat: false };
        info.set(k, c);
      }
      c.n++;
      const v = m[k];
      if (isObj(v)) c.objs.push(v);
      else c.flat = true;
    }
  }
  if (info.size === 0 || present < TEXT_PRESENT * (info.size * rows.length)) return null;
  const keys = [...info.keys()].sort(cmpKeys);
  return keys.map((key) => {
    const c = info.get(key) as ColInfo;
    let sub: ColSpec[] | null = null;
    if (depth < MAX_NEST && !c.flat && c.n === rows.length) {
      const s = buildSpecs(c.objs, depth + 1);
      if (s !== null && s.length >= 2) sub = s;
    }
    return { key, sub };
  });
}

export function allObjects(a: unknown[]): a is Obj[] {
  for (const x of a) if (!isObj(x)) return false;
  return true;
}

/** Columns when `rows` can be written as a text table. */
export function tableSpecs(rows: unknown[]): ColSpec[] | null {
  if (rows.length < 2 || !allObjects(rows)) return null;
  return buildSpecs(rows, 0);
}

/** Columns when every value of `m` is an object and a table of them has two keys. */
export function keyedSpecs(m: Obj): ColSpec[] | null {
  const keys = Object.keys(m);
  if (keys.length < 2) return null;
  const rows: Obj[] = [];
  for (const k of keys) {
    const v = m[k];
    if (!isObj(v)) return null;
    rows.push(v);
  }
  const specs = buildSpecs(rows, 0);
  return specs !== null && specs.length >= 2 ? specs : null;
}

/** Union of keys when `a` holds 2+ objects and enough cells would be present. */
export function sparseKeys(a: unknown[], minPresent: number): string[] | null {
  if (a.length < 2) return null;
  const seen = new Set<string>();
  let present = 0;
  for (const x of a) {
    if (!isObj(x)) return null;
    const keys = Object.keys(x);
    present += keys.length;
    for (const k of keys) seen.add(k);
  }
  if (seen.size === 0 || present < minPresent * (seen.size * a.length)) return null;
  return [...seen].sort(cmpKeys);
}

/** Shared sorted keys if `a` is 2+ non-empty objects with identical keys. */
export function tableKeys(a: unknown[]): string[] | null {
  if (a.length < 2 || !allObjects(a)) return null;
  const first = a[0] as Obj;
  const fk = Object.keys(first);
  if (fk.length === 0) return null;
  for (let i = 1; i < a.length; i++) {
    const m = a[i] as Obj;
    if (Object.keys(m).length !== fk.length) return null;
    for (const k of fk) if (!Object.prototype.hasOwnProperty.call(m, k)) return null;
  }
  return sortedKeys(first);
}

/** tableKeys plus the cell values, column-major. */
export function tableData(a: unknown[]): { keys: string[]; cols: unknown[][] } | null {
  const keys = tableKeys(a);
  if (keys === null) return null;
  const cols = keys.map((k) => (a as Obj[]).map((m) => m[k]));
  return { keys, cols };
}
