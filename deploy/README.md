# Deploying an ncli relay

- `docker/` holds the images: `Dockerfile` builds from this checkout
  (dev stack, integration tests), `Dockerfile.release` packages a released
  binary (GoReleaser, the edge image).
- `helm/ncli-relay/` is one chart for every stack, with optional
  Meilisearch and MinIO.
- `stacks/<kind>/` is one kind of relay: `relay.yaml`, `compose.yaml`, and
  (except `dev`) `values.yaml` for the chart.

| stack | what runs |
|---|---|
| `dev` | relay built from this checkout + Meilisearch (Compose only) |
| `personal` | relay alone |
| `public-search` | open relay + Meilisearch: profile search, stats, zap ranking |
| `community` | members-only relay with NIP-29 groups and the HTTP bridge + Meilisearch + MinIO |

Each stack's `relay.yaml` is ready to run, minus secrets. `examples/relay/`
has the full set of single-file configs.

## Secrets

A stack's `relay.yaml` leaves `nip11.privkey`, `nip11.url` and the search
key empty; set them in the environment:

| variable | key |
|---|---|
| `NCLI_NIP11_PRIVKEY` | `nip11.privkey` |
| `NCLI_NIP11_URL` | `nip11.url` |
| `NCLI_CACHE_PROFILES_SEARCH_MEILISEARCH_KEY` | `cache.profiles.search.meilisearch.key` |
| `NCLI_BLOSSOM_STORAGE_S3_ACCESSKEY` | `blossom.storage.s3.accessKey` |
| `NCLI_BLOSSOM_STORAGE_S3_SECRETKEY` | `blossom.storage.s3.secretKey` |

An `NCLI_*` variable only overrides a key present in the file, so keep the
empty placeholders.

## Docker Compose

From the repo root:

```sh
cp deploy/stacks/community/.env.example deploy/stacks/community/.env
# fill in .env
docker compose -f deploy/stacks/community/compose.yaml up -d
```

`dev` needs no `.env`: `docker compose -f deploy/stacks/dev/compose.yaml up`
(or `just dev up`). Its data lands in `deploy/stacks/dev/.dev/`.

## Helm

Create the stack's Secret (the command is in its `values.yaml`), then pass
its values and `relay.yaml`:

```sh
helm install community deploy/helm/ncli-relay \
  -f deploy/stacks/community/values.yaml \
  --set-file relayConfig=deploy/stacks/community/relay.yaml
```

- The relay is a single-replica StatefulSet with its store on a
  PersistentVolume.
- `meilisearch.enabled` and `minio.enabled` each add a StatefulSet; with
  MinIO, a post-install Job creates the bucket.
- `secrets.existingSecret` names a Secret you manage (keys `relay-privkey`,
  `meili-master-key`, `minio-root-user`, `minio-root-password`); otherwise
  the chart creates one from `secrets.*`.
- MinIO images (Compose and chart) are Chainguard's
  `cgr.dev/chainguard/minio` and `minio-client`; MinIO no longer publishes
  to Docker Hub.

## Images

Every image is pinned, and `go test ./deploy/` fails on one that isn't
(it also checks `examples/` and `integration/`):

- The relay is `ghcr.io/ohstr/ncli` at the release it ships with: the
  chart's `appVersion`, the stacks' `NCLI_VERSION` defaults (and
  `.env.example`), `examples/signer/pod.yaml` and the agent-eval rig. On a
  PR the test accepts the CHANGELOG's top heading or the one below it.
- Releasing: in the release PR, set every one of those pins to the
  version being released and bump the chart's `version`. The release
  workflow runs `NCLI_RELEASE_VERSION=<version> go test ./deploy -run
  '^TestRelayImagePinsVersion$'` before tagging and refuses to release
  unless every pin is that version.
- Chainguard tags MinIO only `latest`, so it is pinned by digest. To bump
  it, take the `Digest:` from `docker buildx imagetools inspect
  cgr.dev/chainguard/minio:latest` (and `minio-client`) and put it in
  every copy: the chart's `templates/_helpers.tpl`
  (`ncli-relay.imageDigests`), the community stack's `compose.yaml`, and
  ohstr/nmilat's CI workflow (its S3 tests).
- The chart adds a pinned digest only to that exact default image. For a
  mirror or another tag, set an exact `tag`, or the image's own `digest`.
- `helm upgrade --reuse-values` keeps the values of the earlier install,
  including an older chart's image tags and digests. Upgrade with
  `--reset-then-reuse-values`, or set the images explicitly, to take the
  new chart's defaults.
