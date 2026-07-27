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
const { generateSecretKey, finalizeEvent } = require('nostr-tools/pure');
const { BunkerSigner, parseBunkerInput } = require('nostr-tools/nip46');

useWebSocketImplementation(WebSocket);

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

    const signed = await signer.signEvent({
      kind: 1,
      created_at: Math.floor(Date.now() / 1000),
      tags: [],
      content: 'nip46-client fixture probe (integration/agent-eval round R6)',
    });

    console.log(JSON.stringify({ ok: true, remote_pubkey: remotePubkey, signed_event: signed }));
  } finally {
    await signer.close();
    pool.close(bp.relays);
  }
}

main().catch((err) => {
  console.error(JSON.stringify({ ok: false, error: String((err && err.message) || err) }));
  process.exit(1);
});
