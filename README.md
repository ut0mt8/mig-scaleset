# mig-scaler

Autoscaler for GitHub Actions runner scale sets on a GCP managed
instance group. Built on top of `github.com/actions/scaleset`.

One runner is one VM. The scaler registers each runner on GitHub (JIT) and
creates its VM with the single-use config in the `jit-config` metadata key.
When the job is done, the VM is deleted.

## Build

Go 1.27 or newer. Source: https://github.com/ut0mt8/mig-scaleset

```bash
make tidy     # first time: resolves deps and writes go.sum
make build    # bin/mig-scaler
make dist     # dist/mig-scaler-VERSION-OS-ARCH.tar.gz (GOOS/GOARCH to cross-build)
make docker-build IMAGE=us-central1-docker.pkg.dev/PROJECT/REPO/mig-scaler
```

Commit `go.sum`, the Docker build needs it.

## GCP setup

1. Instance template: the runner image (`shr-image-*`), the runner
   service account, and a `startup-script`.
2. Regional MIG at size 0, no autoscaler, no autohealing, redistribution off:

   ```bash
   gcloud compute instance-groups managed create gh-runners \
     --region us-central1 --template TEMPLATE --size 0 \
     --instance-redistribution-type NONE --target-distribution-shape ANY
   ```

3. The scaler's service account needs `roles/compute.instanceAdmin.v1` and
   `roles/iam.serviceAccountUser` on the runner service account. On GCE, the
   VM running the scaler needs the `cloud-platform` scope.

## Run

```bash
export GITHUB_TOKEN=ghp_...
mig-scaler \
  --url https://github.com/org-hq --name gcp-runners \
  --project self-hosted-runners --region us-central1 --mig gh-runners \
  --max-runners 10 --min-runners 1
```

Workflows use `runs-on: gcp-runners`. `mig-scaler --help` lists all flags.
GitHub App auth: `--app-id` (the App ID or its client ID),
`--app-installation-id`, and `--app-private-key` or `--app-private-key-file`.
The key can be PEM or base64 encoded PEM. The usual `GITHUB_TOKEN`,
`GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID` and `GITHUB_APP_PRIVATE_KEY`
env vars are read when the `MIG_SCALER_` ones are not set.

## Several instances

One process can run several instances. An instance is one GitHub URL (org or
repo) with one scale set and one MIG, with its own GitHub session and
reconcile loop. Only the API, the dashboard (one tab per instance) and the
metrics are shared. A failing instance restarts on its own, with backoff,
without touching the others.

Describe them in a TOML file and pass `--config` (or `MIG_SCALER_CONFIG`):

```toml
# Optional, for every instance. The flags and env vars are the defaults
# underneath, so shared settings can stay there too.
[defaults]
project = "self-hosted-runners"
region = "us-central1"
max_runners = 10

[[instances]]
name = "default"
url = "https://github.com/org-hq"
scale_set = "gcp-runners"
mig = "gh-runners"
labels = ["linux", "e2-medium"]

[[instances]]
name = "large"
url = "https://github.com/org-hq/repo"   # a single repo
scale_set = "gcp-runners-large"
mig = "gh-runners-large"
max_runners = 4
idle_timeout = "15m"
```

Durations are strings (`"90s"`, `"15m"`, `"7h"`). Unknown keys are refused,
so a typo fails at start instead of being ignored. `config.example.toml` is
a starting point.

Fields: `name`, `url`, `scale_set`, `labels`, `runner_group`, `project`,
`region`, `mig`, `runner_prefix`, `work_folder`, `min_runners`,
`max_runners`, `reconcile_interval`, `boot_timeout`, `idle_timeout`,
`max_runner_age`, and the credential overrides `app_installation_id` and
`github_token_env` (name of an env var holding a PAT for this instance).
Credentials are otherwise shared, from the usual flags.

## Labels

The scale set name is always a label. Add more with `--labels` (or
`MIG_SCALER_LABELS`), comma separated:

```bash
MIG_SCALER_LABELS=linux,x64,e2-medium
```

A workflow matches when it asks for a subset of them, for example
`runs-on: [gcp-runners, e2-medium]` or just `runs-on: gcp-runners`. On start,
the scaler updates the labels of an existing scale set when they changed.

## Deleting a stale scale set

`scaleset-delete` (built with `make tools`) removes a scale set from GitHub,
for example a test one. It uses the same auth flags and env vars as the
scaler.

