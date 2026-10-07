#!/usr/bin/env node
// NIP-46 counterparty fixture for round R6 ("bunker"). ncli bunker is only
// ever the *signer* side of NIP-46 -- something has to play the "other app"
// for a pairing to actually complete. This is that something.
//
// This is harness-side test infrastructure, not part of ncli and not part
// of what the agent-under-test is asked to do: the R6 round prompt has the
// agent produce a bunker:// URI and then just watch for a session to show
// up; bin/run.sh is what actually invokes this file against that URI while
// the round is still running.
//
// CommonJS on purpose -- Node's ESM resolver ignores NODE_PATH, and
// dependencies here are baked into the agent image at a fixed path (see
// agent/Dockerfile) rather than living inside the /fixtures bind mount, so
// pulling this file into a running container doesn't also require an
// `npm install` there.
'use strict';

const { useWebSocketImplementation, SimplePool } = require('nostr-tools/pool');
const { generateSecretKey, getPublicKey } = require('nostr-tools/pure');
const { BunkerSigner, parseBunkerInput } = require('nostr-tools/nip46');

useWebSocketImplementation(WebSocket);

// How long each out-of-grant request waits for the agent's decision.
const DECISION_TIMEOUT_MS = 150000;

function withTimeout(promise, ms) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`no decision within ${ms / 1000}s`)), ms);
  });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

async function main() {
  const bunkerURI = process.argv[2];
  if (!bunkerURI) {
    console.error(JSON.stringify({ ok: false, error: 'usage: nip46-client.cjs <bunker:// URI>' }));
    process.exit(2);
  }

  const bp = await parseBunkerInput(bunkerURI);
  if (!bp) {
    console.error(JSON.stringify({ ok: false, error: `could not parse bunker URI: ${bunkerURI}` }));
    process.exit(1);
  }

  const clientKey = generateSecretKey();
  const pool = new SimplePool();
  const signer = BunkerSigner.fromBunker(clientKey, bp, { pool });

  try {
    await signer.connect();
    const remotePubkey = await signer.getPublicKey();

    const sign = (kind, content) => signer.signEvent({
      kind,
      created_at: Math.floor(Date.now() / 1000),
      tags: [],
      content,
    });
    // Covered by the agent's --grants spec: answered with no decision.
    const signed = await sign(1, 'nip46-client fixture probe (integration/agent-eval round R6)');

    // Not covered: these wait in `ncli bunker pending` until the agent
    // approves the kind 7 and rejects the kind 30023.
    const decided = async (kind, content) => {
      try {
        const ev = await withTimeout(sign(kind, content), DECISION_TIMEOUT_MS);
        return { signed_event: ev };
      } catch (err) {
        return { error: String((err && err.message) || err) };
      }
    };
    const approved = await decided(7, '+');
    const rejected = await decided(30023, 'an article the agent should reject');

    console.log(JSON.stringify({
      ok: true, client_pubkey: getPublicKey(clientKey), remote_pubkey: remotePubkey,
      signed_event: signed, approve_kind7: approved, reject_kind30023: rejected,
    }));
  } finally {
    await signer.close();
    pool.close(bp.relays);
  }
}

main().catch((err) => {
  console.error(JSON.stringify({ ok: false, error: String((err && err.message) || err) }));
  process.exit(1);
});
