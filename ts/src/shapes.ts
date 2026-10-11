import { setKey, type Row } from "./util.js";

type ObjFn = (v: unknown[], o: number) => Row;
interface Shape {
  uses: number;
  fn: ObjFn | null;
}

const COMPILE_AFTER = 32;
const MAX_KEYS = 64;
const MAX_SHAPES = 256;
const shapes = new Map<string, Shape>();

function shapeFor(keys: string[]): Shape | null {
  if (keys.length > MAX_KEYS || keys.includes("__proto__")) return null;
  const sig = JSON.stringify(keys);
  let shape = shapes.get(sig);
  if (shape === undefined) {
    if (shapes.size >= MAX_SHAPES) return null;
    shape = { uses: 0, fn: null };
    shapes.set(sig, shape);
  }
  return shape;
}

function compile(keys: string[], shape: Shape): void {
  try {
    const body = keys.map((k, j) => `${JSON.stringify(k)}:v[o${j === 0 ? "" : "+" + j}]`).join(",");
    shape.fn = new Function("v", "o", `return {${body}}`) as ObjFn;
  } catch {
    shape.uses = -Infinity;
  }
}

const MIN_KEYS = 3;
const REF_SPAN = MAX_KEYS + 1;

/** An object key sequence given as string-pool refs; builds objects with that exact key order. */
export class Candidate {
  private shape: Shape | null | undefined;

  constructor(
    readonly refs: number[],
    readonly keys: string[],
  ) {}

  /** Builds an object from v[o .. o+keys.length). */
  make(v: unknown[], o: number): Row {
    if (this.shape === undefined) this.shape = shapeFor(this.keys);
    const shape = this.shape;
    if (shape !== null) {
      if (shape.fn !== null) return shape.fn(v, o);
      if (++shape.uses >= COMPILE_AFTER) compile(this.keys, shape);
    }
    const m: Row = {};
    for (let j = 0; j < this.keys.length; j++) setKey(m, this.keys[j] as string, v[o + j]);
    return m;
  }
}

/** Per-document memory of repeated object key sequences, found by first key ref and key count. */
export class ShapeIndex {
  private readonly seen = new Map<number, Candidate | null>();

  static slot(ref0: number, n: number): number {
    return n >= MIN_KEYS && n <= MAX_KEYS ? ref0 * REF_SPAN + n : -1;
  }

  /** Returns the candidate for the slot; undefined if unseen, null once the slot was seen a single time. */
  get(slot: number): Candidate | null | undefined {
    return this.seen.get(slot);
  }

  markSeen(slot: number): void {
    this.seen.set(slot, null);
  }

  add(slot: number, c: Candidate): void {
    this.seen.set(slot, c);
  }
}
