param()

function Wait-ForRunToFinish($runId, $repo, $timeoutSeconds) {
    $deadline = (Get-Date).AddSeconds($timeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $info = gh run view $runId --repo $repo --json status,conclusion 2>$null
        if ($info) {
            $parsed = $info | ConvertFrom-Json
            if ($parsed.status -eq "completed") {
                return $true
            }
        }
        Start-Sleep -Seconds 3
    }
    return $false
}

Write-Host "=== Cancelling active workflows ==="
$allCancelledIds = @{}
foreach ($r in 1..10) {
    $repo = "lawdachuss/node-$r"
    $json = gh run list --repo $repo --limit 50 --json databaseId,status --jq ".[]" 2>$null
    $count = 0
    if ($json) {
        $runs = $json | ConvertFrom-Json
        $repoIds = @()
        foreach ($run in $runs) {
            if ($run.status -eq "in_progress" -or $run.status -eq "queued") {
                gh run cancel $run.databaseId --repo $repo 2>$null
                $repoIds += $run.databaseId
                $count++
            }
        }
        if ($repoIds.Count -gt 0) {
            $allCancelledIds[$repo] = $repoIds
        }
    }
    if ($count -gt 0) {
        Write-Host ("node-${r}: cancelling $count run(s)")
    } else {
        Write-Host ("node-${r}: no active runs")
    }
}

Write-Host ""
Write-Host "=== Waiting for cancelled runs to fully stop ==="
foreach ($r in 1..10) {
    $repo = "lawdachuss/node-$r"
    if ($allCancelledIds.ContainsKey($repo)) {
        foreach ($id in $allCancelledIds[$repo]) {
            Write-Host ("node-${r}: waiting for run $id to complete..." )
            $finished = Wait-ForRunToFinish $id $repo 30
            if ($finished) {
                Write-Host ("node-${r}: run $id stopped")
            } else {
                Write-Host ("node-${r}: run $id still active after timeout, proceeding anyway")
            }
        }
    }
}

Write-Host ""
Write-Host "=== Triggering fresh workflows ==="
foreach ($r in 1..10) {
    $repo = "lawdachuss/node-$r"
    $url = gh workflow run secure-rdp.yml --repo $repo 2>$null
    Write-Host ("node-${r}: triggered → $url")
}
