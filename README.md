# Herakles

**Herakles** is a backup system written in Go. It stores files, directories and whole disks or partitions in **deduplicated, compressed, AES‑256 encrypted** repositories. Repositories can live on a local disk, a network share (NFS/CIFS mount, Windows UNC path or SMB without mounting), an SFTP server, or the **Herakles repository server** (optional client/server mode).

It ships as a single static binary for Linux and Windows. That one binary contains:

* a command line interface (`herakles …`),
* a curses‑style terminal UI (`herakles tui`),
* a graphical desktop UI (`herakles gui`),
* a tray icon that runs the daemon and the GUI on your desktop (`herakles tray`),
* a background daemon/service for scheduled backups (`herakles daemon`),
* Docker Compose awareness: every project backed up and restored on its own (`herakles docker`),
* a web interface with accounts and passkeys for servers (`web:` in the configuration),
* the repository server (`herakles server`).

Everything is configured in one YAML file. You can edit it by hand, from the CLI, in the TUI or in the GUI.

---

## Features

| Area | What you get |
|---|---|
| **Sources** | Files and directories; whole **disks/partitions as raw images** (`/dev/sda1`, `/dev/nvme0n1`, `\\.\C:`, `\\.\PhysicalDrive0`). Metadata includes permissions, owner, timestamps, symlinks, FIFOs, device nodes and Linux extended attributes. |
| **Space saving** | Content‑defined chunking (FastCDC with a secret, per‑repository gear table) deduplicates data across files, snapshots and hosts. Blobs are stored in pack files. Compression is zstd, with auto‑detection of incompressible data. |
| **Backup types** | `full` re‑reads everything, `incremental` reads only what changed since the last snapshot, `differential` reads what changed since the last *full* snapshot, and `auto` is incremental but becomes full once `full_interval` has passed. **Every snapshot is complete and independently restorable.** |
| **Retention** | Fully configurable rules: `keep_last`, `keep_hourly/daily/weekly/monthly/yearly`, `keep_full`, `keep_within`, `keep_within_{hourly,daily,weekly,monthly,yearly}`, `keep_tags` and `group_by`. Rules are applied per job after every backup, with optional immediate prune. |
| **Encryption** | AES‑256‑GCM for all data and metadata. Keyed HMAC‑SHA256 content IDs, so blob IDs leak nothing. The master key is wrapped with Argon2id using a **password, a key file, or both**. A repository can have several keys (add, remove, change). |
| **Storage** | Local path, mounted NFS/CIFS share, UNC path, `smb://` (no mount needed), `sftp://` and `herakles://` (repository server). Upload and download bandwidth can be limited. |
| **Client/server (optional)** | HTTPS repository server with bcrypt users, per‑user repository ACLs, **append‑only mode** (protects against ransomware deleting backups), quotas and a self‑signed certificate generator. The server never sees plaintext or keys. |
| **Scheduling** | The daemon runs cron schedules for each job (with a different backup type per schedule), retries, catch‑up of missed runs and scheduled prune/check per repository. It installs as a **systemd** or **Windows service** and has a token‑protected local control API. |
| **Reliability** | Repository locks (shared/exclusive, stale detection) and atomic writes. `check` verifies structure, and `check --read-data 10%` also downloads and verifies a sample of the data. `repair index` rebuilds the index from pack headers. Restores can be verified. Interrupted backups resume (indexes are written periodically). |
| **Restore** | Full, partial (`snapshot:path`), include/exclude filters, overwrite policies, sparse files, `restore-device` back to a disk, `dump` of a file or a tar of a folder, `find` across all snapshots, `diff` between snapshots, and download from the GUI. |
| **Docker** | A `docker:` section makes a job back up every Compose project (and standalone container) as its own snapshot `JOB/PROJECT`: project directory, bind mounts, named volumes (also of stopped projects) and dumps of Postgres, MySQL/MariaDB and MongoDB. Projects can be stopped during their backup instead. `herakles docker restore` stops the project, restores everything, starts it and loads the dumps into fresh databases, with automatic rollback. |
| **Web interface** | The daemon can serve the GUI on the network: accounts with Argon2id passwords and passkeys, roles (admin, user), no public sign-up (one-time setup link for the first administrator). |
| **Extras** | Pre/post/success/failure hooks, notifications (webhook, e‑mail, command), `copy` of snapshots between repositories (3‑2‑1 replication), JSON output for scripting, run history. |

