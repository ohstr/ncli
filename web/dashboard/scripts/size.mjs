// Fails the build when the gzipped dashboard outgrows its budget.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

const BUDGET = 150 * 1024;
const dir = new URL("../dist", import.meta.url).pathname;

function files(d) {
  return readdirSync(d).flatMap((f) => {
    const p = join(d, f);
    return statSync(p).isDirectory() ? files(p) : [p];
  });
}

let total = 0;
for (const f of files(dir)) {
  if (!/\.(js|css|html)$/.test(f)) continue;
  const gz = gzipSync(readFileSync(f)).length;
  total += gz;
  console.log(`${(gz / 1024).toFixed(1).padStart(7)} KiB  ${f.slice(dir.length + 1)}`);
}
console.log(`${(total / 1024).toFixed(1).padStart(7)} KiB  total gzipped (budget ${BUDGET / 1024} KiB)`);
if (total > BUDGET) {
  console.error("dashboard bundle over budget");
  process.exit(1);
}
