# Synchronization

The `[sync]` section says how [`zk sync`](../tips/git-sync.md) keeps a
[notebook](../notes/notebook.md) in step with somewhere else.

```toml
[sync]
backend = "git"
```

| Setting | Default | |
|---|---|---|
| `backend` | `"git"` | `"git"` to synchronize through the notebook's Git repository, or `"s3"` to reconcile it with an S3 bucket. |
| `exclude` | `[]` | Path globs kept out of the synchronization, in the same syntax as [`note.exclude`](config-note.md). Only the `s3` backend honours it. |

Saying nothing at all keeps Git, so a notebook written before this section
existed behaves exactly as it did.

## Git

The [Git backend](../tips/git-sync.md) needs nothing here. Make the notebook a
repository once, with `git init` and `git remote add`, and `zk sync` does the
round trip from then on.

## S3

The [S3 backend](../tips/s3-sync.md) is configured under `[sync.s3]`, and
`bucket` is the only setting it insists on.

```toml
[sync]
backend = "s3"

[sync.s3]
bucket = "my-notes"
prefix = "notebook"
region = "eu-central-1"
```

| Setting | Default | |
|---|---|---|
| `bucket` | — | The bucket's name. Required. |
| `prefix` | none | The key prefix the notebook lives under, so that one bucket can hold several notebooks side by side. |
| `region` | from the environment | The bucket's region, when your AWS configuration does not already say. |
| `profile` | from the environment | An AWS profile to use. See the note below before setting it. |
| `endpoint-url` | AWS | An S3-compatible service other than AWS, such as MinIO, Cloudflare R2 or Backblaze B2. |
| `path-style` | `false` | Address the bucket as a path rather than as a subdomain, which MinIO and some other services need. |
| `max-deletes` | `10` | Stop and ask for `--force` when a synchronization would delete more files than this. `0` removes the limit. |

### Credentials

There are deliberately no credential settings. `zk` reads the standard AWS
credential chain — the environment, `~/.aws/credentials`, a named profile, SSO,
an instance role — so your existing setup applies as it does to the AWS CLI,
and nothing secret is written to a file which is itself synchronized.

### Settings which should not travel

A notebook's `.zk/config.toml` goes to your other machines along with the
notes, which is what you want for `bucket`, `prefix` and `region` — but not for
everything.

`profile` is a name that may mean nothing on another machine, and an
`endpoint-url` of `http://192.168.1.10:9000` is right on the desktop next to
the server and wrong on the laptop. Keep those two, and `path-style` with them,
out of the notebook: use the `AWS_PROFILE` environment variable, or your global
`~/.config/zk/config.toml`, which notebook configurations inherit from and which
stays on the machine it belongs to.

```toml
# ~/.config/zk/config.toml - this machine only
[sync.s3]
profile = "personal"
endpoint-url = "http://192.168.1.10:9000"
path-style = true
```

### S3-compatible services

Setting `endpoint-url` also tells `zk` not to send the request checksums the
AWS SDK adds by default. AWS accepts them; several S3-compatible services
reject them outright or store the chunked framing as though it were the note.
You should not have to do anything about it, but it is worth knowing if you
have met that problem with other tools.

### What never travels

Whatever you exclude, two things are always kept out of the synchronization:

* `.zk/notebook.db` and its companions. The search index is rebuilt from your
  notes, and a binary file rewritten on every indexing would conflict every
  time — the same reason the Git backend keeps it out of its commits.
* `.zk/sync-state.json`, which records what this machine last saw and would be
  wrong on any other.

`.zk/config.toml` *is* synchronized, as it is under Git, so your settings
follow your notes.
