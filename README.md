# Herakles

**Herakles** is a backup system written in Go. It stores files, directories and whole disks or partitions in **deduplicated, compressed, AES‑256 encrypted** repositories. Repositories can live on a local disk, a network share (NFS/CIFS mount, Windows UNC path or SMB without mounting), an SFTP server, or the **Herakles repository server** (optional client/server mode).

It ships as a single static binary for Linux and Windows. That one binary contains:

* a command line interface (`herakles …`),
* a backup wizard in the GUI and the terminal UI that sets up a complete job step by step,
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
| **Space saving** | Content‑defined chunking (FastCDC with a secret, per‑repository gear table) deduplicates data across files and within a backup chain. Compression is zstd, with auto‑detection of incompressible data. |
| **Portable backups** | Every backup is **one file** (`backups/*.hbk`). A full backup restores on its own; incrementals need only their chain. Copy the files anywhere (USB disk, other storage) and open them with the password alone. |
| **Backup types** | `full` re‑reads everything, `incremental` reads only what changed since the last snapshot, `differential` reads what changed since the last *full* snapshot, and `auto` is incremental but becomes full once `full_interval` has passed. **Every snapshot is complete and independently restorable.** |
| **Retention** | Fully configurable rules: `keep_last`, `keep_hourly/daily/weekly/monthly/yearly`, `keep_full`, `keep_within`, `keep_within_{hourly,daily,weekly,monthly,yearly}`, `keep_tags` and `group_by`. Rules are applied per job after every backup, with optional immediate prune. |
| **Encryption** | AES‑256‑GCM for all data and metadata. Keyed HMAC‑SHA256 content IDs, so blob IDs leak nothing. The master key is wrapped with Argon2id using a **password, a key file, or both**. A repository can have several keys (add, remove, change). |
| **Storage** | Local path, mounted NFS/CIFS share, UNC path, `smb://` (no mount needed), `sftp://` and `herakles://` (repository server). Upload and download bandwidth can be limited. |
| **Client/server (optional)** | HTTPS repository server with bcrypt users, per‑user repository ACLs, **append‑only mode** (protects against ransomware deleting backups), quotas and a self‑signed certificate generator. The server never sees plaintext or keys. |
| **Scheduling** | The daemon runs cron schedules for each job (with a different backup type per schedule), retries, catch‑up of missed runs and scheduled prune/check per repository. It installs as a **systemd** or **Windows service** (on Windows with a setup, no console needed) and has a token‑protected local control API. |
| **Reliability** | Repository locks (shared/exclusive, stale detection) and atomic writes. `check` verifies structure, and `check --read-data 10%` also downloads and verifies a sample of the data. `repair index` rebuilds the index of older‑format data from pack headers. Restores can be verified. A backup file appears only once it is complete; an interrupted backup leaves nothing behind (and starts over). |
| **Restore** | Full, partial (`snapshot:path`), include/exclude filters, overwrite policies, sparse files, `restore-device` back to a disk, `dump` of a file or a tar of a folder, `find` across all snapshots, `diff` between snapshots, and the GUI **explorer** (preview, search, multi-select) with downloads as ZIP, tar.gz or tar — also of files **inside disk images** (ext2/3/4, FAT, NTFS; MBR/GPT). |
| **Databases** | A `databases:` section dumps PostgreSQL, MySQL/MariaDB, MongoDB and SQLite running on the host with their own tools (`pg_dump`, `mysqldump`/`mariadb-dump`, `mongodump`, `sqlite3`) while they keep running. The dumps are streamed straight into the snapshot (no temporary files) and deduplicated like everything else. `herakles databases restore` and the GUI load a dump back into the server, also into a new database next to the current one. |
| **Docker** | A `docker:` section makes a job back up every Compose project (and standalone container) as its own snapshot `JOB/PROJECT`: project directory, bind mounts, named volumes (also of stopped projects) and dumps of Postgres, MySQL/MariaDB and MongoDB. Projects can be stopped during their backup instead. `herakles docker restore` stops the project, restores everything, starts it and loads the dumps into fresh databases, with automatic rollback. |
| **Web interface** | The daemon can serve the GUI on the network: accounts with Argon2id passwords and passkeys, roles (admin, user), no public sign-up (one-time setup link for the first administrator). |
| **Storage use** | The Repositories page shows a bar per repository: its size, other data on the same storage and free space (or the server quota); local disks, SMB, SFTP (statvfs) and the Herakles server. |
| **Updates** | Notifies about new releases (tray, window) and installs them on request through the package manager, the Windows setup or by replacing the file; announcements are signed and downloads checksummed. |
| **Central management** | A Herakles with the web interface is a **hub**: other servers attach with a one-time **enrollment code** and keep an outbound connection to it (no open port). In the hub's GUI, pick a server to use the wizard, see its disks, run, browse and restore there. Repositories marked as shared are passed to every server **with their passwords**. Both sides authenticate with Ed25519 keys. |
| **Extras** | Pre/post/success/failure hooks, notifications (webhook, e‑mail, command), `copy` of snapshots between repositories (3‑2‑1 replication), JSON output for scripting, run history. |

