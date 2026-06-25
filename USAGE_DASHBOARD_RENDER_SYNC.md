# Usage Dashboard on Render

CLIProxyAPI can persist token usage records from Render into the existing GitStore GitHub repository, then sync them with a Windows `D:` drive backup.

## Render Setup

Recommended environment variables:

```text
USAGE_DASHBOARD_ENABLED=true
```

Equivalent `config.yaml`:

```yaml
usage-dashboard:
  enabled: true
  data-dir: ""
```

When `data-dir` is empty, the service stores usage files next to `auth-dir`. In your Render GitStore deployment, that means `usage-dashboard/` is stored in the same GitHub repository as `auths/` and `config/`.

The dashboard UI is available at:

```text
https://your-service.onrender.com/usage-dashboard.html
```

The page uses the same management key as `/v0/management`.

## API Endpoints

```text
GET   /v0/management/usage-dashboard
GET   /v0/management/usage-dashboard/prices
PUT   /v0/management/usage-dashboard/prices
GET   /v0/management/usage-dashboard/files
GET   /v0/management/usage-dashboard/files/:name
PUT   /v0/management/usage-dashboard/files/:name
```

Usage files are daily JSONL files named `usage-YYYY-MM-DD.jsonl`. Prices are stored in `prices.json`.

## Backup Design

GitHub GitStore is the primary cloud copy for usage dashboard files. Backups are separate from that primary copy.

There are three independent backup modes:

1. Windows mirror: bidirectional merge folder under `D:\cliproxy-usage-backups\mirror`.
2. Pull export: remote-only timestamped copies under `D:\cliproxy-usage-backups\pull-exports`.
3. Snapshot: immutable zip archives under `D:\cliproxy-usage-backups\snapshots`, with hourly, daily, weekly, and monthly restore points.

The folders are intentionally separate:

```text
D:\cliproxy-usage-backups\mirror
D:\cliproxy-usage-backups\pull-exports\YYYYMMDD-HHMMSS
D:\cliproxy-usage-backups\snapshots\hourly
D:\cliproxy-usage-backups\snapshots\daily
D:\cliproxy-usage-backups\snapshots\weekly
D:\cliproxy-usage-backups\snapshots\monthly
```

Conflict behavior:

- `mirror` uploads local JSONL files first, Render merges and deduplicates records, then the merged remote files are downloaded back.
- `pull-exports` only downloads into a new timestamped folder and never uploads.
- `snapshots` only zips the local mirror and never uploads.
- Snapshot retention defaults to 24 hourly, 14 daily, 8 weekly, and 12 monthly restore points.
- Daily usage JSONL files are append-style and deduplicated during merge.
- `prices.json` uses `updated_at`; the newest price book wins. If an uploaded price book has no `updated_at`, it is treated as an intentional local edit.

## Windows D Drive Sync From GitHub

Preferred path:

```text
Render -> GitHub GitStore -> D:\cliproxy-usage-backups
```

For the current Render image deployment, run a collector on the Windows machine to move Render's in-memory usage queue into GitHub:

```powershell
.\scripts\collect-render-usage-to-github.ps1 `
  -RemoteBaseUrl "https://cliproxyapiplus-sh7t.onrender.com" `
  -ManagementKey "your-management-key" `
  -GitRepoUrl "https://github.com/sdhadahaoi/cli-config.git" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -EnableUsageStatistics `
  -IntervalSeconds 60
```

This collector is useful before the custom source build is deployed. After the custom build is running, Render can write the same `usage-dashboard/` folder through GitStore directly.

Pull usage files from the GitHub GitStore repository to D drive:

```powershell
.\scripts\backup-usage-dashboard-from-github.ps1 `
  -GitRepoUrl "https://github.com/sdhadahaoi/cli-config.git" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode pull
```

Pull and create multi-period restore snapshots:

```powershell
.\scripts\backup-usage-dashboard-from-github.ps1 `
  -GitRepoUrl "https://github.com/sdhadahaoi/cli-config.git" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode all
```

Restore a local snapshot back to GitHub. The next Render start or GitStore refresh will read it from GitHub:

```powershell
.\scripts\backup-usage-dashboard-from-github.ps1 `
  -GitRepoUrl "https://github.com/sdhadahaoi/cli-config.git" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode restore `
  -RestorePath "D:\cliproxy-usage-backups\snapshots\daily\cliproxy-usage-daily-20260626.zip"
```

## Windows D Drive Sync Through Render API

Run bidirectional mirror once:

```powershell
.\scripts\sync-usage-dashboard.ps1 `
  -RemoteBaseUrl "https://your-service.onrender.com" `
  -ManagementKey "your-management-key" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode mirror
```

Run all three backup modes once:

```powershell
.\scripts\sync-usage-dashboard.ps1 `
  -RemoteBaseUrl "https://your-service.onrender.com" `
  -ManagementKey "your-management-key" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode all
```

Customize restore point retention:

```powershell
.\scripts\sync-usage-dashboard.ps1 `
  -RemoteBaseUrl "https://your-service.onrender.com" `
  -ManagementKey "your-management-key" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode all `
  -KeepHourly 48 `
  -KeepDaily 30 `
  -KeepWeekly 12 `
  -KeepMonthly 24
```

Run mirror continuously every 60 seconds:

```powershell
.\scripts\sync-usage-dashboard.ps1 `
  -RemoteBaseUrl "https://your-service.onrender.com" `
  -ManagementKey "your-management-key" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode mirror `
  -IntervalSeconds 60
```

Restore from a snapshot or pull-export directory:

```powershell
.\scripts\sync-usage-dashboard.ps1 `
  -RemoteBaseUrl "https://your-service.onrender.com" `
  -ManagementKey "your-management-key" `
  -LocalRoot "D:\cliproxy-usage-backups" `
  -Mode restore `
  -RestorePath "D:\cliproxy-usage-backups\snapshots\daily\cliproxy-usage-daily-20260626.zip"
```

Restore uploads the selected files to Render for merge, then refreshes the D drive mirror from the merged cloud copy.
