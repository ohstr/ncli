import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, onSession, setRenewer } from "./api";

afterEach(() => {
  setRenewer(null);
  vi.unstubAllGlobals();
});

describe("session renewal", () => {
  it("renews once on 401 and retries, sharing one sign-in", async () => {
    let renewedTo = "";
    onSession({ expired: () => {}, renewed: (s) => (renewedTo = s.csrf) });
    let calls = 0;
    let logins = 0;
    vi.stubGlobal("location", { origin: "http://admin.test" });
    vi.stubGlobal("fetch", async (url: string) => {
      calls++;
      if (url === "/admin/api/login") {
        logins++;
        return new Response(JSON.stringify({ pubkey: "pk", expires_at: 9, csrf: "fresh" }), { status: 200 });
      }
      // The first two calls find the session expired.
      return calls <= 2 ? new Response("{}", { status: 401 }) : new Response('{"ok":1}', { status: 200 });
    });
    setRenewer(async (ev) => ({ ...ev, id: "i", pubkey: "pk", sig: "s" }));
    const [a, b] = await Promise.all([api.latest(), api.history()]);
    expect(a).toEqual({ ok: 1 });
    expect(b).toEqual({ ok: 1 });
    expect(logins).toBe(1);
    expect(renewedTo).toBe("fresh");
  });

  it("reports expiry when it can't renew", async () => {
    let expired = 0;
    onSession({ expired: () => expired++, renewed: () => {} });
    vi.stubGlobal("fetch", async () => new Response('{"error":"unauthorized"}', { status: 401 }));
    await expect(api.latest()).rejects.toBeInstanceOf(ApiError);
    expect(expired).toBe(1);
  });

  it("reports expiry when renewal is refused", async () => {
    let expired = 0;
    onSession({ expired: () => expired++, renewed: () => {} });
    vi.stubGlobal("location", { origin: "http://admin.test" });
    vi.stubGlobal("fetch", async () => new Response('{"error":"nope"}', { status: 401 }));
    setRenewer(async (ev) => ev);
    await expect(api.latest()).rejects.toBeInstanceOf(ApiError);
    expect(expired).toBe(1);
  });

  it("wipes a kept key when replaced or cleared", () => {
    const released: string[] = [];
    setRenewer(async () => ({}), () => released.push("first"));
    setRenewer(async () => ({}), () => released.push("second"));
    setRenewer(null);
    expect(released).toEqual(["first", "second"]);
  });
});