---

## Installation

On Linux, install the package from the signed repository on https://herakles.kloeck-it.de (Debian/Ubuntu, Fedora/RHEL/Rocky/Alma, openSUSE, Arch; x86‑64, ARM64, ARMv7):

```sh
curl -fsSL https://herakles.kloeck-it.de/install.sh | sudo sh
```

It ships `/usr/bin/herakles`, a menu entry, shell completions and `herakles.service`. Afterwards the script asks how Herakles will be used and sets it up:

* **Desktop** (default when a graphical session is installed): shares the system service with your user (`herakles daemon desktop on`), optionally turns on the tray icon at login and starts it right away if that user is logged in; then open *Herakles* from the menu (it also brings up the tray icon). Backups run as root, so whole disks and all files work.
* **Desktop, only your own files**: Herakles runs as your user (tray autostart).
* **Server**: starts the backup wizard in the terminal (`herakles tui --wizard`), then enables and starts `herakles.service`.
* **Nothing**: install only and print the next steps.

On **Windows**, run `herakles-setup.exe` from the site: one setup for x64 and ARM64 that installs to *Program Files*, adds a Start menu entry and an uninstaller, and asks the same question without a console: **whole computer** (the Windows service backs up every file and whole disks; the user's Herakles window and tray icon open it; configuration in `%ProgramData%\Herakles`, readable only by administrators) or **only my own files**. It also takes a hub enrollment code. Unattended: `herakles-setup.exe /S /MODE=desktop|user|none [/HUB=HKE1-…] [/NAME=…] [/USER=DOMAIN\name] [/NOAUTOSTART] [/NOPATH]`; uninstall with `uninstall.exe /S [/PURGE]`. The setup is built with NSIS (`packaging/windows/`, `apt install nsis`); its steps after copying the files are `herakles windows-setup install|uninstall|restart`.

On Linux, without a terminal, or to skip the questions, set `HERAKLES_MODE=desktop|user|server|none` (and `HERAKLES_USER` if sudo did not set `SUDO_USER`): `curl -fsSL https://herakles.kloeck-it.de/install.sh | sudo HERAKLES_MODE=server sh`. Binaries for all platforms are on the same site.

To build it yourself you need Go 1.26 or newer:

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

Each backup creates a **snapshot**: a complete, immutable view of the sources, written as **one file** in the repository's `backups/` folder, named after time, job and type (`20261112-031500_docker_full_3fa9c1d2e4b6.hbk`). The backup *type* decides what Herakles reads and what the file contains:

* **full** reads and hashes every byte and stores all of it in its own file. It never relies on earlier backups, so silent changes (for example a file modified without a new mtime) are caught, and the file restores on its own. Within the file, data is deduplicated.
* **incremental** compares size, mtime, ctime and inode with the latest snapshot of the job, reads only changed files and stores only data its **chain** (the files from the last full backup on) does not have yet. This is very fast and small.
* **differential** does the same as incremental, but builds on the latest **full** backup: its chain is two files.
* **auto** is the default. It runs incremental, but runs full if the last full backup is older than the job's `full_interval` (for example `7d`). With no earlier backup in this format, the backup is always full.

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

Every snapshot is complete: restoring one gives all its files, whatever its type. Forgetting a snapshot never breaks another one: a file that later backups still need stays (its snapshot hidden) until they are gone too. When the oldest backups of a chain have expired but newer ones still build on them, `prune` merges what is still needed into one file that replaces them, so a chain without new full backups (`auto` without `full_interval`) does not grow forever. Full backups cost space (they store everything again) but cut chains: each full backup and the incrementals after it can be copied, moved or deleted together.

**Portable.** A backup file carries the repository's settings and its password‑protected keys. A folder with nothing but copied `.hbk` files — a chain, or a single full backup — is a read‑only repository: `herakles snapshots --repo /media/usb/backups`, `herakles restore --repo /media/usb/backups latest --target /restore`, or the GUI. The window's snapshot details show each backup's file and which files a restore needs.

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
* Windows: `%ProgramData%\Herakles\herakles.yaml` if it exists and you may read it (administrators, the service), otherwise `%APPDATA%\Herakles\herakles.yaml`
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

### Backup wizard

**New backup** in the GUI (also *Get started* on an empty dashboard and *Backup wizard* on the jobs page) and `w` in the terminal UI lead through: what to back up (files and folders, Docker containers, a disk or partition) → sources (with suggested folders, exclusion presets, the Docker project picker, or the discovered disks and partitions) → where (an existing repository or a new one on a local/USB disk, SMB share, SFTP server or Herakles server, with a generated or chosen password stored next to the configuration with mode 0600, or asked for each time) → when (schedule presets) → how long (retention presets) → name and summary. The wizard creates and initialises the repository, saves the job and can start the first backup.

### Terminal UI — `herakles tui`

A full‑screen curses‑style interface with five tabs (keys `1`–`5` or `F1`–`F5`):

1. **Dashboard**: jobs with schedule and last result. Run a job (`Enter` to choose the type, `f` full, `i` incremental), see live progress bars, cancel, create/edit/delete jobs with a form.
2. **Repositories**: add, edit, initialize, check, prune and show statistics.
3. **Snapshots**: browse a snapshot as a tree, restore any file or folder, forget snapshots.
4. **Config**: raw YAML editor with validation (`Ctrl‑S`).
5. **Logs**: what happened in this session.

### Desktop GUI — `herakles gui`

The GUI is served only on `127.0.0.1` and protected by a random session token, CSRF and DNS‑rebinding checks. It opens in a **window of its own** without a web browser: WebView2 on Windows (built into Windows 11; the setup installs it elsewhere), WebKitGTK on Linux through the small `herakles-window` helper (`/usr/lib/herakles/`, loads `libwebkit2gtk-4.1` at run time so `herakles` itself stays a static binary). Without those it falls back to the default browser (Linux: `xdg-open`, the desktop portal, then installed browsers; Windows: an Edge app window) (`HERAKLES_BROWSER=1` forces the browser). The interface is fully embedded in the binary.

* **Dashboard**: job cards with Back up now / Full / Incremental / Differential, live progress, daemon status and next run.
* **Systems**: one card per backed-up computer (from all repositories) with last backup, freshness dot, snapshot count, jobs and stored size; click it to see that system's snapshots.
* **Snapshots & Restore**: opens with an overview of the backed-up sets: one card per Docker project, disk and folder job (per system) with last backup, freshness, snapshot counts by type and size; click a card for its snapshots, or switch to *All snapshots*. Summary (snapshots, jobs, latest backup, stored, read errors), filter by system and by type (full / incremental / differential), group by backup type, job, content, day or system, sort by any column; click a row for details (file counts, new data, parent, read errors, all paths). Compare two snapshots, forget one or several; **Download** a whole snapshot as ZIP, tar.gz or tar; **Browse** opens the explorer.
* **Explorer** (from Snapshots, the dashboard and container cards): breadcrumbs, a version switcher for the same job, sortable columns, filter, name search below the current folder (`invoice`, `*.pdf`), preview of text files and images, multi-select across folders; download the selection (a single file as it is, anything else as ZIP, tar.gz or tar) or restore it. `Backspace` goes up, `Esc` closes.
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
* Run either the tray or the daemon service, not both: they share the control port. To use the service from your desktop, see below.

### Desktop with the system service: `herakles daemon desktop`

The tray runs as your user, so it can only read your own files. To back up whole disks and partitions, system files or other users' homes, let the system service (running as root) do the backups and use it from your desktop:

```sh
sudo herakles daemon desktop on            # shares the service with the user who ran sudo, enables + starts it
sudo herakles daemon desktop on --user alice
sudo herakles daemon desktop off           # stop sharing; no argument lists the users
```

This adds the user to `gui.users` in `/etc/herakles/herakles.yaml`. The service then serves the GUI on 127.0.0.1 and writes `/run/herakles/desktop/<uid>.json` for each listed user, readable only by that user. Their **Herakles** menu entry (`herakles tray --open`: tray icon plus window; its action *Open the window only* runs `herakles gui`) and tray icon (`herakles tray`, also with autostart) find it and open the service's window instead of running Herakles as that user. The tray icon shows the service's status; its menu entry **Hide icon** only removes the icon. Backups run also when nobody is logged in. The window opens with a one-time login link, so the session token never appears on a command line.

Shared users get full control over the service, restores to any path included: only add people who may be root on this machine. Jobs in the user's own configuration do not run in this mode; set them up again in the service's window.

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

Without `projects` and `folders` every project on the host is backed up. `folders` includes every project whose directory lies inside one of them (for standalone containers: one of their bind mounts), so projects created later are picked up automatically; folders are also searched (three levels deep) for project directories whose containers do not exist. Everything else in the folders (files that belong to no project) goes into the job's own snapshot, so nothing in them is missed and nothing is stored twice. `projects` adds projects by name or glob, `exclude_projects` always wins. In the GUI job editor you tick the projects on the host (and per project whether it is stopped during its backup); on the command line use `config set job NAME --docker-folder /srv --docker-project wiki --docker-exclude portainer --docker-stop zammad`.

A project's own directory is the Compose directory, grown to the closest common parent of its bind mounts when that is still a directory of its own (inside a configured folder and containing no other project). Bitwarden, for example, keeps its compose file in `bwdata/docker` and mounts `bwdata/core`, `bwdata/mssql`, …: the whole `bwdata` (with `config.yml` and `env/`) belongs to the project. A bind mount that contains other projects (a deploy tool mounting `/srv/docker`) is left out of the project, and an in-place restore that would replace another project is refused.

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

## Databases

```yaml
jobs:
  - name: databases
    repository: nas
    databases:
      - type: postgres               # this computer: dumped as the system user postgres
      - type: mysql                  # MySQL or MariaDB; this computer: root over the socket
        exclude: [test_*]
      - type: postgres
        name: reporting              # directory name in the snapshot (default: the type)
        host: db.example.com
        user: backup
        password_env: PG_BACKUP_PASSWORD
        databases: [sales, crm]      # default: all
      - type: sqlite
        name: app
        databases: [/var/lib/app/*.db]
      # - type: mongodb
      #   uri: mongodb://backup@127.0.0.1:27017/?authSource=admin
      #   password_file: /etc/herakles/mongo.pw
    paths: [/etc]                    # files can go in the same snapshot (optional)
    schedule: [{ cron: "45 1 * * *" }]
```

Each database is dumped with the server's own client tool while it keeps running and stored as a file in the job's snapshot, under `/herakles-databases/<name>/`:

| Type | Tool | In the snapshot |
|---|---|---|
| `postgres` | `pg_dump` (plain SQL), `pg_dumpall --globals-only` | `<db>.sql` per database, `_globals.sql` with roles and tablespaces (when the user is a superuser) |
| `mysql` | `mariadb-dump` or `mysqldump` with `--single-transaction`, routines, events, triggers | `<db>.sql` per database (system schemas left out) |
| `mongodb` | `mongodump --archive` | `all.archive`, or `<db>.archive` per database listed |
| `sqlite` | `sqlite3 .dump` | `<file>.sql` per database file |

The dumps are streamed into the repository, so they need no disk space. They are deduplicated and compressed like files, so unchanged tables cost almost nothing. A failed dump (server down, wrong password) is left out of the snapshot and makes the run a warning that names it; the other databases and files are still saved.

Without connection settings Herakles connects like the tools do on the server itself. As root (the system service), Postgres is dumped as the system user `postgres` (peer authentication) and MySQL/MariaDB as `root` over the socket. Otherwise set `host` (or a socket directory), `port`, `user` and one of `password`, `password_file`, `password_env` or `password_command`. Passwords never appear on a command line: Postgres gets `PGPASSWORD`, while MySQL and MongoDB get a private option file. `run_as` runs the tools as another system user, `bin_dir` points to tools that are not on `PATH`, and `options` adds arguments.

```sh
herakles databases ls                                   # what each job dumps; checks the connections
herakles databases restore latest --job databases       # list the dumps of the latest snapshot
herakles databases restore latest postgres/shop         # load it back into "shop"
herakles databases restore latest postgres/shop --into shop_check   # next to the current one
herakles databases restore latest postgres/_globals     # roles first, on a new server
herakles databases restore latest app/app --into /tmp/app.db        # SQLite into a new file
herakles dump latest /herakles-databases/postgres/shop.sql > shop.sql   # just the file
```

A restore creates a missing Postgres or MySQL database and replaces the tables in the dump. Postgres restores run in one transaction, so they apply completely or not at all. On a new server, restore `_globals` first: it creates the roles that own the tables. A MongoDB restore replaces the collections in the dump. An existing SQLite file is only replaced with `--replace`, and keeps its owner. In the GUI, the job editor has a **Databases** section with a connection test. The snapshot details list the dumps with **Download** and **Restore…**.

Databases in Docker containers are covered by the `docker:` section (next chapter).

## Web interface with accounts

```yaml
web:
  listen: 127.0.0.1:7480             # behind a TLS reverse proxy
  url: https://backup.example.com    # public address; passkeys are bound to its host
```

The daemon then serves the GUI on the network. Sign-in is by password (Argon2id) or passkey (WebAuthn, discoverable). There is **no public sign-up**: while no account exists, the daemon logs a one-time setup link (`herakles web setup-link` prints it) for creating the first administrator; administrators create further users in the GUI (**Users**) or with `herakles web add NAME [--admin]`. Users can run backups, browse and restore; only administrators change configuration, repositories and users or see secrets. Failed logins are throttled; sessions end on sign-out, password change or when an account is disabled. Accounts live in `<state_dir>/web/accounts.json` (mode 0600).

---

## Updates

Herakles looks for a new release twice a day. The tray then notifies you and offers **Update to vX.Y.Z** in its menu, and the window shows **Install update**. Nothing is installed unasked. The update goes the way Herakles was installed: the package manager via pkexec (apt, dnf, zypper, pacman), the Windows setup (with a UAC prompt), or replacing the program file. Afterwards the tray restarts with the new version; while a backup runs, the update is refused. On servers, `herakles update [--check] [--yes]` does the same in a terminal and restarts the service.

The release announcement `downloads/latest.json` is signed with an Ed25519 release key whose public half is built into the program (`internal/update`), and every download must match its SHA‑256. `website/deploy/deploy.sh` writes and signs it for release tags with `.herakles-signing/update-ed25519.pem` (gitignored; never commit it), and publishes it last. Turn the check off with `updates: {check: false}`.

## Central management: hub and attached servers

A Herakles with the [web interface](#web-interface-with-accounts) is a **hub**. Other servers attach to it and are then managed from its GUI: pick one under **Managing** in the menu (or **Servers → Manage**) and every page — dashboard, wizard, disks, jobs, snapshots, restore, configuration, log — works on that server as if you were logged in there. Each server keeps its own configuration and schedule and keeps backing up while the hub is down; it can still be configured locally too.

```sh
# on the hub (or in its GUI: Servers → Enroll a server)
herakles hub enroll --name web-1
#   sudo herakles hub join HKE1-eyJ1Ijoi…

# on the server (Herakles installed, daemon running)
sudo herakles hub join HKE1-eyJ1Ijoi…
herakles hub status
```

Instead of `hub join`, paste the code in the server's own Herakles window (**Configuration → Central management → Attach to the hub**, also **Leave the hub** there) or, on Windows, into the setup.

The **enrollment code** is one string with everything the server needs: the hub's address, a one-time secret (valid 24 hours, `--valid`), the hub's public identity key, the name on the hub and — when the hub serves TLS itself (`web.tls_cert`) — a pin of its certificate, so a self-signed hub works without a CA file. A checksum catches copy errors.

```yaml
# hub
hub:
  repositories: [storagebox]   # passed to every attached server, with its password
```

While a server is managed from the hub, its wizard and job editor also offer the hub's repositories; choosing one shares it before the job is saved. Shared repositories arrive on the servers as `hub: true` entries; their password (and key, SSH and CA files) go to `<state_dir>/hub/repositories/` (mode 0600), so no server needs credentials of its own; each server's backups go into their own files (a server's incrementals build only on its own full backups). Snapshots are kept apart by host name, retention groups by host, and pruning/checks of shared repositories run only on the hub (their maintenance schedule is not passed on). A repository of the server with the same name wins. `herakles hub leave` detaches a server and keeps the repositories as ordinary ones; `herakles hub servers|rename|remove` manage the servers on the hub.

**Protocol.** The server makes its own Ed25519 key pair at enrollment and sends the public half with the secret; the hub stores only that and a hash of the used-up secret, and signs its answer with its identity key, which the server checks against the code. On every connection the server signs a login with a timestamp and nonce (replays and clocks off by more than 5 minutes are refused), and the hub signs the nonce back in the WebSocket handshake. HTTP requests then run over streams multiplexed on that WebSocket (yamux). Plain users of the hub get the same limits on the servers as on the hub; answers from servers are sandboxed so a server cannot run scripts in the hub's pages. **Whoever administers the hub controls the attached servers completely** (backups run as root and jobs can run hooks) — attach only servers the hub's administrators may control. A diagram is in the [guide](https://herakles.kloeck-it.de/guide.html#hub).

**Versions and compatibility.** Each server announces its version and **capabilities** when it connects (versions before 0.13 announce none; the hub derives theirs from the version). The hub compares them with its own, in both directions, and shows the result on **Servers** and in `herakles hub servers`: *compatible*, *limitations* (a page or function is missing, e.g. browsing disk images on a 0.11 server) or *incompatible* (e.g. a server that cannot read backups stored as one file per backup), each with the consequence and which side to update. Incompatible servers are also logged and marked in the *Managing* menu. The list of capabilities is in `internal/hub/compat.go`; a new one is added whenever hub and servers depend on each other for it (a shared storage format, an API the hub's window calls, a protocol change).

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

`herakles devices` lists disks, partitions and LVM/LUKS/RAID volumes with size, model, file system, label and mount points, and the **stable path** (`/dev/disk/by-id/…`) that survives reordered disks; the wizard (GUI and TUI) and the job editor (**Choose disks…**) offer the same list. Devices that hold the running system are marked *system*.

Herakles images the device content block by block, so unused (zero) areas deduplicate and compress to almost nothing. For consistent images, back up unmounted or read‑only filesystems, or LVM/ZFS/btrfs snapshots created in a `before` hook.

**Files inside disk images.** The explorer (and `ls`, `dump`, `restore`) opens disk images like folders: partitions (`part1`, `part2`, … from MBR with logical partitions, or GPT), then their files, read straight from the repository (`internal/imagefs`). Readable: ext2/3/4 (own reader: block maps and extents, htree directories, links followed inside the file system), FAT12/16/32 (go-diskfs) and NTFS (go-ntfs). XFS, btrfs, exFAT, LUKS, BitLocker, LVM, RAID members and swap are recognised and explained. Journals are not replayed.

```sh
herakles ls latest:/dev/sda                       # partitions
herakles dump latest:/dev/sda/part2/etc/fstab
herakles restore latest:/dev/sda/part2/home/alice --target /tmp/alice
```

### Inside a container (LXC, Docker, Podman)

A container sees the host's disks in `/sys`, but cannot read them: their device nodes are not passed in, and the host's device rules block them even when created by hand. Herakles recognises the container. `herakles devices`, the wizard and the job editor mark those disks as *unavailable*, and a disk backup fails with an explanation instead of a bare "no such file". What works:

* **The whole container as files**, the full backup of a container. Choose *This whole container* in the wizard, or use a job with `paths: [/]`. `/proc`, `/sys`, `/dev`, `/run` and other kernel and memory file systems are left out by themselves; their mount points are kept, so a restore gives a complete root file system. The local repository is never stored inside its own backup. With `one_file_system: true`, volumes mounted into the container (Proxmox `mp0`) are left out.
* **An image of the container's volume, made on the host**, e.g. `/dev/pve/vm-101-disk-0` on Proxmox LVM. A ZFS subvolume has no block device; back it up as files.
* **A disk passed into the container** (Proxmox: `dev0: /dev/sdb`, LXC: `lxc.cgroup2.devices.allow` plus a mount entry). It shows up as available and is imaged as usual.

---

## Repository format

```
config                plaintext: version, id, chunker parameters
keys/<id>             Argon2id-wrapped master keys
backups/<name>.hbk    one file per backup (below)
forgotten/<id>        marks a snapshot inside a backup file as forgotten
locks/<id>            encrypted lock files
data/, index/, snapshots/   older format (before 0.13): read, pruned, no longer written
```

A backup file:

```
"HERAKLES-BACKUP1" | header length (4) | header: config + key envelopes (JSON)
packs: [AES-GCM blobs…][encrypted pack header][4-byte length]  …
encrypted table of contents: packs with offsets, parent file, replaced files, encrypted snapshot
table of contents length (8) | "HKBEND01"
```

Blobs are `zstd(plaintext)` with a 1‑byte algorithm header and are encrypted individually with AES‑256‑GCM and a random nonce. Blob IDs are `HMAC‑SHA256(mac_key, plaintext)`. Packs are named after the SHA‑256 of their bytes, snapshots after the SHA‑256 of their encrypted form, so storage corruption is detectable (`check --read-data`). Backup files are streamed to the storage while the backup runs and appear under their name only when complete. `check` also verifies that every snapshot refers only to data in its own chain.

Repositories of earlier versions keep working: their snapshots stay readable and are pruned as before, and the first backup of a job in 0.13 is a full backup in a file of its own. Herakles before 0.13 does not see backups stored as files (the hub flags such servers).

---

## Packages

`packaging/` builds the Linux packages and repositories: `nfpm.yaml` (deb, rpm, pacman), the systemd unit, the desktop entry, install scripts, `install.sh` for the website and `repotool` (apt `Packages`/`Release` and the pacman database, written in Go). `packaging/build.sh <tag> dist <out>` builds and signs everything; `website/deploy/deploy.sh` runs it for release tags and publishes `/apt`, `/rpm` and `/arch`. The signing key lives in `.herakles-signing/` (gitignored, never commit it).

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
* Databases: dump plan, streaming into the snapshot, failed dumps left out, restore into a new database (fake tools); plus a manual run against real PostgreSQL 15, MariaDB 10.11, MongoDB 7 and SQLite (backup, in-place and side-by-side restore, roles, socket and password logins, server down).
* Containers: detection, unavailable host disks, backup of `/` with kernel file systems skipped, the repository left out of its own backup; plus a manual run in a container on a real host (host disks refused with an explanation, a passed-in partition imaged, the whole container backed up, restored and compared).
* Hub: enrollment codes (checksum, expiry, one-time use), signed enrollment and logins (impostor hub, wrong key, replay, old timestamps, removed server), a pinned self-signed hub certificate, passing repositories on (conflicts, removal, still-used ones), the forwarded GUI API with roles and sandboxed answers; plus a run with two daemons and the wizard, a backup and the explorer driven through the hub in a browser.
* Hub from the GUI: attaching and leaving in a server's own window (refused through the hub's proxy and without the service), plus a browser run: attach from the window, the hub's repository offered in the managed server's wizard, a backup into it, leave.
* Desktop sharing of the system service: share file, status, one-time login tickets.
* Disk images: ext2/3/4 in several variants (old features, 1 KiB blocks, block maps, extents, indexed directories, fast/slow/relative/absolute links), FAT12/16/32, NTFS, GPT and MBR with logical partitions, unbrowsable kinds recognised, randomly damaged ext metadata (errors, no panics); the explorer API on a backed-up GPT disk (partitions, preview, search, ZIP, restore); plus a real Debian 12 cloud disk: all 21,569 files and 2,152 links restored from the backed-up image match `debugfs rdump`.
* Storage use: repository size and capacity without a password, the server's quota and free space.
* Updates: version order, signed announcements (tampered or foreign signatures refused), checksum checks, replacing the program file, one notice per release, the tray's update entry and desktop notification over D‑Bus, the window's update API; plus runs against a local release server with the real key: `herakles update`, the window's Install button (file replaced, tray restarted as the new version), and on Wine a v0.10.0 setup install updated through the tray menu by the v0.11.0 setup.
* Windows setup: unattended and interactive installs (whole computer, own files, install only), upgrade and uninstall, run under Wine (service, shared window, tray autostart, PATH, Start menu entry, Add/Remove Programs).
* Backup files: one file per backup (no pack, index or snapshot files), chains (incrementals store only what their chain lacks, full backups store everything), a partial copy of one chain restores, forgetting and pruning (whole chains deleted, an expired chain start merged into a replacing file, further incrementals build through it), aborted and failed uploads leave nothing, corruption found, older-format data next to backup files (read, forgotten, pruned; the next backup is full), folders of copied `.hbk` files and single files opened read-only with the password; streams on local, SFTP, memory and the repository server (quota enforced while streaming); the CLI round trip backup → copy files → restore.
* Hub compatibility: capabilities announced and derived from old versions, issues in both directions, unknown capabilities of newer servers; a v0.12.3 server attached to a v0.13 hub is shown as incompatible with the reason.
* Tray mode: daemon and GUI sharing one runner, second-start handover, and the tray icon itself over a private D‑Bus session against a fake panel (registration after a late panel start, icon, tooltip, menu, Open, left click, Quit).

## Current limitations

* No Windows VSS snapshots yet. Open/locked files on Windows may fail to read; they are reported, not fatal.
* Hard links are stored as separate files (deduplicated, so no extra space). They are restored as separate files.
* Windows ACLs and alternate data streams are not preserved. POSIX permissions, ownership and xattrs are.
* No FUSE mount. Use `ls`, `find`, `dump`, the TUI browser or the GUI instead.
