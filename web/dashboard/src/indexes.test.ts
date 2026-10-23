import { describe, expect, it } from "vitest";
import type { IndexInfo, Job } from "./api";
import { actions, ago, cancellable, count, ended, eta, meta, note, planText, progressText, tone, versionLabel } from "./indexes";

const T = 1_800_000_000;

const job = (over: Partial<Job> = {}): Job => ({
  id: "ab12",
  index: "zaps",
  kind: "rebuild",
  trigger: "admin",
  state: "running",
  done: 0,
  total: 0,
  applied: 0,
  entries_before: 0,
  entries_after: 0,
  started_at: T - 20,
  duration_ms: 0,
  summary: "",
  ...over,
});

const index = (over: Partial<IndexInfo> = {}): IndexInfo => ({
  name: "zaps",
  enabled: true,
  state: "ready",
  has_data: true,
  entries: 18402,
  behind: 0,
  head: 100,
  ...over,
});

describe("count", () => {
  it("groups digits", () => {
    expect(count(0)).toBe("0");
    expect(count(1204331)).toBe("1 204 331");
    expect(count(-18402)).toBe("-18 402");
  });
});

describe("progressText", () => {
  it("says where a job is", () => {
    expect(progressText(job({ phase: "waiting" }), T)).toBe("waiting for another index job to finish");
    expect(progressText(job({ phase: "cancelling" }), T)).toBe("cancelling…");
    expect(progressText(job({ phase: "waiting for Meilisearch: refused" }), T)).toBe("waiting for Meilisearch: refused");
    expect(progressText(job(), T)).toBe("starting");
    expect(progressText(job({ phase: "scan", done: 25, total: 100 }), T)).toBe("25% · ~1m 0s left");
    expect(progressText(job({ phase: "deletes", done: 3, total: 3, applied: 3 }), T)).toBe("deletes · 100% · 3 applied");
    expect(progressText(job({ done: 300, total: 100 }), T)).toBe("100%");
  });
  it("estimates time left from the pace so far", () => {
    expect(eta(job({ done: 50, total: 100, started_at: T - 30 }), T)).toBe(30);
    expect(eta(job({ done: 0, total: 100 }), T)).toBeNull();
  });
});

describe("what an index offers", () => {
  it("rebuilds an index in use, drops only a disabled one holding data", () => {
    expect(actions(index())).toEqual({ rebuild: true, drop: false });
    expect(actions(index({ enabled: false, state: "disabled" }))).toEqual({ rebuild: false, drop: true });
    expect(actions(index({ enabled: false, state: "disabled", has_data: false }))).toEqual({ rebuild: false, drop: false });
    expect(actions(index({ enabled: false, state: "disabled", has_data: false, retired: 1 }))).toEqual({ rebuild: false, drop: true });
    expect(actions(index({ running: job() }))).toEqual({ rebuild: false, drop: false });
  });
  it("can't cancel live search catching up", () => {
    expect(cancellable(job())).toBe(true);
    expect(cancellable(job({ index: "search", kind: "catch_up" }))).toBe(false);
  });
});

describe("what an index says", () => {
  it("shows its numbers", () => {
    expect(meta(index({ behind: 1234, built_at: T - 3 * 3600 }), T)).toBe("18 402 entries · 1 234 events behind · built 3h ago");
    expect(meta(index({ name: "search", queued: 5 }), T)).toBe("18 402 entries · 5 profiles queued");
    expect(meta(index({ enabled: false, has_data: false, entries: 0, state: "disabled" }), T)).toBe("");
  });
  it("explains what isn't simply ready", () => {
    expect(note(index(), T)).toBe("");
    expect(note(index({ state: "behind", action: "catch_up", reason: "12 event(s) stored since it was last current" }), T)).toBe(
      "catch-up needed (12 event(s) stored since it was last current); the relay starts it on its own",
    );
    expect(note(index({ state: "needs_rebuild", action: "rebuild", reason: "never built", retry_at: T + 300 }), T)).toBe(
      "rebuild needed (never built); the last try failed, retrying in 5m",
    );
    expect(note(index({ state: "unreachable", reason: "Meilisearch can't be reached", error: "refused" }), T)).toBe("Meilisearch can't be reached (refused)");
    expect(note(index({ enabled: false, state: "disabled", unavailable: "zap ranking is off" }), T)).toBe("zap ranking is off. It still holds data you can drop.");
  });
  it("colours states", () => {
    expect(tone("ready")).toBe("ok");
    expect(tone("catching_up")).toBe("warn");
    expect(tone("unreachable")).toBe("bad");
    expect(tone("disabled")).toBe("muted");
  });
  it("tells past and future times", () => {
    expect(ago(T - 2, T)).toBe("just now");
    expect(ago(T - 180, T)).toBe("3m ago");
    expect(ago(T + 300, T)).toBe("in 5m");
    expect(ago(undefined, T)).toBe("–");
  });
});

describe("planText", () => {
  it("says what will happen before it does", () => {
    expect(planText({ index: "zaps", kind: "rebuild", state: "ready", entries: 18402, scan: 1204331, effect: "builds a new index beside the live one" })).toBe(
      "Rebuild zaps from 1 204 331 stored events; it holds 18 402 entries now. It builds a new index beside the live one.",
    );
    expect(planText({ index: "stats", kind: "drop", state: "disabled", entries: 3, scan: 0, effect: "removes the 3 entries it holds" })).toBe(
      "Drop 3 entries from stats: removes the 3 entries it holds.",
    );
  });
});

describe("ended", () => {
  it("finds the jobs that just finished, for a notice", () => {
    const running = job({ id: "j1" });
    const before = [index({ running })];
    expect(ended(before, [index({ running })])).toEqual([]);
    const done = job({ id: "j1", state: "succeeded", summary: "zaps: rebuilt" });
    expect(ended(before, [index({ last: done })])).toEqual([done]);
    // Not one that ended before the page saw it run.
    expect(ended([], [index({ last: done })])).toEqual([]);
  });
});

describe("versionLabel", () => {
  it("names the relay's build and flags a different CLI", () => {
    expect(versionLabel({})).toEqual({ label: "", mismatch: "" });
    expect(versionLabel({ version: { ncli: "0.8.0", nmilat: "v0.5.0" } })).toEqual({ label: "ncli 0.8.0 · nmilat v0.5.0", mismatch: "" });
    expect(versionLabel({ version: { ncli: "0.8.0", nmilat: "v0.5.0" }, client: { ncli: "0.8.0", nmilat: "v0.5.0" } }).mismatch).toBe("");
    expect(versionLabel({ version: { ncli: "0.8.0", nmilat: "v0.5.0" }, client: { ncli: "0.7.9", nmilat: "v0.4.0" } }).mismatch).toBe(
      "this CLI is ncli 0.7.9, the relay runs 0.8.0",
    );
  });
});
