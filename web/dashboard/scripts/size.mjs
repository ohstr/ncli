// Fails the build when a page's gzipped size outgrows its budget.
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

const budgets = [
  ["dist", 150 * 1024],
  ["dist-home", 60 * 1024],
];

function files(d) {
  return readdirSync(d).flatMap((f) => {
    const p = join(d, f);
    return statSync(p).isDirectory() ? files(p) : [p];
  });
}

let over = false;
for (const [name, budget] of budgets) {
  const dir = new URL(`../${name}`, import.meta.url).pathname;
  if (!existsSync(dir)) continue;
  let total = 0;
  for (const f of files(dir)) {
    if (!/\.(js|css|html)$/.test(f)) continue;
    const gz = gzipSync(readFileSync(f)).length;
    total += gz;
    console.log(`${(gz / 1024).toFixed(1).padStart(7)} KiB  ${name}/${f.slice(dir.length + 1)}`);
  }
  console.log(`${(total / 1024).toFixed(1).padStart(7)} KiB  ${name} total gzipped (budget ${budget / 1024} KiB)`);
  if (total > budget) {
    console.error(`${name} over budget`);
    over = true;
  }
}
process.exit(over ? 1 : 0);
