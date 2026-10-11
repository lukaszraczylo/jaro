/** JSON parser that keeps integers beyond 2^53 exact as bigint. */
export function parseJsonExact(text: string): unknown {
  let i = 0;
  const ws = (): void => {
    while (i < text.length && " \t\n\r".includes(text[i] as string)) i++;
  };
  const str = (): string => {
    const start = i;
    i++;
    while (text[i] !== '"') i += text[i] === "\\" ? 2 : 1;
    i++;
    return JSON.parse(text.slice(start, i)) as string;
  };
  const val = (): unknown => {
    ws();
    const c = text[i];
    if (c === "{") {
      i++;
      const o: Record<string, unknown> = {};
      ws();
      if (text[i] === "}") {
        i++;
        return o;
      }
      for (;;) {
        ws();
        const k = str();
        ws();
        i++;
        Object.defineProperty(o, k, { value: val(), enumerable: true, writable: true, configurable: true });
        ws();
        if (text[i++] === "}") return o;
      }
    }
    if (c === "[") {
      i++;
      const a: unknown[] = [];
      ws();
      if (text[i] === "]") {
        i++;
        return a;
      }
      for (;;) {
        a.push(val());
        ws();
        if (text[i++] === "]") return a;
      }
    }
    if (c === '"') return str();
    const m = /^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|^true|^false|^null/.exec(text.slice(i, i + 400));
    if (!m) throw new Error(`bad json at ${i}`);
    i += m[0].length;
    if (m[0] === "true") return true;
    if (m[0] === "false") return false;
    if (m[0] === "null") return null;
    if (/^-?\d+$/.test(m[0])) {
      const b = BigInt(m[0]);
      return b >= -9007199254740991n && b <= 9007199254740991n ? Number(b) : b;
    }
    return Number(m[0]);
  };
  const v = val();
  ws();
  if (i !== text.length) throw new Error("trailing json");
  return v;
}

export function hexToBytes(h: string): Uint8Array {
  const out = new Uint8Array(h.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
  return out;
}

export function bytesToHex(b: Uint8Array): string {
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

/** Strict deep equality that treats -0 as 0 and compares own keys. */
export function same(a: unknown, b: unknown): boolean {
  if (typeof a === "number" && typeof b === "number") return a === b;
  if (Array.isArray(a)) return Array.isArray(b) && a.length === b.length && a.every((x, i) => same(x, b[i]));
  if (typeof a === "object" && a !== null) {
    if (typeof b !== "object" || b === null || Array.isArray(b)) return false;
    const ka = Object.keys(a);
    const kb = Object.keys(b);
    if (ka.length !== kb.length) return false;
    return ka.every((k) => Object.prototype.hasOwnProperty.call(b, k) && same((a as Record<string, unknown>)[k], (b as Record<string, unknown>)[k]));
  }
  return a === b;
}
