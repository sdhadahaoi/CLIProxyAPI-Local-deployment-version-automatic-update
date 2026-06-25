param(
    [Parameter(Mandatory = $true)]
    [string]$RemoteBaseUrl,

    [Parameter(Mandatory = $true)]
    [string]$ManagementKey,

    [string]$LocalRoot = "D:\cliproxy-usage-backups",

    [ValidateSet("mirror", "pull-export", "snapshot", "restore", "all")]
    [string]$Mode = "mirror",

    [string]$RestorePath = "",

    [int]$KeepHourly = 24,

    [int]$KeepDaily = 14,

    [int]$KeepWeekly = 8,

    [int]$KeepMonthly = 12,

    [int]$IntervalSeconds = 0
)

$ErrorActionPreference = "Stop"

function Join-RemotePath {
    param([string]$BaseUrl, [string]$Path)
    return $BaseUrl.TrimEnd("/") + $Path
}

function Get-Headers {
    return @{
        "X-Management-Key" = $ManagementKey
    }
}

function Get-UsageFiles {
    param([string]$Directory)
    if (-not (Test-Path -LiteralPath $Directory)) {
        return @()
    }
    return Get-ChildItem -LiteralPath $Directory -File |
        Where-Object { $_.Name -eq "prices.json" -or ($_.Name -like "usage-*.jsonl") }
}

function Get-RemoteUsageFiles {
    $filesUrl = Join-RemotePath $RemoteBaseUrl "/v0/management/usage-dashboard/files"
    return Invoke-RestMethod -Method Get -Uri $filesUrl -Headers (Get-Headers)
}

function Send-UsageFile {
    param([System.IO.FileInfo]$File)
    $encodedName = [uri]::EscapeDataString($File.Name)
    $uploadUrl = Join-RemotePath $RemoteBaseUrl "/v0/management/usage-dashboard/files/$encodedName"
    Invoke-RestMethod -Method Put -Uri $uploadUrl -Headers (Get-Headers) -InFile $File.FullName -ContentType "application/octet-stream" | Out-Null
}

function Receive-UsageFile {
    param(
        [string]$Name,
        [string]$TargetDirectory
    )
    New-Item -ItemType Directory -Force -Path $TargetDirectory | Out-Null
    $encodedName = [uri]::EscapeDataString($Name)
    $downloadUrl = Join-RemotePath $RemoteBaseUrl "/v0/management/usage-dashboard/files/$encodedName"
    $targetPath = Join-Path $TargetDirectory $Name
    $tmpPath = $targetPath + ".download"
    Invoke-WebRequest -Method Get -Uri $downloadUrl -Headers (Get-Headers) -OutFile $tmpPath
    Move-Item -LiteralPath $tmpPath -Destination $targetPath -Force
}

function Sync-Mirror {
    $mirrorDir = Join-Path $LocalRoot "mirror"
    New-Item -ItemType Directory -Force -Path $mirrorDir | Out-Null

    foreach ($file in (Get-UsageFiles -Directory $mirrorDir)) {
        Send-UsageFile -File $file
    }

    foreach ($remote in (Get-RemoteUsageFiles)) {
        $name = [string]$remote.name
        if (-not [string]::IsNullOrWhiteSpace($name)) {
            Receive-UsageFile -Name $name -TargetDirectory $mirrorDir
        }
    }

    Write-Host ("mirror synced: {0}" -f $mirrorDir)
}

function New-PullExport {
    $stamp = (Get-Date).ToUniversalTime().ToString("yyyyMMdd-HHmmss")
    $exportDir = Join-Path (Join-Path $LocalRoot "pull-exports") $stamp
    New-Item -ItemType Directory -Force -Path $exportDir | Out-Null

    foreach ($remote in (Get-RemoteUsageFiles)) {
        $name = [string]$remote.name
        if (-not [string]::IsNullOrWhiteSpace($name)) {
            Receive-UsageFile -Name $name -TargetDirectory $exportDir
        }
    }

    Write-Host ("pull export created: {0}" -f $exportDir)
}

