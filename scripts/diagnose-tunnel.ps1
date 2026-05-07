# Diagnose tunnel connectivity issues
# Usage: .\scripts\diagnose-tunnel.ps1 <tunnel-url>

param(
    [Parameter(Mandatory=$false)]
    [string]$TunnelUrl
)

$ErrorActionPreference = "Continue"

Write-Host "╔════════════════════════════════════════════════════════════╗" -ForegroundColor Cyan
Write-Host "║           🔍 TUNNEL DIAGNOSTICS                            ║" -ForegroundColor Cyan
Write-Host "╚════════════════════════════════════════════════════════════╝" -ForegroundColor Cyan
Write-Host ""

# Load environment variables from .env if it exists
if (Test-Path ".env") {
    Get-Content ".env" | ForEach-Object {
        if ($_ -match '^\s*([^#][^=]+)=(.*)$') {
            $name = $matches[1].Trim()
            $value = $matches[2].Trim()
            [Environment]::SetEnvironmentVariable($name, $value, "Process")
        }
    }
}

# If no URL provided, try to fetch from Supabase
if (-not $TunnelUrl) {
    Write-Host "📡 No URL provided, fetching from Supabase..." -ForegroundColor Yellow
    
    $SUPABASE_URL = $env:SUPABASE_URL
    $SUPABASE_API_KEY = $env:SUPABASE_API_KEY
    
    if ($SUPABASE_URL -and $SUPABASE_API_KEY) {
        try {
            $response = Invoke-RestMethod -Uri "$SUPABASE_URL/rest/v1/tunnel_sessions?select=*&order=started_at.desc&limit=1" `
                -Method Get `
                -Headers @{
                    "apikey" = $SUPABASE_API_KEY
                    "Content-Type" = "application/json"
                } -ErrorAction Stop
            
            if ($response -and $response.Count -gt 0) {
                $TunnelUrl = $response[0].url
                Write-Host "✅ Found tunnel URL: $TunnelUrl" -ForegroundColor Green
                Write-Host "   Started: $($response[0].started_at)" -ForegroundColor Gray
                Write-Host ""
            } else {
                Write-Host "❌ No tunnel sessions found in Supabase" -ForegroundColor Red
                exit 1
            }
        } catch {
            Write-Host "❌ Failed to fetch from Supabase: $_" -ForegroundColor Red
            Write-Host ""
            Write-Host "Please provide the tunnel URL manually:" -ForegroundColor Yellow
            Write-Host "  .\scripts\diagnose-tunnel.ps1 https://xxxxx.trycloudflare.com" -ForegroundColor Cyan
            exit 1
        }
    } else {
        Write-Host "❌ No Supabase credentials found and no URL provided" -ForegroundColor Red
        Write-Host ""
        Write-Host "Please provide the tunnel URL manually:" -ForegroundColor Yellow
        Write-Host "  .\scripts\diagnose-tunnel.ps1 https://xxxxx.trycloudflare.com" -ForegroundColor Cyan
        exit 1
    }
}

Write-Host "Testing tunnel: $TunnelUrl" -ForegroundColor Cyan
Write-Host ""

# Test 1: DNS Resolution
Write-Host "[1/5] DNS Resolution..." -ForegroundColor Yellow
try {
    $hostname = ([System.Uri]$TunnelUrl).Host
    $dnsResult = [System.Net.Dns]::GetHostAddresses($hostname)
    Write-Host "   ✅ DNS resolves to: $($dnsResult[0].IPAddressToString)" -ForegroundColor Green
} catch {
    Write-Host "   ❌ DNS resolution failed: $_" -ForegroundColor Red
    Write-Host "   This means the tunnel URL is invalid or expired" -ForegroundColor Yellow
    exit 1
}

# Test 2: TCP Connection
Write-Host "[2/5] TCP Connection (port 443)..." -ForegroundColor Yellow
try {
    $tcpClient = New-Object System.Net.Sockets.TcpClient
    $tcpClient.Connect($hostname, 443)
    $tcpClient.Close()
    Write-Host "   ✅ TCP connection successful" -ForegroundColor Green
} catch {
    Write-Host "   ❌ TCP connection failed: $_" -ForegroundColor Red
    Write-Host "   The tunnel may be down or blocked by firewall" -ForegroundColor Yellow
}

