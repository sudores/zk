# Keeping a notebook in sync with Git

A [notebook](../notes/notebook.md) is a directory of plain text files, which
makes Git an excellent way to keep its history and to share it between your
machines. `zk sync` does the round trip for you:

```sh
$ zk sync
Committing the changes...
[main 9b1f3a2] zk sync: 2024-05-14 09:32:11
 2 files changed, 18 insertions(+)
Pulling from origin/main...
Pushing to origin/main...
```

Git is what `zk sync` uses unless you say otherwise, so there is nothing to
configure to get the above. If you would rather synchronize with an object
store, see [keeping a notebook in sync with S3](s3-sync.md) and the
[`[sync]` section](../config/config-sync.md).

It runs the commands you would run by hand, in this order:

```sh
git add --all
git commit --message <MESSAGE>
git pull --rebase
git push
```

## Setting up the repository

`zk` does not create the repository or invent a remote for you, so do that once
in your notebook:

```sh
$ cd $ZK_NOTEBOOK_DIR
$ git init
$ git remote add origin git@github.com:you/notes.git
```

From then on, `zk sync` is all you need. The first run pushes your branch and
sets it to track the remote, as `git push` would ask you to do yourself.

`zk` shells out to the `git` binary, so your own configuration applies: your
credential helper, your SSH keys, your commit signing and your hooks all behave
exactly as they do on the command line.

## What gets committed

Only the notebook's own files, which means a notebook living in a subdirectory
of a larger repository leaves the rest of it alone.

The search index in `.zk/notebook.db` is never committed. It is rebuilt from
your notes whenever `zk` needs it, and committing a binary file rewritten on
every indexing would turn each sync into a conflict. You may still want to
ignore it, so that it stays out of `git status`:

```sh
$ echo "notebook.db*" >> .zk/.gitignore
```

When the notes have not changed, no commit is made — but the changes made
elsewhere are still pulled in.

## Options

| Flag | |
|---|---|
| `--message`, `-m` | The commit message. Defaults to `zk sync` and the current date. |
| `--no-pull` | Commit and push, without pulling first. |
| `--no-push` | Commit locally, without reaching the remote. |
| `--dry-run`, `-n` | Print the Git commands which would be run, without running them. |
| `--quiet`, `-q` | Say nothing unless something goes wrong. |

`--force` applies to the S3 backend only, and is ignored here.

Committing locally without touching the remote at all is `zk sync --no-pull
--no-push`, which is handy in a notebook you have not published anywhere.

## When a sync conflicts

`zk sync` rebases, so if the same note was edited on two machines, Git stops
mid-rebase and says so. `zk` deliberately does nothing clever about it: resolve
it as you would any other rebase.

```sh
$ zk edit --interactive      # fix the conflict markers
$ git add .
$ git rebase --continue
$ zk sync
```

## Syncing automatically

If you would rather not think about it, run `zk sync --quiet` from your
scheduler. The command is quiet and exits non-zero when something goes wrong,
which is what a cron job wants.

```sh
# Every hour, on the hour.
0 * * * * cd ~/notes && /usr/local/bin/zk sync --quiet
```

Be aware that an automatic sync commits whatever is on disk, half-written notes
included. Many people prefer to keep it a deliberate act — possibly behind a
[command alias](../config/config-alias.md) of their own.
