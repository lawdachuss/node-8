$envLines = Get-Content .env
$map = @{}
foreach ($l in $envLines) { if ($l -match '^([A-Z_]+)="?(.*?)"?$') { $map[$matches[1]] = $matches[2] } }
$base = $map['SUPABASE_URL'].TrimEnd('/')
$key  = $map['SUPABASE_SERVICE_ROLE_KEY']
$headers = @{ "apikey" = $key; "Authorization" = "Bearer $key"; "Accept" = "application/json" }

function FetchAll($p){ $out=@();$o=0;while($true){$url="$base/rest/v1$p&limit=1000&offset=$o";$pg=Invoke-RestMethod -Uri $url -Headers $headers -Method Get;if($pg -is [System.Array]){$out+=$pg}else{$out+=@($pg)};if($pg.Count -lt 1000){break}$o+=1000}return $out }

$all = FetchAll "/channel_assignments?select=*"
Write-Host "total rows: $($all.Count)"
foreach ($r in $all) {
    if ($r.username -match 'cutenikkibaby|pleasure_hard66') {
        $un = $r.username; $st = $r.site
        $unBytes = ($un.ToCharArray() | ForEach-Object { [int]$_ }) -join ','
        $stBytes = ($st.ToCharArray() | ForEach-Object { [int]$_ }) -join ','
        Write-Host "---"
        Write-Host "username='$un' len=$($un.Length) codes=[$unBytes]"
        Write-Host "site='$st' len=$($st.Length) codes=[$stBytes]"
        Write-Host "assigned_node=$($r.assigned_node) status=$($r.status) created_at=$($r.created_at)"
    }
}