---

## Installation

You need Go 1.26 or newer to build:

```sh
make build            # ./herakles for this platform
make dist             # cross-compile linux/windows/darwin binaries into dist/
```

Or build directly: `CGO_ENABLED=0 go build -o herakles ./cmd/herakles`. With cgo disabled the binary is fully static, so `GOOS=windows go build ./cmd/herakles` gives you `herakles.exe`.

---

## Quick start

```sh
# 1. Create a configuration (commented example) — or use the TUI/GUI instead
herakles config init
herakles config edit

# …or script it:
herakles config set repo local /mnt/backup/herakles --password-file-path /etc/herakles/pw
herakles config set job home --repository local --path /home \
    --exclude '**/.cache' --exclude '*.tmp' \
    --schedule '0 2 * * 0=full' --schedule '0 2 * * 1-6=incremental' \
    --yaml 'retention: {keep_daily: 7, keep_weekly: 4, keep_monthly: 12, keep_yearly: 2}'

# 2. Create the repository
herakles init --repo local

# 3. Back up now
herakles backup home                 # auto: incremental, full when due
herakles backup home --type full

# 4. Look around and restore
herakles snapshots
herakles ls latest /home/alice -l
herakles find '*.docx'
herakles restore latest:/home/alice/Documents --target /tmp/restore --verify

# 5. Run scheduled backups in the background
herakles daemon install && herakles daemon start      # systemd / Windows service
herakles daemon status
```

The ad‑hoc mode needs no configuration:

```sh
herakles init   --repo /mnt/usb/backup
herakles backup --repo /mnt/usb/backup --path ~/Projects --path ~/Documents
herakles backup --repo /mnt/usb/backup --device /dev/nvme0n1p2      # partition image
```

---

## Concepts

### Snapshots and backup types

Each backup creates a **snapshot**: a complete, immutable view of the sources. Because data is deduplicated, a snapshot only adds the chunks that are new. The backup *type* decides how much work Herakles does to find changes:

* **full** reads and hashes every byte. It never relies on earlier snapshots, so silent changes (for example a file modified without a new mtime) are caught. Unchanged data is still stored only once.
* **incremental** compares size, mtime, ctime and inode with the latest snapshot of the job and reads only changed files. This is very fast.
* **differential** does the same as incremental, but compares with the latest **full** snapshot.
* **auto** is the default. It runs incremental, but runs full if the last full backup is older than the job's `full_interval` (for example `7d`). With no earlier snapshot, the backup is always full.

A typical "full on Sunday, incremental on weekdays, keep a month back" setup looks like this:

```yaml
schedule:
  - { cron: "0 2 * * 0",   type: full }
  - { cron: "0 2 * * 1-6", type: incremental }
retention:
  keep_daily: 7
  keep_weekly: 5
  keep_monthly: 12
  keep_full: 2
```

Unlike tape‑style incremental chains, deleting any snapshot never breaks another one.

### Retention

After each backup, a job's `retention` policy is applied to that job's snapshots on this host (`group_by: job,host` by default). A snapshot is **kept if any rule keeps it**. If no rules are set, everything is kept, and Herakles refuses to forget without a policy. Durations use `y`, `m`, `w`, `d`, `h` (for example `1y6m`, `14d`). They are measured from the newest snapshot, so a broken backup schedule can never age out all of your data. Snapshot removal (`forget`) is separate from freeing space (`prune`). Set `prune: true` on a job, or schedule `maintenance.prune_schedule` on the repository.

### Encryption and keys

* Each repository has a random master key: an AES‑256 key, an HMAC key and the chunker seed.
* The master key is stored only in wrapped form, encrypted with a key derived via Argon2id from a **password**, a **key file**, or **password + key file**.
* Password sources, tried in order: `password`, `password_file`, `password_env`, `password_command`, then the `HERAKLES_PASSWORD` environment variable. If none is set, the CLI, TUI and GUI ask interactively.
* `herakles key generate PATH` creates a key file. Use `key add`, `key list`, `key remove` and `key passwd` to manage keys.
* **If you lose all passwords and key files, the data cannot be recovered.**

