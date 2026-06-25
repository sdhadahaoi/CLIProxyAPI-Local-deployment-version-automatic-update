param(
    [Parameter(Mandatory = $true)]
    [string]$GitRepoUrl,

    [string]$Branch = "main",

    [string]$LocalRoot = "D:\cliproxy-usage-backups",

    [ValidateSet("pull", "snapshot", "restore", "all")]
    [string]$Mode = "pull",

    [string]$RestorePath = "",

    [int]$KeepHourly = 24,

    [int]$KeepDaily = 14,

    [int]$KeepWeekly = 8,

    [int]$KeepMonthly = 12
)

$ErrorActionPreference = "Stop"

function Get-RepoDir {
    return Join-Path $LocalRoot "github-repo"
}

function Get-MirrorDir {
    return Join-Path $LocalRoot "mirror"
}

function Ensure-GitRepo {
    New-Item -ItemType Directory -Force -Path $LocalRoot | Out-Null
    $repoDir = Get-RepoDir
    if (-not (Test-Path -LiteralPath (Join-Path $repoDir ".git"))) {
        if (Test-Path -LiteralPath $repoDir) {
            Remove-Item -LiteralPath $repoDir -Recurse -Force
        }
        git clone --branch $Branch $GitRepoUrl $repoDir
    } else {
        git -C $repoDir fetch origin $Branch
        git -C $repoDir checkout $Branch
        git -C $repoDir pull --ff-only origin $Branch
    }
    return $repoDir
}

function Sync-FromGitHub {
    $repoDir = Ensure-GitRepo
    $sourceDir = Join-Path $repoDir "usage-dashboard"
    $mirrorDir = Get-MirrorDir
    New-Item -ItemType Directory -Force -Path $mirrorDir | Out-Null
    if (-not (Test-Path -LiteralPath $sourceDir)) {
        Write-Host "No usage-dashboard folder exists in GitHub yet."
        return
    }
    Copy-Item -LiteralPath (Join-Path $sourceDir "*") -Destination $mirrorDir -Recurse -Force
    Write-Host ("GitHub usage dashboard copied to: {0}" -f $mirrorDir)
}

function Restore-ToGitHub {
    if ([string]::IsNullOrWhiteSpace($RestorePath)) {
        throw "RestorePath is required when Mode is restore."
    }
    $repoDir = Ensure-GitRepo
    $targetDir = Join-Path $repoDir "usage-dashboard"
    New-Item -ItemType Directory -Force -Path $targetDir | Out-Null

    $resolvedRestorePath = Resolve-Path -LiteralPath $RestorePath
    $sourceItem = Get-Item -LiteralPath $resolvedRestorePath
    $sourceDir = $sourceItem.FullName
    if (-not $sourceItem.PSIsContainer) {
        if ($sourceItem.Extension -ne ".zip") {
            throw "RestorePath must be a snapshot .zip file or a directory."
        }
        $stamp = (Get-Date).ToUniversalTime().ToString("yyyyMMdd-HHmmss")
        $sourceDir = Join-Path (Join-Path $LocalRoot "restore-staging") $stamp
        New-Item -ItemType Directory -Force -Path $sourceDir | Out-Null
        Expand-Archive -LiteralPath $sourceItem.FullName -DestinationPath $sourceDir -Force
    }

    Copy-Item -LiteralPath (Join-Path $sourceDir "*") -Destination $targetDir -Recurse -Force
    git -C $repoDir add usage-dashboard
    $status = git -C $repoDir status --porcelain
    if ([string]::IsNullOrWhiteSpace($status)) {
        Write-Host "No restore changes to push."
        return
    }
    git -C $repoDir commit -m "Restore usage dashboard backup"
    git -C $repoDir push origin $Branch
    Sync-FromGitHub
}

function New-LocalSnapshot {
    $mirrorDir = Get-MirrorDir
    $snapshotDir = Join-Path $LocalRoot "snapshots"
    $hourlyDir = Join-Path $snapshotDir "hourly"
    $dailyDir = Join-Path $snapshotDir "daily"
    $weeklyDir = Join-Path $snapshotDir "weekly"
    $monthlyDir = Join-Path $snapshotDir "monthly"
    foreach ($dir in @($hourlyDir, $dailyDir, $weeklyDir, $monthlyDir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
    $files = @(Get-ChildItem -LiteralPath $mirrorDir -File -ErrorAction SilentlyContinue)
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
        if (-not (Test-Path -LiteralPath $zipPath)) {
            Compress-Archive -LiteralPath $files.FullName -DestinationPath $zipPath -CompressionLevel Optimal
            Write-Host ("snapshot created: {0}" -f $zipPath)
        }
    }
    Remove-OldSnapshots -Directory $hourlyDir -Keep $KeepHourly
    Remove-OldSnapshots -Directory $dailyDir -Keep $KeepDaily
    Remove-OldSnapshots -Directory $weeklyDir -Keep $KeepWeekly
    Remove-OldSnapshots -Directory $monthlyDir -Keep $KeepMonthly
}

function Get-WeekStamp {
    param([datetime]$Date)
    $calendar = [System.Globalization.CultureInfo]::InvariantCulture.Calendar
    $week = $calendar.GetWeekOfYear($Date, [System.Globalization.CalendarWeekRule]::FirstFourDayWeek, [DayOfWeek]::Monday)
    return "{0}-W{1:D2}" -f $Date.Year, $week
}

function Remove-OldSnapshots {
    param([string]$Directory, [int]$Keep)
    if ($Keep -le 0) {
        return
    }
    $files = @(Get-ChildItem -LiteralPath $Directory -Filter "*.zip" -File | Sort-Object LastWriteTimeUtc -Descending)
    if ($files.Count -gt $Keep) {
        $files | Select-Object -Skip $Keep | ForEach-Object { Remove-Item -LiteralPath $_.FullName -Force }
    }
}

switch ($Mode) {
    "pull" {
        Sync-FromGitHub
    }
    "snapshot" {
        New-LocalSnapshot
    }
    "restore" {
        Restore-ToGitHub
    }
    "all" {
        Sync-FromGitHub
        New-LocalSnapshot
    }
}
