// Usage: pnpm build && node bench/decode-metric.mjs [--detail]  (JARO_DIST overrides the dist/index.js path)
// Prints the sum over corpus files of min decode ms (text parse + binary decode).
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const dist = process.env.JARO_DIST ?? join(here, "../dist/index.js");
const { decodeBinary, encodeBinary, parse, stringify } = await import(dist);
const corpus = resolve(process.env.JARO_CORPUS ?? join(here, "../../../corpus"));
const WARMUP = 5;
const RUNS = 25;
const TARGET_MS = 20;

const exact = (k, v, ctx) => (typeof v === "number" && /^-?\d+$/.test(ctx.source) && !Number.isSafeInteger(v) ? BigInt(ctx.source) : v);

function minMs(fn) {
  let iters = 1;
  for (;;) {
    const t0 = performance.now();
    for (let i = 0; i < iters; i++) fn();
    const dt = performance.now() - t0;
    if (dt >= TARGET_MS || iters >= 1 << 16) break;
    iters = Math.max(iters + 1, Math.ceil((iters * TARGET_MS * 1.1) / Math.max(dt, 0.05)));
  }
  for (let w = 0; w < WARMUP; w++) for (let i = 0; i < iters; i++) fn();
  let best = Infinity;
  for (let r = 0; r < RUNS; r++) {
    const t0 = performance.now();
    for (let i = 0; i < iters; i++) fn();
    best = Math.min(best, (performance.now() - t0) / iters);
  }
  return best;
}

const files = readdirSync(corpus).filter((f) => f.endsWith(".json")).sort();
if (files.length !== 6) throw new Error(`expected 6 corpus files, got ${files.length}`);
let sum = 0;
const detail = [];
for (const f of files) {
  const tree = JSON.parse(readFileSync(join(corpus, f), "utf8"), exact);
  const text = stringify(tree);
  const bin = encodeBinary(tree);
  const t = minMs(() => parse(text));
  const b = minMs(() => decodeBinary(bin));
  sum += t + b;
  detail.push(`${f} text=${t.toFixed(4)} bin=${b.toFixed(4)}`);
}
if (process.argv.includes("--detail")) console.error(detail.join("\n"));
console.log(sum.toFixed(4));