# Test 3: HTTPS Request
Write-Host "[3/5] HTTPS Request..." -ForegroundColor Yellow
try {
    $response = Invoke-WebRequest -Uri $TunnelUrl -Method Get -TimeoutSec 10 -UseBasicParsing -ErrorAction Stop
    Write-Host "   ✅ HTTP Status: $($response.StatusCode)" -ForegroundColor Green
    Write-Host "   ✅ Content Length: $($response.Content.Length) bytes" -ForegroundColor Green
} catch {
    $statusCode = $_.Exception.Response.StatusCode.value__
    Write-Host "   ❌ HTTPS request failed" -ForegroundColor Red
    if ($statusCode) {
        Write-Host "   HTTP Status Code: $statusCode" -ForegroundColor Yellow
        
        if ($statusCode -eq 502 -or $statusCode -eq 503 -or $statusCode -eq 504) {
            Write-Host "   ⚠️  Gateway error - the backend app (port 8080) may not be running" -ForegroundColor Yellow
        } elseif ($statusCode -eq 403) {
            Write-Host "   ⚠️  Forbidden - Cloudflare may be blocking the request" -ForegroundColor Yellow
        }
    } else {
        Write-Host "   Error: $_" -ForegroundColor Yellow
    }
}

# Test 4: Check if it's the GoOnDVR app
Write-Host "[4/5] Checking if GoOnDVR is responding..." -ForegroundColor Yellow
try {
    $response = Invoke-WebRequest -Uri $TunnelUrl -Method Get -TimeoutSec 10 -UseBasicParsing -ErrorAction Stop
    $content = $response.Content
    
    if ($content -match "goondvr|GoOnDVR|GOONDVR") {
        Write-Host "   ✅ GoOnDVR application detected" -ForegroundColor Green
    } elseif ($content -match "cloudflare") {
        Write-Host "   ⚠️  Cloudflare page detected (not the app)" -ForegroundColor Yellow
        Write-Host "   The tunnel exists but the backend app may not be running" -ForegroundColor Yellow
    } else {
        Write-Host "   ⚠️  Unknown response (first 200 chars):" -ForegroundColor Yellow
        Write-Host "   $($content.Substring(0, [Math]::Min(200, $content.Length)))" -ForegroundColor Gray
    }
} catch {
    Write-Host "   ❌ Cannot verify app response" -ForegroundColor Red
}

# Test 5: Check API endpoint
Write-Host "[5/5] Testing API endpoint..." -ForegroundColor Yellow
try {
    $apiUrl = "$TunnelUrl/api/stats"
    $response = Invoke-RestMethod -Uri $apiUrl -Method Get -TimeoutSec 10 -ErrorAction Stop
    Write-Host "   ✅ API is responding" -ForegroundColor Green
    Write-Host "   Uptime: $($response.uptime_seconds) seconds" -ForegroundColor Gray
    Write-Host "   Recording Count: $($response.recording_count)" -ForegroundColor Gray
    Write-Host "   Disk Usage: $([math]::Round($response.disk_percent, 2))%" -ForegroundColor Gray
} catch {
    Write-Host "   ❌ API endpoint not responding" -ForegroundColor Red
    Write-Host "   The app may not be fully started yet" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "════════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host "DIAGNOSIS COMPLETE" -ForegroundColor Cyan
Write-Host "════════════════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host ""

# Recommendations
Write-Host "💡 RECOMMENDATIONS:" -ForegroundColor Yellow
Write-Host ""
Write-Host "If the tunnel URL is valid but not working:" -ForegroundColor White
Write-Host "  1. Check GitHub Actions logs for the 'Build and start recorder' step" -ForegroundColor Gray
Write-Host "  2. Look for errors in the Docker container startup" -ForegroundColor Gray
Write-Host "  3. Verify port 8080 is accessible inside the container" -ForegroundColor Gray
Write-Host "  4. Wait 1-2 minutes after tunnel creation (Cloudflare propagation)" -ForegroundColor Gray
Write-Host ""
Write-Host "If you see 502/503/504 errors:" -ForegroundColor White
Write-Host "  • The tunnel is working but the app isn't responding on port 8080" -ForegroundColor Gray
Write-Host "  • Check the 'Build and start recorder' step logs in GitHub Actions" -ForegroundColor Gray
Write-Host "  • Look for Docker container crashes or startup failures" -ForegroundColor Gray
Write-Host ""
Write-Host "To check GitHub Actions logs:" -ForegroundColor White
Write-Host "  https://github.com/YOUR_USERNAME/YOUR_REPO/actions" -ForegroundColor Cyan
Write-Host ""