### Storage locations

| Location | Example |
|---|---|
| Local disk / mounted share | `/mnt/backup/herakles`, `D:\Backups\herakles`, `/mnt/nfs/herakles` |
| Windows UNC share | `\\nas\backup\herakles` |
| SMB/CIFS (no mount needed) | `smb://WORKGROUP;user@nas.local/share/herakles` (`remote.password_env`) |
| SFTP | `sftp://backup@host/srv/herakles` (ssh‑agent, `~/.ssh` keys, `known_hosts`) |
| Herakles server | `herakles://backup.example.com:8443/alice-laptop` (`herakles+http://` for plain HTTP) |

---

## Configuration

Run `herakles config example` to see a fully commented example with every option, or read [`internal/config/example.go`](internal/config/example.go). The default file location is:

* Linux: `/etc/herakles/herakles.yaml` as root, otherwise `~/.config/herakles/herakles.yaml`
* Windows: `%ProgramData%\Herakles\herakles.yaml` if it exists, otherwise `%APPDATA%\Herakles\herakles.yaml`
* or set `--config` / `HERAKLES_CONFIG`

Useful commands: `config validate`, `config show` (secrets redacted), `config edit` (opens `$EDITOR` and re‑validates), and `config set repo|job|remove …` for scripting. Saving from the TUI/GUI forms rewrites the file. The raw YAML editors in the TUI and GUI keep your comments.

---

## Interfaces

### CLI

```
herakles init | backup | jobs | snapshots | ls | find | diff | restore | restore-device | dump
         forget | prune | check | stats | copy | key | unlock | repair index
         config | daemon | server | tui | gui | tray | version
```

Every command has `--help` with examples. `--json` gives machine‑readable output. Exit codes: `0` ok, `1` error, `3` backup finished but some files could not be read.

### Terminal UI — `herakles tui`

A full‑screen curses‑style interface with five tabs (keys `1`–`5` or `F1`–`F5`):

1. **Dashboard**: jobs with schedule and last result. Run a job (`Enter` to choose the type, `f` full, `i` incremental), see live progress bars, cancel, create/edit/delete jobs with a form.
2. **Repositories**: add, edit, initialize, check, prune and show statistics.
3. **Snapshots**: browse a snapshot as a tree, restore any file or folder, forget snapshots.
4. **Config**: raw YAML editor with validation (`Ctrl‑S`).
5. **Logs**: what happened in this session.

### Desktop GUI — `herakles gui`

The GUI is served only on `127.0.0.1` and protected by a random session token, CSRF and DNS‑rebinding checks. It opens in a Chromium/Edge **app window** (no browser chrome) or falls back to the default browser. It needs no extra runtime on Linux or Windows and is fully embedded in the binary.

* **Dashboard**: job cards with Back up now / Full / Incremental / Differential, live progress, daemon status and next run.
* **Snapshots & Restore**: filter, browse, restore (target, overwrite policy, include filter, verify), download files or folders (tar), compare two snapshots, forget.
* **Backup Jobs**: full editor for sources, exclusions, schedules (with presets), retention, hooks and retries.
* **Repositories**: add/edit (local, SMB, SFTP, server), encryption/password source, key file, bandwidth limits, maintenance schedules, initialize, check, prune, statistics.
* **Configuration**: YAML editor that keeps comments. **Activity Log**: history and messages.

### Tray icon: `herakles tray`

On a desktop this is the easiest way to run Herakles. One process runs the daemon (scheduled backups and the control API) and the GUI, and shows an icon in the system tray. The icon's menu shows the current status and offers **Open Herakles** and **Quit Herakles**; a left click also opens the window. A badge on the icon shows when a backup is running (blue), a repository needs its password (amber) or the last backup of a job failed (red).

```sh
herakles tray                 # run it now (add --open to open the window right away)
herakles tray autostart on    # start it when you log in; "off" to stop, no argument shows the setting
```

Autostart is the `tray.autostart` option in the YAML file:

```yaml
tray:
  autostart: true
```

It is applied whenever the configuration is saved, from the GUI (checkbox under *Configuration → Desktop*), the TUI, `config edit`, `tray autostart`, and when the tray starts or reloads. When on, Herakles adds a login autostart entry and a *Herakles Backup* launcher to the applications menu; when off, it removes them (only if they start this configuration file). It starts with your desktop session, so it needs a login. For backups at boot without anyone logged in, use the daemon service (`daemon install`) instead.