function New-LocalSnapshot {
    $mirrorDir = Join-Path $LocalRoot "mirror"
    $snapshotDir = Join-Path $LocalRoot "snapshots"
    $hourlyDir = Join-Path $snapshotDir "hourly"
    $dailyDir = Join-Path $snapshotDir "daily"
    $weeklyDir = Join-Path $snapshotDir "weekly"
    $monthlyDir = Join-Path $snapshotDir "monthly"
    foreach ($dir in @($hourlyDir, $dailyDir, $weeklyDir, $monthlyDir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }

    $files = @(Get-UsageFiles -Directory $mirrorDir)
    if ($files.Count -eq 0) {
        Write-Host ("snapshot skipped: no mirror files in {0}" -f $mirrorDir)
        return
    }

    $now = (Get-Date).ToUniversalTime()
    $targets = @(
        @{ Directory = $hourlyDir; Name = "cliproxy-usage-hourly-{0}.zip" -f $now.ToString("yyyyMMdd-HH") },
        @{ Directory = $dailyDir; Name = "cliproxy-usage-daily-{0}.zip" -f $now.ToString("yyyyMMdd") },
        @{ Directory = $weeklyDir; Name = "cliproxy-usage-weekly-{0}.zip" -f (Get-WeekStamp -Date $now) },
        @{ Directory = $monthlyDir; Name = "cliproxy-usage-monthly-{0}.zip" -f $now.ToString("yyyyMM") }
    )

    foreach ($target in $targets) {
        $zipPath = Join-Path $target.Directory $target.Name
        if (Test-Path -LiteralPath $zipPath) {
            continue
        }
        Compress-Archive -LiteralPath $files.FullName -DestinationPath $zipPath -CompressionLevel Optimal
        Write-Host ("snapshot created: {0}" -f $zipPath)
    }

    Remove-OldSnapshots -Directory $hourlyDir -Keep $KeepHourly
    Remove-OldSnapshots -Directory $dailyDir -Keep $KeepDaily
    Remove-OldSnapshots -Directory $weeklyDir -Keep $KeepWeekly
    Remove-OldSnapshots -Directory $monthlyDir -Keep $KeepMonthly
}

function Restore-UsageBackup {
    if ([string]::IsNullOrWhiteSpace($RestorePath)) {
        throw "RestorePath is required when Mode is restore."
    }

    $resolvedRestorePath = Resolve-Path -LiteralPath $RestorePath
    $sourceItem = Get-Item -LiteralPath $resolvedRestorePath
    $restoreSourceDir = $sourceItem.FullName

    if (-not $sourceItem.PSIsContainer) {
        if ($sourceItem.Extension -ne ".zip") {
            throw "RestorePath must be a snapshot .zip file or an export directory."
        }
        $stamp = (Get-Date).ToUniversalTime().ToString("yyyyMMdd-HHmmss")
        $restoreSourceDir = Join-Path (Join-Path $LocalRoot "restore-staging") $stamp
        New-Item -ItemType Directory -Force -Path $restoreSourceDir | Out-Null
        Expand-Archive -LiteralPath $sourceItem.FullName -DestinationPath $restoreSourceDir -Force
    }

    $files = @(Get-UsageFiles -Directory $restoreSourceDir)
    if ($files.Count -eq 0) {
        throw "RestorePath does not contain usage dashboard files."
    }

    foreach ($file in $files) {
        Send-UsageFile -File $file
    }

    Sync-Mirror
    Write-Host ("restore merged from: {0}" -f $sourceItem.FullName)
}

function Get-WeekStamp {
    param([datetime]$Date)
    $calendar = [System.Globalization.CultureInfo]::InvariantCulture.Calendar
    $week = $calendar.GetWeekOfYear($Date, [System.Globalization.CalendarWeekRule]::FirstFourDayWeek, [DayOfWeek]::Monday)
    return "{0}-W{1:D2}" -f $Date.Year, $week
}

function Remove-OldSnapshots {
    param(
        [string]$Directory,
        [int]$Keep
    )
    if ($Keep -le 0) {
        return
    }
    $files = @(Get-ChildItem -LiteralPath $Directory -Filter "*.zip" -File | Sort-Object LastWriteTimeUtc -Descending)
    if ($files.Count -le $Keep) {
        return
    }
    $files | Select-Object -Skip $Keep | ForEach-Object {
        Remove-Item -LiteralPath $_.FullName -Force
    }
}

function Invoke-BackupCycle {
    New-Item -ItemType Directory -Force -Path $LocalRoot | Out-Null
    switch ($Mode) {
        "mirror" {
            Sync-Mirror
        }
        "pull-export" {
            New-PullExport
        }
        "snapshot" {
            New-LocalSnapshot
        }
        "restore" {
            Restore-UsageBackup
        }
        "all" {
            Sync-Mirror
            New-PullExport
            New-LocalSnapshot
        }
    }
    Write-Host ("cycle complete: {0}" -f (Get-Date).ToString("s"))
}

do {
    Invoke-BackupCycle
    if ($IntervalSeconds -gt 0) {
        Start-Sleep -Seconds $IntervalSeconds
    }
} while ($IntervalSeconds -gt 0)
