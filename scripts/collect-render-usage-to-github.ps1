param(
    [Parameter(Mandatory = $true)]
    [string]$RemoteBaseUrl,

    [Parameter(Mandatory = $true)]
    [string]$ManagementKey,

    [Parameter(Mandatory = $true)]
    [string]$GitRepoUrl,

    [string]$Branch = "main",

    [string]$LocalRoot = "D:\cliproxy-usage-backups",

    [int]$BatchSize = 100,

    [int]$IntervalSeconds = 60,

    [switch]$EnableUsageStatistics
)

$ErrorActionPreference = "Stop"

function Join-RemotePath {
    param([string]$BaseUrl, [string]$Path)
    return $BaseUrl.TrimEnd("/") + $Path
}

function Get-Headers {
    return @{
        "X-Management-Key" = $ManagementKey
        "Content-Type" = "application/json"
    }
}

function Ensure-GitRepo {
    New-Item -ItemType Directory -Force -Path $LocalRoot | Out-Null
    $repoDir = Join-Path $LocalRoot "github-repo"
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

function Enable-RemoteUsageStatistics {
    $url = Join-RemotePath $RemoteBaseUrl "/v0/management/usage-statistics-enabled"
    Invoke-RestMethod -Method Put -Uri $url -Headers (Get-Headers) -Body '{"value":true}' | Out-Null
}

function Get-UsageRecords {
    $url = Join-RemotePath $RemoteBaseUrl ("/v0/management/usage-queue?count={0}" -f $BatchSize)
    return @(Invoke-RestMethod -Method Get -Uri $url -Headers (Get-Headers))
}

function Get-RecordDay {
    param($Record)
    $timestamp = $Record.timestamp
    if ([string]::IsNullOrWhiteSpace([string]$timestamp)) {
        return (Get-Date).ToUniversalTime().ToString("yyyy-MM-dd")
    }
    try {
        return ([datetime]$timestamp).ToUniversalTime().ToString("yyyy-MM-dd")
    } catch {
        return (Get-Date).ToUniversalTime().ToString("yyyy-MM-dd")
    }
}

function Write-UsageRecords {
    param(
        [string]$RepoDir,
        [array]$Records
    )
    if ($Records.Count -eq 0) {
        return 0
    }
    $usageDir = Join-Path $RepoDir "usage-dashboard"
    New-Item -ItemType Directory -Force -Path $usageDir | Out-Null

    $written = 0
    foreach ($record in $Records) {
        $day = Get-RecordDay -Record $record
        $path = Join-Path $usageDir ("usage-{0}.jsonl" -f $day)
        $json = $record | ConvertTo-Json -Depth 32 -Compress
        Add-Content -LiteralPath $path -Value $json -Encoding UTF8
        $written++
    }
    return $written
}

function Publish-GitHubChanges {
    param([string]$RepoDir)
    git -C $RepoDir add usage-dashboard
    $status = git -C $RepoDir status --porcelain
    if ([string]::IsNullOrWhiteSpace($status)) {
        return
    }
    git -C $RepoDir commit -m "Collect Render usage dashboard data"
    git -C $RepoDir push origin $Branch
}

if ($EnableUsageStatistics) {
    Enable-RemoteUsageStatistics
}

do {
    $repoDir = Ensure-GitRepo
    $records = Get-UsageRecords
    $written = Write-UsageRecords -RepoDir $repoDir -Records $records
    if ($written -gt 0) {
        Publish-GitHubChanges -RepoDir $repoDir
    }
    Write-Host ("collected {0} usage records at {1}" -f $written, (Get-Date).ToString("s"))
    if ($IntervalSeconds -gt 0) {
        Start-Sleep -Seconds $IntervalSeconds
    }
} while ($IntervalSeconds -gt 0)
