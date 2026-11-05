# Deploying an ncli relay

- `docker/` holds the images: `Dockerfile` builds from this checkout
  (dev stack, integration tests), `Dockerfile.release` packages a released
  binary (GoReleaser, the edge image).
- `helm/ncli-relay/` is one chart for every kind of relay, with optional
  Meilisearch and MinIO.
- `stacks/<kind>/` is one kind of relay: its `relay.yaml`, a `compose.yaml`,
  and (except `dev`) a `values.yaml` for the chart.

| stack | what runs |
|---|---|
| `dev` | relay built from this checkout + Meilisearch (Compose only) |
| `personal` | relay alone |
| `public-search` | open relay + Meilisearch: profile search, stats, zap ranking |
| `community` | members-only relay with NIP-29 groups and the HTTP bridge + Meilisearch + MinIO |

`examples/relay/` keeps the full catalogue of single-file relay configs;
each stack's `relay.yaml` is a ready-to-run one, with secrets left out.

## Secrets

A stack's `relay.yaml` leaves `nip11.privkey`, `nip11.url` and the search
key empty. They come from the environment, which overrides any key present
in the file:

| variable | key |
|---|---|
| `NCLI_NIP11_PRIVKEY` | `nip11.privkey` |
| `NCLI_NIP11_URL` | `nip11.url` |
| `NCLI_CACHE_PROFILES_SEARCH_MEILISEARCH_KEY` | `cache.profiles.search.meilisearch.key` |

An `NCLI_*` variable only overrides a key the file already has, so keep the
empty placeholder in `relay.yaml`.

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

Create the stack's Secret (its `values.yaml` shows the command), then pass
the stack's values and its `relay.yaml`:

```sh
helm install community deploy/helm/ncli-relay \
  -f deploy/stacks/community/values.yaml \
  --set-file relayConfig=deploy/stacks/community/relay.yaml
```

The relay runs as a single-replica StatefulSet with its store on a
PersistentVolume. `meilisearch.enabled` and `minio.enabled` add each as its
own StatefulSet; with MinIO, a post-install Job creates the bucket.
`secrets.existingSecret` names a Secret you manage (keys `relay-privkey`,
`meili-master-key`, `minio-root-user`, `minio-root-password`); otherwise the
chart creates one from `secrets.*`.

MinIO no longer publishes images on Docker Hub; both the Compose stack and
the chart use Chainguard's `cgr.dev/chainguard/minio` and `minio-client`.
