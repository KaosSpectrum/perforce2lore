# perforce2lore

Imports Perforce history into a Lore repository, one revision per changelist, credited to the person who wrote it. It imports a single depot path, a stream and the streams derived from it, or a set of classic depot branches, each into its own Lore branch.

It can import a whole history in one go, then keep running and mirror new changelists as they're submitted. That lets a team move to Lore gradually while it keeps working in Perforce.

## Features

- One Lore revision per changelist, in order, with the description as the message.
- Streams: import a mainline plus any derived streams, chosen by name or by pattern. Each stream becomes a Lore branch, created from its parent at the point the stream was made.
- Classic depots: import branch paths such as `//depot/game/main` and `//depot/game/release-1.0` the same way, naming each one's parent.
- Each revision is credited to its author through a simple authors file. Authors are matched by Perforce user, email or full name.
- The changelist number, Perforce user, workspace, submit time and fixed jobs are stored as revision metadata.
- `#tag value` lines in descriptions can be kept, copied to metadata, or moved to metadata.
- Deletes, moves and folders emptied by deletes are mirrored.
- The import resumes safely after a crash or a failed commit, and a lock stops two imports running at once.
- Watch mode polls for new changelists and retries after a halt.
- `-check` and `-dry-run` let you validate the setup and the author mapping before importing anything.

## Requirements

- The `p4` command-line client, logged in (`p4 login`) as an account that can read the depot path.
- The `lore` command-line client, signed in as the account that commits the import. `LORE_IDENTITY_TOKEN` in the environment also works.
- A Perforce workspace for the import, whose root is the Lore working copy. Nothing else should write to that folder.

## Setup

1. **Create the Lore repository** and clone it to the folder the import will use, for example `/srv/lore-import/game`.
2. **Create a Perforce workspace** with that folder as its root and a view of the depot path. Turn on the `rmdir` option so that deletes remove empty folders:

   ```
   p4 client -o lore-import-main | sed 's/normdir/rmdir/' | p4 client -i
   ```

3. **Copy the examples.** Copy one settings file, for a single path ([`perforce2lore.json`](examples/perforce2lore.json)), streams ([`perforce2lore-streams.json`](examples/perforce2lore-streams.json)) or classic branches ([`perforce2lore-paths.json`](examples/perforce2lore-paths.json)), plus [`authors.json`](examples/authors.json), next to the binary. Then fill them in.
4. **Check the setup:**

   ```
   perforce2lore -config perforce2lore.json -check
   ```

5. **Preview the import.** This lists every pending changelist with its author and every authors file entry still missing:

   ```
   perforce2lore -config perforce2lore.json -dry-run
   ```

6. **Import:**

   ```
   perforce2lore -config perforce2lore.json           # import everything pending, then exit
   perforce2lore -config perforce2lore.json -watch    # keep importing new changelists
   ```

## Configuration

Relative paths in the settings file are resolved from the file's own folder.