```bash
make tools
./bin/scaleset-delete --url https://github.com/org-hq --name test        # show only
./bin/scaleset-delete --url https://github.com/org-hq --name test --yes  # delete
```

It refuses a scale set with assigned jobs or busy runners unless `--force`.
Stop the scaler using that scale set first, and delete its VMs from the MIG.

## Configuration

Every flag can be set with an env var: `MIG_SCALER_` + the flag name in
upper case, dashes as underscores. `--max-runners` is `MIG_SCALER_MAX_RUNNERS`.
Command line flags win over env vars, env vars over flag defaults. Settings
are read with viper; an invalid value (`MIG_SCALER_MAX_RUNNERS=abc`) stops the
scaler at start with a message naming the variable. `mig-scaler --help` shows each name.

For GitHub App auth from a secret, use `MIG_SCALER_APP_PRIVATE_KEY` (PEM or
base64 PEM) instead of the file flag.

Durations are Go durations everywhere: `--boot-timeout 5m`,
`MIG_SCALER_RECONCILE_INTERVAL=1m`, or `idle_timeout = "15m"` in the config
file. At start the scaler refuses a `reconcile-interval` under 10s, a
non-positive timeout, or a `max-runner-age` not above `idle-timeout`.

## API and UI

The scaler serves a dashboard and a JSON API on `--listen-addr` (default
`:8080`, or `:$PORT` when `PORT` is set). Empty disables it.

| Endpoint | Content |
| --- | --- |
| `GET /` | dashboard, one tab per instance, refreshes every 5s |
| `GET /api/v1/instances` | every instance: state, last error, restarts, queue summary |
| `GET /api/v1/instances/{name}/status` | job queue, runner counts, GitHub view, last reconcile |
| `GET /api/v1/instances/{name}/runners` | tracked runners: state, job, age |
| `GET /api/v1/instances/{name}/events?limit=50` | recent activity, newest first (last 200 kept) |
| `GET /healthz` | liveness |
| `GET /metrics` | Prometheus metrics |

The API answers 503 until the scaler is ready. There's no auth: keep it
private (IAM on Cloud Run, or a private network).

## Prometheus metrics

`/metrics` exposes the same numbers as the status page, read at scrape time,
plus counters since start. All names start with `mig_scaler_`, and every
metric has an `instance` label:

| Metric | Type | Labels |
| --- | --- | --- |
| `info` | gauge, always 1 | `version`, `url`, `scale_set`, `scale_set_id`, `mig`, `region` |
| `ready` | gauge, 1 while running | |
| `instance_restarts_total` | counter | |
| `jobs` | gauge | `state`: waiting, available, acquired, assigned, running |
| `runners` | gauge | `state`: pending, idle, busy |
| `runners_target`, `runners_min`, `runners_max` | gauge | |
| `github_runners` | gauge | `state`: registered, busy, idle, offline |
| `github_stats_age_seconds` | gauge | |
| `reconcile_last_timestamp_seconds`, `reconcile_last_duration_seconds` | gauge | |
| `reconcile_vms` | gauge | `kind`: in_mig, pending, foreign |
| `jobs_started_total` | counter | |
| `jobs_completed_total` | counter | `result` |
| `runners_registered_total` | counter | |
| `runners_deregistered_total` | counter | `reason` |
| `vms_deleted_total` | counter | `reason` |
| `vms_adopted_total`, `scale_ups_total`, `scale_downs_total` | counter | |
| `errors_total`, `reconcile_passes_total` | counter | |

Plus the standard `go_*` and `process_*` metrics. Some useful queries:

```promql
mig_scaler_jobs{state="waiting"}                                  # queue
sum(mig_scaler_runners) / mig_scaler_runners_max                  # fleet usage
rate(mig_scaler_jobs_completed_total{result="failed"}[1h])        # failures
time() - mig_scaler_reconcile_last_timestamp_seconds > 600        # stuck reconciler
mig_scaler_github_runners{state="offline"} > 0                    # runners not connecting
```

## Logs

JSON by default (`--log-format console` for humans). Useful messages:
`message stats` (GitHub statistics on each message), `runner registered`,
`runner deregistered`, `reconcile: nothing to do` / `reconcile: actions taken`,
`MIG error for runner VM`.

## Limits

- State lives in memory; adopted VMs start their idle and age clocks at adoption.
- Zone and capacity errors are retried by GCP, the runner is dropped after
  `--boot-timeout`.
