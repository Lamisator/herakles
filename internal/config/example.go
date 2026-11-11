package config

// Example is a fully commented sample configuration.
const Example = `# Herakles configuration file (YAML).
# Location: /etc/herakles/herakles.yaml (root), ~/.config/herakles/herakles.yaml,
# %ProgramData%\Herakles\herakles.yaml or %APPDATA%\Herakles\herakles.yaml,
# or set HERAKLES_CONFIG / --config.

# Where run history, logs and the daemon token are kept.
# state_dir: /var/lib/herakles

repositories:
  # A repository on a local disk, a mounted NFS/CIFS share or a UNC path
  # such as \\nas\backup\herakles.
  - name: local
    location: /mnt/backup/herakles
    # Choose one password source (or a key file, or both):
    password_file: /etc/herakles/password
    # password_env: HERAKLES_PASSWORD
    # password_command: "pass show backup/herakles"
    # password: "inline-secret"            # discouraged
    # key_file: /etc/herakles/repo.key     # see 'herakles key generate'
    compression: auto          # auto | none | zstd | zstd:1..4 | auto,zstd:3
    pack_size_mib: 16
    # connections: 4
    maintenance:
      prune_schedule: "30 4 * * 0"   # weekly garbage collection
      repack_threshold: 0.2
      check_schedule: "0 5 1 * *"    # monthly integrity check
      check_read_data: "10%"         # ...downloading 10% of the data

  # A Herakles repository server (see 'herakles server').
  - name: offsite
    location: herakles://backup.example.com:8443/alice-laptop
    password_env: HERAKLES_OFFSITE_PASSWORD
    remote:
      username: alice
      password_env: HERAKLES_SERVER_PASSWORD
      # ca_cert: /etc/herakles/server-ca.pem
    upload_limit_kib: 5120     # 5 MiB/s

  # An SMB/CIFS share without mounting it.
  # - name: nas
  #   location: smb://WORKGROUP;backup@nas.local/backups/herakles
  #   password_file: /etc/herakles/password
  #   remote:
  #     password_env: NAS_PASSWORD

  # SFTP (uses ~/.ssh keys / ssh-agent and known_hosts).
  # - name: sftp
  #   location: sftp://backup@host.example.com/srv/herakles
  #   password_file: /etc/herakles/password
  #   remote:
  #     identity_file: ~/.ssh/id_ed25519

jobs:
  - name: home
    description: Home directories
    repository: local
    paths:
      - /home
    exclude:
      - "*.tmp"
      - "**/node_modules"
      - "**/.cache"
      - /home/*/Downloads
    exclude_if_present: [.nobackup]
    exclude_caches: true
    exclude_larger_than: 20G
    one_file_system: false
    tags: [home]
    # "auto" backups become full when the last full one is older than this.
    full_interval: 7d
    schedule:
      - cron: "0 2 * * 0"      # Sunday 02:00: full backup (re-reads everything)
        type: full
      - cron: "0 2 * * 1-6"    # Mon-Sat 02:00: incremental (only changed files)
        type: incremental
      - cron: "0 */4 * * *"    # every 4 hours: auto (full if older than full_interval)
        type: auto
    retention:
      keep_last: 3
      keep_hourly: 24
      keep_daily: 7
      keep_weekly: 4
      keep_monthly: 12
      keep_yearly: 3
      keep_full: 2
      keep_within: 2d
      # keep_within_daily: 1m
      # keep_tags: [important]
      # group_by: job,host
    prune: false               # prune right after forgetting snapshots
    hooks:
      before: ["echo starting backup of $HERAKLES_JOB"]
      after: []
      on_failure: []
      timeout: 15m
    retry:
      attempts: 2
      delay: 10m
    catch_up: true             # run missed schedules when the daemon starts

  - name: system-disk
    repository: offsite
    devices:
      - /dev/sda1              # raw partition image (Windows: \\.\C: or \\.\PhysicalDrive0)
    schedule:
      - cron: "0 3 1 * *"
        type: full
    retention:
      keep_monthly: 6

daemon:
  control_listen: 127.0.0.1:7477
  max_concurrent_jobs: 1
  # log_file: /var/log/herakles.log

gui:
  listen: 127.0.0.1:7478
  open_browser: true

tray:
  # Start 'herakles tray' (scheduled backups, GUI and a tray icon) when you
  # log in to your desktop. Use either this or the daemon service.
  autostart: false

server:
  listen: ":8443"
  data_dir: /srv/herakles
  # tls_cert: /etc/herakles/server.crt
  # tls_key: /etc/herakles/server.key
  append_only: false
  # quota_gib: 500
  users:
    - name: alice
      # generate with: herakles server hash-password
      password_hash: "$2a$10$replace.with.a.real.bcrypt.hash.................."
      repositories: ["alice-*"]
      append_only: true        # protects against ransomware deleting backups

notifications:
  - type: webhook
    url: https://hooks.example.com/herakles
    on: [failure, warning]
  # - type: email
  #   smtp_host: smtp.example.com
  #   smtp_port: 587
  #   smtp_user: alerts@example.com
  #   smtp_password: secret
  #   from: alerts@example.com
  #   to: [admin@example.com]
  #   on: [failure]
  # - type: command
  #   command: /usr/local/bin/notify-backup
`
