// npubEncode spells a hex pubkey as a NIP-19 npub (bech32), so the homepage
// needs no Nostr library.
const CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l";

function polymod(values: number[]): number {
  const gen = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];
  let chk = 1;
  for (const v of values) {
    const top = chk >>> 25;
    chk = ((chk & 0x1ffffff) << 5) ^ v;
    for (let i = 0; i < 5; i++) if ((top >>> i) & 1) chk ^= gen[i];
  }
  return chk;
}

function expand(hrp: string): number[] {
  const out: number[] = [];
  for (const c of hrp) out.push(c.charCodeAt(0) >> 5);
  out.push(0);
  for (const c of hrp) out.push(c.charCodeAt(0) & 31);
  return out;
}

function toWords(bytes: number[]): number[] {
  let acc = 0;
  let bits = 0;
  const out: number[] = [];
  for (const b of bytes) {
    acc = (acc << 8) | b;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      out.push((acc >> bits) & 31);
    }
  }
  if (bits > 0) out.push((acc << (5 - bits)) & 31);
  return out;
}

export function npubEncode(hex: string): string {
  if (!/^[0-9a-f]{64}$/i.test(hex)) return "";
  const bytes = hex.match(/../g)!.map((h) => parseInt(h, 16));
  const words = toWords(bytes);
  const mod = polymod([...expand("npub"), ...words, 0, 0, 0, 0, 0, 0]) ^ 1;
  const check = Array.from({ length: 6 }, (_, i) => (mod >> (5 * (5 - i))) & 31);
  return "npub1" + [...words, ...check].map((w) => CHARSET[w]).join("");
}