| Key | Default | Meaning |
| --- | --- | --- |
| `p4Exe` | `p4` | Perforce client executable. |
| `p4Port`, `p4User`, `p4Client`, `p4Charset` | environment | Connection settings. Empty values use `P4PORT`, `P4USER`, `P4CLIENT`, `P4CHARSET` or `P4CONFIG`. |
| `depotPath` | | Depot path to import, such as `//Game/main/...`. One of `depotPath`, `streams` or `paths` is required. |
| `streams` | | Streams to import (see [Streams](#streams)). |
| `paths` | | Classic depot branches to import (see [Classic depot branches](#classic-depot-branches)). |
| `startChange` | `0` | First changelist to import into an empty repository. |
| `syncThreads` | `0` | Parallel sync threads. Values above 1 need `net.parallel.max` on the server. |
| `loreExe` | `lore` | Lore client executable. |
| `loreRoot` | *required* | Lore working copy, which is also the Perforce workspace root. |
| `push` | `false` | Push revisions to the Lore remote. |
| `pushEvery` | `1` | Push after this many commits. Any unpushed revisions are also pushed when a run ends. |
| `authorsFile` | `authors.json` | The authors file (see [Authors](#authors)). |
| `defaultAuthor` | none | Lore user credited when no author is mapped. When empty, the import halts instead. |
| `descriptionTags` | `keep` | What to do with `#tag value` description lines: `keep` them, also copy them to `metadata`, or `move` them to metadata. |
| `tagPrefix` | `p4-tag.` | Prefix of the metadata key for tags not in `tagMap`. |
| `tagMap` | `{}` | Metadata key for named tags (see [Description tags](#description-tags)). |
| `onlyMappedTags` | `false` | Leave tags not in `tagMap` in the message. |
| `stripLines` | `[]` | Regular expressions. Description lines that match are left out of the message. |
| `recordJobs` | `true` | Store the jobs a changelist fixes as `p4-jobs`. |
| `pruneEmptyDirs` | `true` | Remove folders left empty after each sync, for workspaces without `rmdir`. |
| `pollSeconds` | `60` | Wait between checks in watch mode. |
| `stateFile` | `<config>.state.json` | Where progress is saved. |

## Authors

The authors file maps names to Lore users. Keys are matched case-insensitively, and an entry with an empty value counts as unmapped. The file is re-read before every changelist, so you can fix a halted import without restarting it.

```json
{
  "jsmith": "john.smith",
  "alex.k@example.com": "alex",
  "Sam Lee": "sam",
  "@10432": "alex"
}
```

The importer tries these entries in order and uses the first one that matches:

1. `@<changelist>` credits that one changelist.
2. The Perforce user name.
3. The user's Perforce email, then their full name.
4. `defaultAuthor`, when it's set.

If nothing matches, the import halts at that changelist and logs the entry to add. `-dry-run` lists every missing entry at once.

## Description tags

Lines such as `#review alex` or `#urgent` are description tags. When `descriptionTags` is `metadata` or `move`, each tag becomes a metadata entry. A tag with no value is stored as `true`, and tags that share a key are joined with `, `.

`tagMap` sets the key for named tags. Tag names are matched case-insensitively, with or without the `#`:

```json
"descriptionTags": "move",
"tagMap": {
  "review": "review.by",
  "rb": "review.by",
  "jira": "issue",
  "ci-skip": ""
}
```

- `#review alex` and `#rb sam` both go to `review.by` as `alex, sam`.
- `#jira GAME-123` goes to `issue`.
- `#ci-skip` has an empty key, so it's dropped: it isn't stored, and with `move` it's removed from the message.
- Any other tag goes to `tagPrefix` plus its name, for example `p4-tag.urgent`. With `onlyMappedTags` on, it stays in the message instead.

## Streams

Set `streams` instead of `depotPath` to import a stream and the streams derived from it. Each stream is imported into its own Lore branch.

```json
"p4Client": "lore-import",
"loreRoot": "/srv/lore-import/main",
"streams": {
  "root": "//Game/Main",
  "rootBranch": "main",
  "include": ["//Game/Release-*", "//Game/Dev-*"],
  "exclude": ["//Game/Release-0.*"],
  "branches": { "//Game/Feature-Lighting": "lighting" },
  "layout": "shared"
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `root` | *required* | The top stream, usually a mainline. It's imported into `rootBranch`, which must already exist. |
| `rootBranch` | `main` | Lore branch for the root stream. |
| `include` | `[]` | Patterns for derived streams to import, such as `//Game/Release-*`. `*` matches within one path segment. |
| `exclude` | `[]` | Patterns for included streams to leave out. |
| `branches` | `{}` | Streams to import, each with its Lore branch name. A stream listed here is always imported. |
| `layout` | `shared` | `shared` or `separate` (see below). |
| `workspaceName` | `<p4Client>-{branch}` | Workspace name for each stream in the `separate` layout. |
| `workspaceRoot` | `<loreRoot>/../{branch}` | Folder for each stream in the `separate` layout. |
| `loreUrl` | none | Repository to clone into a new folder in the `separate` layout. |

`{branch}` is replaced by the branch name, and `{stream}` by the last segment of the stream path. Streams without a name in `branches` get the last segment of their path in lower case, so `//Game/Release-Dev` goes to `release-dev`.

Only streams derived from `root` can be imported. Virtual streams are skipped, because they have no changelists of their own. A second mainline needs its own import into its own repository.

### Branch points

A stream's branch is created from its parent's branch, at the parent's last changelist before the stream's first one. If the parent isn't imported, the nearest imported ancestor is used. Parents are imported before their children, and a child waits until its parent has caught up to its branch point.

The stream's first changelist, usually the one that populated it, is then imported by comparing the files on disk with that changelist. Only files that differ are downloaded, and files the stream doesn't have are removed. Any differences from the parent become that changelist's revision.

Changelists that copy or merge between streams are imported as ordinary revisions on the target branch, with the files they changed. They're not recorded as Lore merges.

### Layouts

- **`shared`**: one folder and one workspace (`loreRoot` and `p4Client`) switch between streams as each one is imported. This needs the least disk space. Each switch updates the files that differ between the branches and points the workspace at the stream. When streams differ a lot, a switch takes a while.
- **`separate`**: every stream has its own folder and its own workspace. Streams don't disturb each other, but each folder holds a full copy of its stream. Missing folders are cloned from `loreUrl`, and missing workspaces are created with the `rmdir` and `clobber` options. This layout needs `push`, so each folder can read the revisions committed in the others.

In both layouts, `p4Client` must be a stream workspace whose root is `loreRoot`, and that folder is used for the root stream.

## Classic depot branches

In a classic depot a branch is just a path, and Perforce doesn't record where it was branched from. Set `paths` to list the branch paths and name each one's parent:

```json
"p4Client": "lore-import",
"loreRoot": "/srv/lore-import/main",
"paths": {
  "root": { "path": "//depot/game/main", "branch": "main" },
  "branches": [
    { "path": "//depot/game/release-1.0", "branch": "release-1.0" },
    { "path": "//depot/game/release-1.1", "parent": "//depot/game/release-1.0" }
  ],
  "layout": "shared"
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `root.path` | *required* | The top branch path. It's imported into `root.branch`, which must already exist. |
| `root.branch` | `main` | Lore branch for the root path. |
| `branches[].path` | *required* | A branch path to import. |
| `branches[].branch` | last path segment | Lore branch name. |
| `branches[].parent` | `root.path` | The path it was branched from. It must also be imported. |

`layout`, `workspaceName`, `workspaceRoot` and `loreUrl` work as they do for [streams](#layouts). Branch points, the first changelist and merges are also handled the same way.

`p4Client` must be a classic workspace whose root is `loreRoot`. When the importer moves to another path, it replaces the workspace view with that path mapped to the whole root. In the `separate` layout, each path gets its own workspace with that view.

## What a revision records

| Metadata | Value |
| --- | --- |
| `created-by` | The Lore user the changelist is credited to. |
| `p4-changelist` | The changelist number. |
| `p4-user` | The Perforce account that submitted it. |
| `p4-workspace` | The workspace it was submitted from. |
| `p4-time` | The submit time, in Unix seconds. |
| `p4-stream` | The stream it was imported from, when importing streams. |
| `p4-path` | The classic path it was imported from, when importing `paths`. |
| `p4-jobs` | The jobs it fixes, when `recordJobs` is on and there are any. |
| `p4-tag.<name>` or a `tagMap` key | Description tags, when `descriptionTags` is `metadata` or `move`. |

`committed-by` is the account the importer runs as. The revision's own timestamp is the time of the import. The original submit time is kept in `p4-time`.

## How a changelist is imported

1. Credit the changelist to an author, or halt.
2. Sync the workspace to `<depotPath>@<changelist>`, then remove any folders left empty. With streams or classic paths, the working copy and workspace are first moved to the right branch and stream or view, and a new branch is created when needed.
3. Stage everything that changed in the working copy. A changelist that changes nothing in the workspace is skipped.
4. Set the metadata and commit with the description as the message.
5. Save progress, and push if it's due.

Progress is saved in the state file, per stream or path when importing several, and also in `p4-changelist` on each branch's latest revision. The import resumes from whichever is newer. If a commit fails, its files stay staged, and the next run commits them as the same changelist.

## Running as a service

The importer runs in the foreground and logs to standard output. Run it with `-watch` under any process supervisor, such as systemd, pm2 or a Windows service wrapper. It stops cleanly on Ctrl+C or SIGTERM.

```ini
# /etc/systemd/system/perforce2lore.service
[Unit]
Description=Perforce to Lore import
After=network-online.target

[Service]
User=lore-import
WorkingDirectory=/srv/lore-import
ExecStart=/srv/lore-import/perforce2lore -config perforce2lore.json -watch
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Finished, or the check passed. |
| `1` | Failed. The log says why. |
| `2` | Halted on a changelist with no author. Add the entry the log names, then run again. |

## Building

```
go build -ldflags "-X main.version=1.0.0" .
go test ./...
```

Releases are built by the **Release** workflow, for Linux (amd64 and arm64), Windows (amd64) and macOS (amd64 and arm64), with a `checksums.txt`. You can start it in either of two ways:

- **From GitHub:** go to Actions → Release → Run workflow, then enter a version such as `v1.3.0`. The tag is created for you.
- **From git:** push a tag:

  ```
  git tag v1.3.0
  git push origin v1.3.0
  ```

A version with a suffix, such as `v1.4.0-beta.1`, is published as a pre-release.

## License

[MIT](LICENSE)
