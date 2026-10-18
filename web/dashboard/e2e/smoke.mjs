// Browser smoke test of the dashboard against a real relay: both ways of
// serving it, sign-in, every chart, an index job, themes and phone width.
// Fails on any console error or CSP violation.
//
//   NCLI_BIN=../../bin/ncli npm run e2e
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright";

const bin = process.env.NCLI_BIN;
if (!bin) throw new Error("set NCLI_BIN to the ncli binary");
const dir = mkdtempSync(join(tmpdir(), "ncli-dash-e2e-"));
const children = [];
const problems = [];
let failures = 0;

function check(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) failures++;
}

const ncli = (args, input) => {
  const r = spawnSync(bin, args, { input, encoding: "utf8", env: { ...process.env, HOME: dir, NO_COLOR: "1" } });
  if (r.status !== 0) throw new Error(`ncli ${args.join(" ")}: ${r.stderr}`);
  return r.stdout;
};

const freePort = () =>
  new Promise((resolve) => {
    const s = createServer().listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });

function start(args) {
  const p = spawn(bin, args, { env: { ...process.env, HOME: dir }, stdio: ["ignore", "pipe", "pipe"] });
  children.push(p);
  return p;
}

async function waitFor(url, ms = 15000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    try {
      await fetch(url);
      return;
    } catch {
      await new Promise((r) => setTimeout(r, 200));
    }
  }
  throw new Error(`nothing answered at ${url}`);
}

// The browser logs every 401 response, including the expected ones: the
// session check before sign-in, and this test's probe after sign-out.
const expected401 = /Failed to load resource: the server responded with a status of 401/;

function watch(page, label) {
  page.on("console", (m) => {
    if (m.type() === "error" && !expected401.test(m.text())) problems.push(`${label}: console: ${m.text()}`);
  });
  page.on("pageerror", (e) => problems.push(`${label}: ${e.message}`));
}

const cspProbe = () =>
  document.addEventListener("securitypolicyviolation", (e) => console.error(`CSP: ${e.violatedDirective} ${e.blockedURI}`));

