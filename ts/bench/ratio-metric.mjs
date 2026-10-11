// Usage: pnpm build && node bench/ratio-metric.mjs [--detail]  (JARO_DIST overrides dist/index.js)
// Prints the geometric mean over 12 cells of (JARO decode ms / JSON.parse ms on the same file),
// measuring both in alternating rounds of the same process and taking the min of each.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const dist = process.env.JARO_DIST ?? join(here, "../dist/index.js");
const { decodeBinary, encodeBinary, parse, stringify } = await import(dist);
const corpus = resolve(process.env.JARO_CORPUS ?? join(here, "../../../corpus"));
const ROUNDS = 25;
const TARGET_MS = 10;

const exact = (k, v, ctx) => (typeof v === "number" && /^-?\d+$/.test(ctx.source) && !Number.isSafeInteger(v) ? BigInt(ctx.source) : v);

function iterations(fn) {
  let iters = 1;
  for (;;) {
    const t0 = performance.now();
    for (let i = 0; i < iters; i++) fn();
    const dt = performance.now() - t0;
    if (dt >= TARGET_MS || iters >= 1 << 16) return iters;
    iters = Math.max(iters + 1, Math.ceil((iters * TARGET_MS * 1.1) / Math.max(dt, 0.05)));
  }
}

function batch(fn, iters) {
  const t0 = performance.now();
  for (let i = 0; i < iters; i++) fn();
  return (performance.now() - t0) / iters;
}

/** Returns [minMsA, minMsB] measured in alternating rounds. */
function paired(fa, fb) {
  const ia = iterations(fa);
  const ib = iterations(fb);
  for (let w = 0; w < 3; w++) {
    batch(fa, ia);
    batch(fb, ib);
  }
  let ba = Infinity;
  let bb = Infinity;
  for (let r = 0; r < ROUNDS; r++) {
    ba = Math.min(ba, batch(fa, ia));
    bb = Math.min(bb, batch(fb, ib));
  }
  return [ba, bb];
}

const files = readdirSync(corpus).filter((f) => f.endsWith(".json")).sort();
if (files.length !== 6) throw new Error(`expected 6 corpus files, got ${files.length}`);
const ratios = {};
for (const f of files) {
  const raw = readFileSync(join(corpus, f), "utf8");
  const tree = JSON.parse(raw, exact);
  const text = stringify(tree);
  const bin = encodeBinary(tree);
  const [t, jt] = paired(() => parse(text), () => JSON.parse(raw));
  const [b, jb] = paired(() => decodeBinary(bin), () => JSON.parse(raw));
  ratios[`${f}:text`] = t / jt;
  ratios[`${f}:binary`] = b / jb;
}
const vals = Object.values(ratios);
const geo = Math.exp(vals.reduce((a, v) => a + Math.log(v), 0) / vals.length);
if (process.argv.includes("--json")) console.error(JSON.stringify(ratios));
else if (process.argv.includes("--detail")) console.error(Object.entries(ratios).map(([k, v]) => `${k}=${v.toFixed(3)}`).join(" "));
console.log(geo.toFixed(4));