* Starting it a second time (e.g. from the applications menu) opens the window of the running instance.
* Unlike the system service, it does not need a stored password: a repository without a password source shows as locked until you unlock it in the GUI; scheduled backups then use the password until you quit.
* Linux: works with every desktop that supports StatusNotifierItem tray icons (KDE Plasma, Xfce, Cinnamon, MATE, LXQt, Budgie, waybar, …). On GNOME install the *AppIndicator and KStatusNotifierItem Support* extension. If the panel starts after Herakles, the icon appears as soon as it is ready. Without a desktop session, it keeps running as daemon + GUI and prints the GUI URL.
* Windows: autostart uses `HKCU\…\CurrentVersion\Run`; the console window closes when it is started from Explorer or at login.
* Run either the tray or the daemon service, not both: they share the control port.

### Daemon

```sh
herakles daemon run                 # foreground (what the service runs)
herakles daemon install [--user]    # systemd unit / Windows service; then: daemon start
herakles daemon status              # schedule, running tasks with progress, last results
herakles daemon trigger home --type full --wait
herakles daemon trigger local --kind prune
herakles daemon reload              # or SIGHUP; the TUI/GUI reload it after saving
herakles daemon logs
```

The daemon needs a non‑interactive password source for each repository.

---

## Docker Compose projects

```yaml
jobs:
  - name: containers
    repository: nas
    docker:
      folders: [/srv]                # every project in /srv, also ones created later
      # projects: [wiki, shop-*]     # and/or explicit projects; neither = all projects
      exclude_projects: [portainer]
      # stop: [zammad]               # stop during backup instead of dumping
    paths: [/srv/other]              # host data outside the projects (optional)
    schedule: [{ cron: "15 3 * * *", type: auto }]
    full_interval: 7d
    retention: { keep_daily: 7, keep_weekly: 4, keep_monthly: 6 }
```

Without `projects` and `folders` every project on the host is backed up. `folders` includes every project whose directory lies inside one of them (for standalone containers: one of their bind mounts), so projects created later are picked up automatically; folders are also searched (three levels deep) for project directories whose containers do not exist. `projects` adds projects by name or glob, `exclude_projects` always wins. In the GUI job editor you tick the projects on the host (and per project whether it is stopped during its backup); on the command line use `config set job NAME --docker-folder /srv --docker-project wiki --docker-exclude portainer --docker-stop zammad`.

Projects are discovered from the Compose container labels. Each project gets its own snapshot `containers/<project>` holding the project directory, bind mounts outside it, its named volumes (by mount and by Compose label, so stopped projects keep theirs), `docker inspect` output and logical dumps of its Postgres, MySQL/MariaDB and MongoDB containers (taken while running; database containers without a server, such as backup sidecars, are skipped). Retention and history work per project.

```sh
herakles docker ls [--last]          # projects, databases, data paths, last backup, orphan volumes
herakles docker show wiki            # exactly what is backed up for one project
herakles docker backup wiki          # or: herakles backup containers/wiki
herakles docker restore wiki         # stop, restore, start, load dumps (asks for confirmation)
herakles docker restore wiki --snapshot 3fa2b1c9 --databases files --keep-old
herakles docker restore wiki --target /tmp/wiki   # just the files, Docker untouched
```

A restore moves the current data aside, restores directory, bind mounts and volumes, recreates missing containers and volumes with `docker compose up --no-start` (so a project can be restored onto a fresh host), starts the project and, in **dumps** mode (default for snapshots taken while running), loads the dumps into freshly initialised database directories. **files** mode (default for snapshots taken with the project stopped) restores the raw database files. If anything fails before the project runs again, the previous data is put back.

## Web interface with accounts

```yaml
web:
  listen: 127.0.0.1:7480             # behind a TLS reverse proxy
  url: https://backup.example.com    # public address; passkeys are bound to its host
```

