$envLines = Get-Content .env
$map = @{}
foreach ($l in $envLines) {
    if ($l -match '^([A-Z_]+)="?(.*?)"?$') { $map[$matches[1]] = $matches[2] }
}
$base = $map['SUPABASE_URL'].TrimEnd('/')
$key  = $map['SUPABASE_SERVICE_ROLE_KEY']
$headers = @{ "apikey" = $key; "Authorization" = "Bearer $key"; "Content-Type" = "application/json"; "Accept" = "application/json" }

$dups = @(
    @{ username = 'cutenikkibaby';     site = 'stripchat' }
    @{ username = 'pleasure_hard66';   site = 'chaturbate' }
)

foreach ($d in $dups) {
    $u = [System.Uri]::EscapeDataString($d.username)
    $s = [System.Uri]::EscapeDataString($d.site)
    # fetch full rows for this (username, site)
    $getUrl = "$base/rest/v1/channel_assignments?username=eq.$u&site=eq.$s&select=*"
    $rows = Invoke-RestMethod -Uri $getUrl -Headers $headers -Method Get
    Write-Host "Found $($rows.Count) rows for $($d.username)/$($d.site)"
    if ($rows.Count -le 1) { Write-Host "  -> nothing to dedupe, skipping"; continue }

    # keep one; delete all then reinsert the kept one
    $keep = $rows[0]
    # remove system-managed columns we must not resend
    $keep.PSObject.Properties.Remove('created_at')
    $keep.PSObject.Properties.Remove('updated_at')

    $delUrl = "$base/rest/v1/channel_assignments?username=eq.$u&site=eq.$s"
    Invoke-RestMethod -Uri $delUrl -Headers $headers -Method Delete | Out-Null
    Write-Host "  -> deleted $($rows.Count) duplicate row(s)"

    $body = $keep | ConvertTo-Json -Depth 10 -Compress
    $resp = Invoke-RestMethod -Uri "$base/rest/v1/channel_assignments" -Headers $headers -Method Post -Body $body
    Write-Host "  -> reinserted 1 clean row (assigned_node=$($keep.assigned_node), status=$($keep.status))"
}

Write-Host "`n=== verification ==="
$all = Invoke-RestMethod -Uri "$base/rest/v1/channel_assignments?select=username,site&limit=50000" -Headers $headers -Method Get
$g = $all | Group-Object { "$($_.username)|$($_.site)" }
$bad = $g | Where-Object { $_.Count -gt 1 }
if ($bad.Count -eq 0) { Write-Host "OK: no duplicate (username,site) rows remain (total $($all.Count))" }
else { Write-Host "STILL HAS DUPLICATES:"; $bad | ForEach-Object { Write-Host "  $($_.Name) -> $($_.Count)" } }