try {
  const key = JSON.parse(ncli(["id", "--json", "-q"]));
  const port = await freePort();
  const adminPort = await freePort();
  const cfg = join(dir, "relay.yaml");
  writeFileSync(
    cfg,
    [
      `port: ${port}`,
      `nip11: {name: e2e, privkey: ${key.priv_hex}}`,
      `store: ${join(dir, "notes.db")}`,
      `logdir: ${dir}`,
      `admin: {listen: "127.0.0.1:${adminPort}", dashboard: {enabled: true}}`,
    ].join("\n"),
  );
  start(["relay", "--config", cfg]);
  const admin = `http://127.0.0.1:${adminPort}`;
  await waitFor(`${admin}/admin/ui/`);
  // Some traffic for the charts.
  for (let i = 0; i < 3; i++) spawnSync(bin, ["find", "-s", `ws://127.0.0.1:${port}`, "--kinds", "1", "-q"]);

  const browser = await chromium.launch();

  // 1. Relay-served, no signer: the login screen says what to do instead.
  {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    watch(page, "no-signer");
    await page.addInitScript(cspProbe);
    await page.goto(`${admin}/admin/ui/`);
    await page.getByRole("tab", { name: "Secret key" }).waitFor();
    check((await page.getByRole("tab", { name: "Secret key" }).getAttribute("aria-selected")) === "true", "without an extension, the secret-key tab opens first");
    await page.getByRole("tab", { name: "Extension" }).click();
    check(await page.getByRole("button", { name: /Sign in with Nostr extension/ }).isDisabled(), "extension sign-in disabled without a NIP-07 signer");
    check(await page.getByText("ncli relay dashboard").isVisible(), "login screen points to `ncli relay dashboard`");
    await ctx.close();
  }

  // 2. Relay-served with a NIP-07 signer backed by `ncli id sign`.
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await ctx.exposeFunction("__ncliSign", (ev) => {
      const out = ncli(["id", "sign", "-e", "-", "-o", "-", "--identity", key.nsec, "-q"], JSON.stringify(ev));
      const signed = JSON.parse(out);
      return Array.isArray(signed) ? signed[0] : signed;
    });
    await ctx.addInitScript(() => {
      window.nostr = { getPublicKey: async () => "", signEvent: (ev) => window.__ncliSign(ev) };
    });
    const page = await ctx.newPage();
    watch(page, "relay-served");
    await page.addInitScript(cspProbe);
    await page.goto(`${admin}/admin/ui/`);
    await page.getByRole("button", { name: /Sign in/ }).click();
    await page.locator(".kpis").waitFor();
    await page.waitForTimeout(5000);
    check((await page.locator(".chart canvas").count()) === 18, "18 charts render");
    check((await page.locator(".kpi").count()) === 11, "11 KPI tiles render");
    check((await page.locator(".dot.live").count()) === 1, "live stream connected");
    check(await page.getByText(key.pub_hex.slice(0, 8)).isVisible(), "header shows the signed-in admin");

    // An index job from the page.
    const zaps = page.locator(".index", { hasText: "zaps" });
    await zaps.getByRole("button", { name: "Rebuild" }).click();
    await zaps.getByText(/last rebuild/).waitFor({ timeout: 15000 });
    check(await zaps.getByText("succeeded").isVisible(), "rebuild from the page succeeds and shows it");
    check(await page.locator(".index", { hasText: "search" }).getByText("cache.search.enabled").isVisible(), "unavailable index says why");

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(300);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    check(overflow <= 0, `no horizontal scroll at 390px (overflow ${overflow}px)`);

    await page.emulateMedia({ colorScheme: "dark" });
    await page.waitForTimeout(300);
    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    check(bg !== "rgb(246, 247, 249)", `dark theme applies (body ${bg})`);

    await page.getByRole("button", { name: "Sign out" }).click();
    await page.getByText("Sign in with Nostr extension").waitFor();
    const after = await page.evaluate(() => fetch("/admin/api/metrics").then((r) => r.status));
    check(after === 401, `API refused after sign out (${after})`);
    await ctx.close();
  }

  // 3. Local mode: the CLI serves and signs; nothing to sign in to.
  {
    const dash = start(["relay", "dashboard", "--config", cfg, "--no-open", "--json", "--port", String(await freePort())]);
    const link = await new Promise((resolve, reject) => {
      dash.stdout.once("data", (d) => resolve(JSON.parse(String(d).split("\n")[0]).url));
      setTimeout(() => reject(new Error("no dashboard link")), 15000);
    });
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    watch(page, "local");
    await page.addInitScript(cspProbe);
    await page.goto(link);
    await page.locator(".kpis").waitFor();
    await page.waitForTimeout(3000);
    check((await page.locator(".chart canvas").count()) === 18, "local mode renders every chart");
    check((await page.getByRole("button", { name: "Sign out" }).count()) === 0, "local mode has no sign-out");
    const reuse = await (await fetch(link, { redirect: "manual" })).status;
    check(reuse === 401, `the one-time link works once (${reuse})`);
    await ctx.close();
  }

  const signedIn = async (page, what) => {
    await page.locator(".kpis").waitFor({ timeout: 30000 });
    check(await page.getByText(key.pub_hex.slice(0, 8)).isVisible(), `${what}: signed in as the admin`);
    const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
    check(!stored.includes(key.nsec) && !stored.includes(key.priv_hex), `${what}: no key in browser storage`);
  };

  // 4. Secret key: signed in the tab, then dropped.
  {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    watch(page, "nsec");
    await page.addInitScript(cspProbe);
    let leaked = false;
    page.on("request", (r) => {
      const body = r.postData() ?? "";
      if (r.url().includes(key.nsec) || body.includes(key.nsec) || body.includes(key.priv_hex)) leaked = true;
    });
    await page.goto(`${admin}/admin/ui/`);
    await page.getByRole("tab", { name: "Secret key" }).click();
    await page.getByLabel("nsec or hex secret key").fill("nsec1notakey");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    check(await page.getByRole("alert").getByText(/Not a secret key/).isVisible(), "a malformed key is refused in the page");
    await page.getByLabel("nsec or hex secret key").fill(key.nsec);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await signedIn(page, "nsec");
    check(!leaked, "the secret key never went over the network");
    await ctx.close();
  }

  // NIP-46 against ncli's own bunker, on the test relay.
  const relayWS = `ws://127.0.0.1:${port}`;
  ncli(["bunker", "--identity", key.nsec, "--relay", relayWS, "--json", "-q"]);
  let approving = true;
  const approver = (async () => {
    while (approving) {
      try {
        const pending = JSON.parse(ncli(["bunker", "pending", "list", "--json", "-q"]) || "[]");
        for (const p of pending) ncli(["bunker", "pending", "approve", p.id, "--json", "-q"]);
      } catch {
        /* the daemon may still be starting */
      }
      await new Promise((r) => setTimeout(r, 300));
    }
  })();

  // 5. bunker:// URL pasted into the page.
  {
    const bunkerURL = JSON.parse(ncli(["bunker", "connect", "--json", "-q"])).result;
    check(Boolean(bunkerURL), "ncli bunker prints a bunker:// URL");
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    watch(page, "bunker");
    await page.addInitScript(cspProbe);
    await page.goto(`${admin}/admin/ui/`);
    await page.getByRole("tab", { name: "Remote signer" }).click();
    await page.getByLabel("bunker:// URL or NIP-05 name").fill(bunkerURL ?? "");
    await page.getByRole("button", { name: "Connect and sign in" }).click();
    await signedIn(page, "bunker://");
    await ctx.close();
  }

  // 6. nostrconnect:// code the page shows, "scanned" by ncli bunker.
  {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    watch(page, "nostrconnect");
    await page.addInitScript(cspProbe);
    await page.goto(`${admin}/admin/ui/`);
    await page.getByRole("tab", { name: "Remote signer" }).click();
    await page.getByLabel("Scan with signer app").check();
    await page.getByLabel("Relay your signer listens on").fill(relayWS);
    await page.getByRole("button", { name: "Show code" }).click();
    check(await page.getByRole("img", { name: "nostrconnect QR code" }).isVisible(), "a QR code is shown");
    const uri = await page.getByLabel("nostrconnect URI").inputValue();
    check(uri.startsWith("nostrconnect://") && uri.includes(encodeURIComponent(relayWS)), "the code names the chosen relay");
    ncli(["bunker", "connect", uri, "--json", "-q"]);
    await signedIn(page, "nostrconnect://");
    await ctx.close();
  }

  approving = false;
  await approver;
  ncli(["bunker", "stop", "--json", "-q"]);

  await browser.close();
  check(problems.length === 0, "no console errors or CSP violations");
  for (const p of problems) console.log(`     ${p}`);
} catch (e) {
  console.error(e);
  failures++;
} finally {
  for (const c of children) c.kill("SIGINT");
}
process.exit(failures ? 1 : 0);
