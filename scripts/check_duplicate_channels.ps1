$envLines = Get-Content .env
$map = @{}
foreach ($l in $envLines) {
    if ($l -match '^([A-Z_]+)="?(.*?)"?$') { $map[$matches[1]] = $matches[2] }
}
$base = $map['SUPABASE_URL'].TrimEnd('/')
$key  = $map['SUPABASE_SERVICE_ROLE_KEY']

$headers = @{
    "apikey"        = $key
    "Authorization" = "Bearer $key"
    "Accept"        = "application/json"
}

function FetchAll($pathBase) {
    $out = @()
    $offset = 0
    $limit = 1000
    while ($true) {
        $url = "$base/rest/v1$pathBase`&limit=$limit&offset=$offset"
        try {
            $page = Invoke-RestMethod -Uri $url -Headers $headers -Method Get
        } catch {
            Write-Host "ERROR fetching $pathBase : $_"
            break
        }
        if ($page -is [System.Array]) { $out += $page } else { $out += @($page) }
        if ($page.Count -lt $limit) { break }
        $offset += $limit
    }
    return $out
}

Write-Host "=== 1. Duplicate (username, site) in channel_assignments ===" -ForegroundColor Cyan
$rows = FetchAll "/channel_assignments?select=username,site,assigned_node,status"
Write-Host "total rows: $($rows.Count)"
$groups = $rows | Group-Object { "$($_.username)|$($_.site)" }
$dup = $groups | Where-Object { $_.Count -gt 1 }
if ($dup.Count -eq 0) { Write-Host "NONE - every (username,site) is unique" }
else {
    Write-Host "DUPLICATES FOUND: $($dup.Count) keys"
    $dup | ForEach-Object {
        Write-Host "  $($_.Name) -> $($_.Count) rows"
        $_.Group | ForEach-Object { Write-Host "      node=$($_.assigned_node) status=$($_.status)" }
    }
}

Write-Host "=== 2. Duplicate usernames in channels table ===" -ForegroundColor Cyan
$chan = FetchAll "/channels?select=username"
$cg = $chan | Group-Object username
$cdup = $cg | Where-Object { $_.Count -gt 1 }
if ($cdup.Count -eq 0) { Write-Host "NONE - every username is unique in channels (total $($chan.Count))" }
else {
    Write-Host "DUPLICATES FOUND: $($cdup.Count)"
    $cdup | ForEach-Object { Write-Host "  $($_.Name) -> $($_.Count)" }
}

Write-Host "=== 3. Same username (any site) assigned to >1 node ===" -ForegroundColor Cyan
$assigned = $rows | Where-Object { $_.assigned_node -and $_.assigned_node -ne $null }
$ag = $assigned | Group-Object username
$adup = $ag | Where-Object { ($_.Group.assigned_node | Sort-Object -Unique).Count -gt 1 }
if ($adup.Count -eq 0) { Write-Host "NONE - no username spread across multiple nodes" }
else {
    Write-Host "CONFLICTS FOUND: $($adup.Count)"
    $adup | ForEach-Object { Write-Host "  $($_.Name) -> nodes: $($_.Group.assigned_node -join ', ')" }
}

Write-Host "=== 4. Username listed more than once in channel_assignments across any field combos ===" -ForegroundColor Cyan
$ug = $rows | Group-Object username
$udup = $ug | Where-Object { $_.Count -gt 1 }
if ($udup.Count -eq 0) { Write-Host "NONE" }
else {
    Write-Host "usernames appearing >1 time: $($udup.Count)"
    $udup | ForEach-Object { Write-Host "  $($_.Name) -> $($_.Count) rows (sites: $(($_.Group.site | Sort-Object -Unique) -join ', '))" }
}
