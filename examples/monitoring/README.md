# Monitoring an ncli relay

- `prometheus.yml`: scrape config for the relay's admin listener, with a
  bearer token from `ncli relay metrics token add prometheus`.
- `alerts.yml`: relay down, admin credential guessing, reject spikes, slow
  EOSE or OK, a saturated write queue, scan-slot refusals, dropped work,
  failed index jobs.
- `grafana-ncli-relay.json`: import into Grafana and pick your Prometheus
  data source.

The relay side is the `admin:` block in
[`../relay/dashboard-relay.yaml`](../relay/dashboard-relay.yaml).

```sh
# check the rules before loading them
promtool check rules alerts.yml
```
