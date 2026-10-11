// Usage: pnpm bench   (builds dist/ first). JARO_CORPUS = dir of .json files.
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import YAML from "yaml";
import { decodeBinary, encodeBinary, parse, stringify } from "../dist/index.js";

const here = dirname(fileURLToPath(import.meta.url));
const corpus = resolve(process.env.JARO_CORPUS ?? join(here, "../../../corpus"));
const MIN_RUNS = 3;
const MAX_RUNS = 500;
const CELL_MS = 2000;

/** Runs fn at least MIN_RUNS times and until CELL_MS has passed; returns the minimum in ms. */
function timeMin(fn) {
  let best = Infinity;
  let total = 0;
  for (let runs = 0; runs < MAX_RUNS; runs++) {
    const t0 = performance.now();
    fn();
    const dt = performance.now() - t0;
    best = Math.min(best, dt);
    total += dt;
    if (runs + 1 >= MIN_RUNS && total >= CELL_MS) break;
  }
  return best;
}

// Integers beyond 2^53 stay exact for JARO; JSON and YAML use the plain tree.
const exact = (k, v, ctx) => (typeof v === "number" && /^-?\d+$/.test(ctx.source) && !Number.isSafeInteger(v) ? BigInt(ctx.source) : v);

const results = {};
const files = readdirSync(corpus).filter((f) => f.endsWith(".json")).sort();
if (files.length === 0) throw new Error(`no .json files in ${corpus}`);

for (const f of files) {
  const raw = readFileSync(join(corpus, f), "utf8");
  const plain = JSON.parse(raw);
  const tree = JSON.parse(raw, exact);
  const json = JSON.stringify(plain);
  const yaml = YAML.stringify(plain);
  const text = stringify(tree);
  const bin = encodeBinary(tree);
  results[f] = {
    json: { enc_ms: timeMin(() => JSON.stringify(plain)), dec_ms: timeMin(() => JSON.parse(json)), bytes: Buffer.byteLength(json) },
    yaml: { enc_ms: timeMin(() => YAML.stringify(plain)), dec_ms: timeMin(() => YAML.parse(yaml)), bytes: Buffer.byteLength(yaml) },
    jaro_text: { enc_ms: timeMin(() => stringify(tree)), dec_ms: timeMin(() => parse(text)), bytes: Buffer.byteLength(text) },
    jaro_binary: { enc_ms: timeMin(() => encodeBinary(tree)), dec_ms: timeMin(() => decodeBinary(bin)), bytes: bin.length },
  };
  console.error(`done ${f}`);
}

const fmt = (n) => (n >= 100 ? n.toFixed(0) : n >= 10 ? n.toFixed(1) : n.toFixed(2));
const cols = ["file", "format", "enc ms", "dec ms", "bytes", "vs JSON bytes"];
const rows = [];
for (const [f, r] of Object.entries(results)) {
  for (const [name, c] of Object.entries(r)) {
    rows.push([f, name, fmt(c.enc_ms), fmt(c.dec_ms), String(c.bytes), `${((100 * c.bytes) / r.json.bytes).toFixed(0)}%`]);
  }
}
const widths = cols.map((c, i) => Math.max(c.length, ...rows.map((r) => r[i].length)));
const line = (r) => r.map((c, i) => (i < 2 ? c.padEnd(widths[i]) : c.padStart(widths[i]))).join("  ");
console.log(line(cols));
for (const r of rows) console.log(line(r));

writeFileSync(join(here, "results.json"), JSON.stringify(results, null, 2) + "\n");
console.error(`wrote ${join(here, "results.json")}`);