The daemon then serves the GUI on the network. Sign-in is by password (Argon2id) or passkey (WebAuthn, discoverable). There is **no public sign-up**: while no account exists, the daemon logs a one-time setup link (`herakles web setup-link` prints it) for creating the first administrator; administrators create further users in the GUI (**Users**) or with `herakles web add NAME [--admin]`. Users can run backups, browse and restore; only administrators change configuration, repositories and users or see secrets. Failed logins are throttled; sessions end on sign-out, password change or when an account is disabled. Accounts live in `<state_dir>/web/accounts.json` (mode 0600).

---

## Client/server mode

```sh
# on the server
herakles server gen-cert /etc/herakles/server.crt /etc/herakles/server.key --hosts backup.example.com
herakles server hash-password            # -> paste into server.users[].password_hash
herakles server run --config /etc/herakles/server.yaml
```

```yaml
server:
  listen: ":8443"
  data_dir: /srv/herakles
  tls_cert: /etc/herakles/server.crt
  tls_key: /etc/herakles/server.key
  quota_gib: 500
  users:
    - name: alice
      password_hash: "$2a$10$…"
      repositories: ["alice-*"]     # default: "alice" and "alice-*"
      append_only: true             # clients can add backups but never delete
```

On the client, use `location: herakles://backup.example.com:8443/alice-laptop` with `remote.username`, `remote.password_env` and `remote.ca_cert`. With append‑only accounts, run `forget`/`prune` on the server host (or with a separate admin account).

---

## Disaster recovery of disks and partitions

```sh
herakles backup --repo offsite --device /dev/sda1        # or a job with `devices:`
herakles restore-device latest:/dev/sda1 --repo offsite --to /dev/sdb1 --force
herakles restore latest:/dev/sda1 --target /tmp          # → /tmp/sda1 image file (sparse)
```

Herakles images the device content block by block, so unused (zero) areas deduplicate and compress to almost nothing. For consistent images, back up unmounted or read‑only filesystems, or LVM/ZFS/btrfs snapshots created in a `before` hook.

---

## Repository format (v1)

```
config                plaintext: version, id, chunker parameters
keys/<id>             Argon2id-wrapped master keys
data/<xx>/<id>        pack files: [AES-GCM blobs…][encrypted header][4-byte header length]
index/<id>            encrypted list of packs and their blobs
snapshots/<id>        encrypted snapshot metadata
locks/<id>            encrypted lock files
```

Blobs are `zstd(plaintext)` with a 1‑byte algorithm header and are encrypted individually with AES‑256‑GCM and a random nonce. Blob IDs are `HMAC‑SHA256(mac_key, plaintext)`. Files are named after the SHA‑256 of their stored bytes, so storage corruption is detectable.

---

## Development & tests

```sh
go test ./...            # unit + integration tests
go test -race ./...
HERAKLES_TEST_SMB='smb://user:pass@127.0.0.1/share' go test ./internal/backend -run SMB
```

The suite covers:

* crypto, chunking and compression;
* every backend through a shared conformance test (local, in‑memory, SFTP via an in‑process SSH server, SMB against Samba when configured);
* the repository core: dedup, locks, prune, check, corruption detection and index rebuild;
* backup/restore round trips, incremental detection, excludes and device images;
* retention, configuration, the job engine (hooks, notifications, copy), the server (auth, ACL, append‑only, quota) and the daemon (scheduler, retries, catch‑up, control API);
* CLI end‑to‑end workflows, the TUI (simulated terminal) and the GUI API.
* Docker: discovery, data sets, database detection, dumps and loads against a fake docker CLI; per-project backup, restore in dumps and files mode, rollback, fresh-host restore; plus a manual integration run against real Postgres, MariaDB and MongoDB containers.
* Web mode: setup link, password and passkey logins, throttling, roles, secret redaction, Host/CSRF checks; a Playwright run with a virtual authenticator.
* Tray mode: daemon and GUI sharing one runner, second-start handover, and the tray icon itself over a private D‑Bus session against a fake panel (registration after a late panel start, icon, tooltip, menu, Open, left click, Quit).

## Current limitations

* No Windows VSS snapshots yet. Open/locked files on Windows may fail to read; they are reported, not fatal.
* Hard links are stored as separate files (deduplicated, so no extra space). They are restored as separate files.
* Windows ACLs and alternate data streams are not preserved. POSIX permissions, ownership and xattrs are.
* No FUSE mount. Use `ls`, `find`, `dump`, the TUI browser or the GUI instead.
