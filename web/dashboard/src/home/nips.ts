// Titles of the NIPs a relay may advertise, for people reading the list.
export const NIP_TITLES: Record<string, string> = {
  "1": "Basic protocol",
  "2": "Follow list",
  "4": "Encrypted direct messages (legacy)",
  "5": "Event deletion",
  "9": "Event deletion requests",
  "10": "Text note threads",
  "11": "Relay information",
  "13": "Proof of work",
  "16": "Event treatment",
  "17": "Private direct messages",
  "18": "Reposts",
  "22": "Comments",
  "23": "Long-form content",
  "25": "Reactions",
  "26": "Delegated signing",
  "28": "Public chat",
  "29": "Relay-based groups",
  "33": "Addressable events",
  "40": "Expiration timestamp",
  "42": "Client authentication",
  "43": "Relay membership",
  "44": "Encrypted payloads",
  "45": "Event counts",
  "46": "Remote signing",
  "47": "Wallet connect",
  "48": "Proxy tags",
  "50": "Search",
  "51": "Lists",
  "53": "Live activities",
  "56": "Reporting",
  "57": "Lightning zaps",
  "59": "Gift wrap",
  "62": "Request to vanish",
  "65": "Relay list metadata",
  "70": "Protected events",
  "71": "Video events",
  "77": "Negentropy sync",
  "86": "Relay management API",
  "98": "HTTP auth",
  CW: "Circle wallet query bridge",
};

// HIGHLIGHTS are the capabilities worth calling out on their own, in order.
export const HIGHLIGHTS: { nip: string; label: string; hint: string }[] = [
  { nip: "50", label: "Search", hint: "Find people by name" },
  { nip: "29", label: "Groups", hint: "Communities hosted here" },
  { nip: "57", label: "Zaps", hint: "Lightning tips are counted" },
  { nip: "17", label: "Private messages", hint: "Encrypted DMs" },
  { nip: "53", label: "Live spaces", hint: "Audio rooms and events" },
  { nip: "43", label: "Membership", hint: "Invite codes and roles" },
  { nip: "45", label: "Counts", hint: "Clients can count without downloading" },
  { nip: "77", label: "Fast sync", hint: "Negentropy reconciliation" },
];

export function nipLink(nip: string): string {
  const n = /^\d+$/.test(nip) ? nip.padStart(2, "0") : nip;
  return `https://github.com/nostr-protocol/nips/blob/master/${n}.md`;
}
