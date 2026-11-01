# Keeping a notebook in sync with S3

[`zk sync`](git-sync.md) can keep a [notebook](../notes/notebook.md) in step
with an S3 bucket instead of a Git remote — handy if you would rather not have
a repository in your notes directory, or if the other end is a phone, a tablet
or a machine where Git is awkward.

```sh
$ zk sync
Comparing 412 local files with s3://my-notes/notebook/...
  download      journal/2026-10-28.md (new on the remote)
  upload        inbox/idea.md (changed here)
Synchronized with s3://my-notes/notebook/: 1 downloaded, 1 uploaded, 0 deleted here, 0 deleted there, 0 conflicts.
```

## Setting it up

Tell the notebook which backend to use, and which bucket:

```toml
# .zk/config.toml
[sync]
backend = "s3"

[sync.s3]
bucket = "my-notes"
prefix = "notebook"
```

Credentials are not part of it. `zk` reads the standard AWS credential chain,
so whatever already works for the AWS CLI works here:

```sh
$ export AWS_PROFILE=personal
$ zk sync
```

`zk` does not create the bucket for you. See
[the configuration reference](../config/config-sync.md) for the full list of
settings, including `endpoint-url` and `path-style` for MinIO, Cloudflare R2
and Backblaze B2.

On a second machine, create the notebook and give it the same `[sync]` block
before the first synchronization:

```sh
$ zk init notes
$ cd notes
$ $EDITOR .zk/config.toml     # paste the [sync] and [sync.s3] sections
$ zk sync
```

## How it differs from Git

Git merges; S3 does not. An object store has no history and no notion of a
common ancestor, so `zk` keeps its own record of what each note looked like at
the end of the last synchronization, in `.zk/sync-state.json`.

That record is what makes the command a synchronization rather than a copy. It
is how `zk` tells a note *you* changed from one changed on another machine,
how a deletion travels instead of being undone by the next run, and how a note
edited in two places is noticed rather than silently overwritten.

The file describes one machine and is never synchronized itself. Losing it is
not a problem: with no record to go by, `zk` merges the two sides and deletes
nothing, which is also what the very first synchronization does.

## When a sync conflicts

If the same note was edited on two machines, both versions are kept. Yours
stays under its own name and theirs is written beside it:

```sh
$ zk sync
  conflict      meeting.md (their version kept in meeting.conflict.md)
Synchronized with s3://my-notes/notebook/: 0 downloaded, 2 uploaded, 0 deleted here, 0 deleted there, 1 conflicts.
```

`zk` does nothing clever about the contents — there are no conflict markers and
no merge. You get two notes and decide what to do with them, and since the
conflict copy is an ordinary note it shows up in `zk list` and opens with
`zk edit` like any other. Both files are synchronized too, so your other
machines end up with the same pair rather than each keeping its own version.

One case `zk` cannot tidy up: if two machines notice the same conflict at the
same moment, you get a `.conflict.md` and a `.conflict.2.md`. There is no lock
on a bucket, so this is the honest outcome rather than a bug.

## Deletions

A note you delete is deleted from the bucket on the next synchronization, and
disappears from your other machines after theirs. Because that is the one
operation which destroys something, it has a limit: a synchronization which
would delete more than ten files stops and says so.

```sh
$ zk sync
zk: error: refusing to synchronize: it would delete 48 files, more than the 10
allowed by sync.s3.max-deletes - check that this is what you want and run again
with --force, or raise the limit
```

Raise it with `max-deletes` under `[sync.s3]`, or pass `--force` once you have
looked. `zk sync --dry-run` is the way to look.

## What gets synchronized

Everything in the notebook, except:

* `.zk/notebook.db` and its companions, as the search index is rebuilt from the
  notes and would conflict on every run;
* `.zk/sync-state.json`, which belongs to this machine;
* `.git/`, so a notebook can be both a repository and synchronized to a bucket;
* whatever you add to `exclude` under `[sync]`.

```toml
[sync]
backend = "s3"
exclude = ["attachments/**", "*.tmp"]
```

`.zk/config.toml` is synchronized, so your settings follow your notes.

## Options

| Flag | |
|---|---|
| `--dry-run`, `-n` | Print what would be done, without doing any of it. |
| `--no-pull` | Send your changes out, without bringing anything in. |
| `--no-push` | Bring changes in, without sending anything out. |
| `--force` | Carry on past the limit on deletions. |
| `--quiet`, `-q` | Say nothing unless something goes wrong. |

`--message` belongs to the Git backend and is ignored here, with a note saying
so.

The running commentary goes to standard error and `--quiet` silences it. A dry
run's plan is the result you asked for rather than commentary, so it goes to
standard output and survives `--quiet` — which is what makes `zk sync -n -q`
worth piping somewhere.

A synchronization which is cut short — by an error, or by Ctrl-C — records
whatever did go through, so the next one picks up the rest rather than starting
again.

## Syncing automatically

As with Git, `zk sync --quiet` is made for a scheduler: it is silent and exits
non-zero when something goes wrong.

```sh
# Every hour, on the hour.
0 * * * * cd ~/notes && /usr/local/bin/zk sync --quiet
```

Be aware that an automatic synchronization uploads whatever is on disk,
half-written notes included, and that two machines syncing on the same schedule
are the likeliest way to produce a conflict copy.
