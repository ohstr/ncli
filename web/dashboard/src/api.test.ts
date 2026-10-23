import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, login, setCsrf } from "./api";

type Call = { url: string; init: RequestInit };

function stubFetch(status: number, body: unknown) {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    return new Response(typeof body === "string" ? body : JSON.stringify(body), { status });
  });
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("login", () => {
  it("signs one NIP-98 event for this origin's login URL", async () => {
    let signed: { kind: number; tags: string[][]; content: string } | undefined;
    vi.stubGlobal("location", { origin: "https://relay.example:5501" });
    vi.stubGlobal("window", {
      nostr: {
        getPublicKey: async () => "pk",
        signEvent: async (ev: typeof signed) => {
          signed = ev;
          return { ...ev, id: "id", pubkey: "pk", sig: "sig" };
        },
      },
    });
    const calls = stubFetch(200, { pubkey: "pk", expires_at: 1, csrf: "c" });

    const s = await login();
    expect(s.csrf).toBe("c");
    expect(signed!.kind).toBe(27235);
    const tag = (n: string) => signed!.tags.find((t) => t[0] === n)?.[1];
    expect(tag("u")).toBe("https://relay.example:5501/admin/api/login");
    expect(tag("method")).toBe("POST");
    // sha256 of the empty body: the listener requires the payload tag.
    expect(tag("payload")).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    expect(tag("nonce")).toMatch(/^[0-9a-f]{32}$/);

    expect(calls[0].url).toBe("/admin/api/login");
    expect(calls[0].init.method).toBe("POST");
    const auth = (calls[0].init.headers as Record<string, string>).Authorization;
    expect(auth.startsWith("Nostr ")).toBe(true);
    expect(JSON.parse(atob(auth.slice(6))).sig).toBe("sig");
  });

  it("explains a missing signer", async () => {
    vi.stubGlobal("window", {});
    await expect(login()).rejects.toThrow(/NIP-07/);
  });
});

describe("api", () => {
  it("sends the CSRF token on writes, not reads", async () => {
    setCsrf("token-1");
    const calls = stubFetch(200, { id: "j" });
    await api.plan("search", "rebuild");
    await api.latest();
    const w = calls[0].init.headers as Record<string, string>;
    expect(w["X-CSRF-Token"]).toBe("token-1");
    expect(calls[0].url).toBe("/admin/api/indexes/search/rebuild");
    expect(JSON.parse(calls[0].init.body as string)).toEqual({ dry_run: true });
    expect((calls[1].init.headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();
    expect(calls[0].init.credentials).toBe("same-origin");
  });

  it("surfaces the server's error message with its status", async () => {
    stubFetch(409, { error: "another job is running on this index", job: { id: "ab12", kind: "catch_up" } });
    const err = (await api.start("zaps", "drop").catch((e: ApiError) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.message).toBe("another job is running on this index");
    expect(err.job?.id).toBe("ab12");
  });

  it("escapes index names in paths", async () => {
    const calls = stubFetch(200, {});
    await api.cancel("../x");
    expect(calls[0].url).toBe("/admin/api/jobs/..%2Fx");
  });
});

describe("api.jobs", () => {
  it("lists one index's jobs, never null", async () => {
    const calls = stubFetch(200, { jobs: null });
    expect(await api.jobs("a b")).toEqual([]);
    expect(calls[0].url).toBe("/admin/api/jobs?index=a%20b");
  });
});
