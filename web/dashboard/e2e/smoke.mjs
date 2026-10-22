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
      // The zaps index is only maintainable with top-zapped on.
      `cache: {topZapped: {enabled: true}}`,
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
    check(await page.getByText(key.npub.slice(0, 10)).isVisible(), "header shows the signed-in admin as an npub");
    check((await page.getByText(key.pub_hex.slice(0, 8)).count()) === 0, "header never shows the hex pubkey");

    // An index job from the page.
    const zaps = page.locator(".index", { hasText: "zaps" });
    await zaps.getByRole("button", { name: "Rebuild" }).click();
    await zaps.getByText(/last rebuild/).waitFor({ timeout: 15000 });
    check(await zaps.getByText("succeeded").isVisible(), "rebuild from the page succeeds and shows it");
    check(await page.locator(".index", { hasText: "search" }).getByText("cache.search.enabled").isVisible(), "unavailable index says why");

    // Toolbar, rows, panels.
    const search = () => page.evaluate(() => location.search);
    const bodyBg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    await page.getByLabel("Time range").selectOption({ label: "Last 5m" });
    check((await search()).includes("from=now-5m"), "time range picker sets the window");
    const span = await page.evaluate(() => {
      const t = document.querySelector('[data-panel="messages"] .legend');
      return t ? t.textContent : "";
    });
    check(Boolean(span), "legend table renders");

    const latency = page.getByRole("button", { name: /^Latency/ });
    await latency.click();
    check((await page.locator(".chart canvas").count()) === 15 && (await page.getByText("(3 panels)").isVisible()), "a collapsed row unmounts its charts");
    check((await search()).includes("collapsed=latency"), "collapsed rows are in the URL");
    await latency.click();
    check((await page.locator(".chart canvas").count()) === 18, "an expanded row draws its charts again");

    await page.getByLabel("Theme").selectOption("dark");
    const darkBg = await bodyBg();
    await page.getByLabel("Theme").selectOption("light");
    const lightBg = await bodyBg();
    check(darkBg !== lightBg && lightBg === "rgb(244, 245, 245)", `theme toggle switches palettes (${darkBg} / ${lightBg})`);
    await page.getByLabel("Theme").selectOption("auto");

    check((await page.getByRole("button", { name: "Kiosk" }).count()) === 0, "no kiosk button");
    await page.getByLabel("Time range").selectOption({ label: "Last 24h" });
    check((await search()).includes("from=now-24h"), "ranges reach 24h");
    check(await page.locator(".history-since").isVisible(), "a relay up less than the range says where its history starts");

    const messages = page.locator('[data-panel="messages"]');
    await messages.hover();
    await messages.getByRole("button", { name: "Messages menu" }).click();
    await page.getByRole("menuitem", { name: "View" }).click();
    check((await page.locator(".panel.viewing").count()) === 1 && (await search()).includes("view=messages"), "panel View opens full screen");
    await page.keyboard.press("Escape");
    check((await page.locator(".panel.viewing").count()) === 0, "Esc closes the panel view");
    await page.getByLabel("Time range").selectOption({ label: "Last 1h" });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(300);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    check(overflow <= 0, `no horizontal scroll at 390px (overflow ${overflow}px)`);

    await page.emulateMedia({ colorScheme: "dark" });
    await page.waitForTimeout(300);
    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    check(bg !== "rgb(244, 245, 245)", `dark theme applies (body ${bg})`);

    await page.getByRole("button", { name: "Sign out" }).click();
    await page.getByText("Sign in with Nostr extension").waitFor();
    const after = await page.evaluate(() => fetch("/admin/api/metrics").then((r) => r.status));
    check(after === 401, `API refused after sign out (${after})`);
    await ctx.close();
  }

  // 2b. A day of history: each range reaches further back. goroutines is
  // 1000 + minutes ago, so a chart's Max shows how far its window goes.
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await ctx.exposeFunction("__ncliSign", (ev) => {
      const signed = JSON.parse(ncli(["id", "sign", "-e", "-", "-o", "-", "--identity", key.nsec, "-q"], JSON.stringify(ev)));
      return Array.isArray(signed) ? signed[0] : signed;
    });
    await ctx.addInitScript(() => {
      window.nostr = { getPublicKey: async () => "", signEvent: (ev) => window.__ncliSign(ev) };
    });
    await ctx.route("**/admin/api/history", (route) => {
      const now = Date.now();
      const t = [];
      const goroutines = [];
      for (let m = 1440; m >= 1; m--) {
        t.push(now - m * 60000);
        goroutines.push(1000 + m);
      }
      return route.fulfill({ json: { interval_ms: 2000, window_ms: 86400000, t, series: { goroutines } } });
    });
    const page = await ctx.newPage();
    watch(page, "day-of-history");
    await page.goto(`${admin}/admin/ui/`);
    await page.getByRole("button", { name: /Sign in/ }).click();
    await page.locator(".kpis").waitFor();
    const max = async (label) => {
      await page.getByLabel("Time range").selectOption({ label: `Last ${label}` });
      await page.waitForTimeout(300);
      return page.evaluate(() => {
        const row = [...document.querySelectorAll('[data-panel="goroutines"] .legend tbody tr')].find((r) => r.cells[0]?.textContent === "goroutines");
        return row ? row.cells[2].textContent : "";
      });
    };
    const maxes = [];
    for (const label of ["5m", "1h", "6h", "24h"]) maxes.push(await max(label));
    check(new Set(maxes).size === 4, `each range shows a different window (max ${maxes.join(", ")})`);
    check(!(await page.locator(".history-since").isVisible()), "no history note when history covers the range");
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
    check(await page.getByText(key.npub.slice(0, 10)).isVisible(), `${what}: signed in as the admin`);
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

  // 4b. Remember the key, encrypted; unlock it later; survive expiry.
  {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    watch(page, "saved-key");
    await page.addInitScript(cspProbe);
    await page.goto(`${admin}/admin/ui/`);
    await page.getByRole("tab", { name: "Secret key" }).click();
    await page.getByLabel("nsec or hex secret key").fill(key.nsec);
    await page.getByLabel(/Remember on this browser/).check();
    await page.getByLabel("Password", { exact: true }).fill("correct horse battery");
    await page.getByLabel("Repeat password").fill("correct horse battery");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await signedIn(page, "remembered key");
    const stored = await page.evaluate(() => localStorage.getItem("ncli-dashboard:saved-key") ?? "");
    check(stored.includes("ncryptsec1") && !stored.includes(key.nsec) && !stored.includes(key.priv_hex), "only an ncryptsec is stored");

    // Expiry with the key kept in the tab: renewed silently.
    await ctx.clearCookies();
    await page.waitForTimeout(4500);
    check((await page.locator(".kpis").count()) === 1, "an expired session renews while the tab keeps the key");

    // A reload keeps a live session; after signing out the saved key is
    // offered first, behind the password.
    await page.reload();
    await page.locator(".kpis").waitFor();
    check(true, "a reload keeps the session");
    await page.getByRole("button", { name: "Sign out" }).click();
    await page.getByRole("tab", { name: "Saved key" }).waitFor();
    check((await page.getByRole("tab", { name: "Saved key" }).getAttribute("aria-selected")) === "true", "a saved key is offered first");
    await page.getByLabel("Password", { exact: true }).fill("wrong password!");
    await page.getByRole("button", { name: "Unlock and sign in" }).click();
    await page.getByRole("alert").getByText("Wrong password.").waitFor({ timeout: 15000 });
    check(true, "a wrong password is refused");
    await page.getByLabel(/Stay signed in/).uncheck();
    await page.getByLabel("Password", { exact: true }).fill("correct horse battery");
    await page.getByRole("button", { name: "Unlock and sign in" }).click();
    await signedIn(page, "unlocked key");

    // Expiry without a kept key: back to sign-in, not a broken page.
    await ctx.clearCookies();
    await page.getByRole("tab", { name: "Saved key" }).waitFor({ timeout: 15000 });
    check(true, "an expired session without a kept key returns to sign-in");

    page.once("dialog", (d) => void d.accept());
    await page.getByRole("button", { name: "Forget this key" }).click();
    check((await page.getByRole("tab", { name: "Saved key" }).count()) === 0, "forget removes the saved key");
    check((await page.evaluate(() => localStorage.getItem("ncli-dashboard:saved-key"))) === null, "nothing left in storage");
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
    await page.getByRole("tab", { name: "Remote" }).click();
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
    await page.getByRole("tab", { name: "Remote" }).click();
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

  // 7. The relay's public homepage, read from its NIP-11.
  {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    watch(page, "homepage");
    await page.addInitScript(cspProbe);
    await page.goto(`http://127.0.0.1:${port}/`);
    await page.getByRole("status").filter({ hasText: "Online" }).waitFor({ timeout: 10000 });
    check(await page.getByRole("heading", { name: "e2e" }).isVisible(), "homepage shows the relay's name");
    check((await page.getByLabel("Relay address").textContent()) === `ws://127.0.0.1:${port}`, "homepage shows the address to add");
    check((await page.locator(".nips li").count()) > 5, "homepage lists supported NIPs");
    check((await page.getByLabel("Operator npub").textContent())?.startsWith("npub1") ?? false, "homepage shows the operator's npub");
    await page.setViewportSize({ width: 390, height: 844 });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    check(overflow <= 0, `homepage fits a phone (overflow ${overflow}px)`);
    const nip11 = await (await fetch(`http://127.0.0.1:${port}/`, { headers: { Accept: "application/nostr+json" } })).json();
    check(nip11.name === "e2e", "NIP-11 clients still get JSON");
    await ctx.close();
  }
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
