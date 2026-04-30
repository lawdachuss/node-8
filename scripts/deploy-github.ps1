# Deploy GoOnDVR from GitHub to EC2
# This script connects to EC2 and runs the deployment from GitHub

param(
    [Parameter(Mandatory=$true)]
    [string]$EC2_IP,
    
    [Parameter(Mandatory=$false)]
    [string]$KeyPath = "aws-secrets/aws-key.pem"
)

Write-Host "Deploying GoOnDVR from GitHub to EC2..." -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "EC2 IP: $EC2_IP" -ForegroundColor Yellow
Write-Host "SSH Key: $KeyPath" -ForegroundColor Yellow
Write-Host "GitHub: https://github.com/vasud3v/record.git" -ForegroundColor Yellow
Write-Host ""

# Check if key exists
if (-not (Test-Path $KeyPath)) {
    Write-Host "ERROR: SSH key not found: $KeyPath" -ForegroundColor Red
    exit 1
}

Write-Host "Testing SSH connection..." -ForegroundColor Yellow
$testConnection = ssh -i $KeyPath -o ConnectTimeout=10 -o StrictHostKeyChecking=no ubuntu@${EC2_IP} "echo 'Connected'" 2>&1

if ($LASTEXITCODE -ne 0) {
    Write-Host "ERROR: Cannot connect to EC2 instance!" -ForegroundColor Red
    Write-Host "   Make sure:" -ForegroundColor Gray
    Write-Host "   1. EC2 instance is running" -ForegroundColor Gray
    Write-Host "   2. Security group allows SSH (port 22) from your IP" -ForegroundColor Gray
    Write-Host "   3. SSH key is correct" -ForegroundColor Gray
    exit 1
}

Write-Host "OK SSH connection successful!" -ForegroundColor Green
Write-Host ""

Write-Host "Downloading deployment script..." -ForegroundColor Yellow
ssh -i $KeyPath ubuntu@${EC2_IP} @"
    curl -fsSL https://raw.githubusercontent.com/vasud3v/record/main/scripts/deploy-from-github.sh -o /tmp/deploy.sh
    chmod +x /tmp/deploy.sh
"@

if ($LASTEXITCODE -ne 0) {
    Write-Host "ERROR: Failed to download deployment script!" -ForegroundColor Red
    Write-Host "   Trying alternative method..." -ForegroundColor Yellow
    
    # Alternative: Upload the script
    Write-Host "Uploading deployment script..." -ForegroundColor Yellow
    scp -i $KeyPath scripts/deploy-from-github.sh ubuntu@${EC2_IP}:/tmp/deploy.sh
    ssh -i $KeyPath ubuntu@${EC2_IP} "chmod +x /tmp/deploy.sh"
}

Write-Host "OK Deployment script ready!" -ForegroundColor Green
Write-Host ""

Write-Host "Running deployment (this will take 5-10 minutes)..." -ForegroundColor Yellow
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

# Run deployment script
ssh -i $KeyPath ubuntu@${EC2_IP} "bash /tmp/deploy.sh"

if ($LASTEXITCODE -eq 0) {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host "SUCCESS: DEPLOYMENT COMPLETE!" -ForegroundColor Green
    Write-Host "========================================" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Web UI: http://${EC2_IP}:8080" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Quick Commands:" -ForegroundColor Yellow
    Write-Host "  Check status:" -ForegroundColor Gray
    Write-Host "    ssh -i $KeyPath ubuntu@${EC2_IP} 'cd /home/ubuntu/goondvr && sudo docker compose ps'" -ForegroundColor White
    Write-Host ""
    Write-Host "  View logs:" -ForegroundColor Gray
    Write-Host "    ssh -i $KeyPath ubuntu@${EC2_IP} 'cd /home/ubuntu/goondvr && sudo docker compose logs recorder --tail=50'" -ForegroundColor White
    Write-Host ""
    Write-Host "  Check memory:" -ForegroundColor Gray
    Write-Host "    ssh -i $KeyPath ubuntu@${EC2_IP} 'free -h'" -ForegroundColor White
    Write-Host ""
    Write-Host "  Check disk:" -ForegroundColor Gray
    Write-Host "    ssh -i $KeyPath ubuntu@${EC2_IP} 'df -h'" -ForegroundColor White
    Write-Host ""
    Write-Host "Next Steps:" -ForegroundColor Yellow
    Write-Host "  1. Open http://${EC2_IP}:8080 in your browser" -ForegroundColor Gray
    Write-Host "  2. Add 20-30 channels to start" -ForegroundColor Gray
    Write-Host "  3. Monitor for 24 hours" -ForegroundColor Gray
    Write-Host "  4. Scale up to 40 channels if stable" -ForegroundColor Gray
    Write-Host ""
    Write-Host "Important:" -ForegroundColor Red
    Write-Host "  If you cannot access port 8080, add it to Security Group" -ForegroundColor Gray
    Write-Host "  AWS Console -> EC2 -> Security Groups -> Add Inbound Rule" -ForegroundColor Gray
    Write-Host "  Type: Custom TCP, Port: 8080, Source: Your IP" -ForegroundColor Gray
    Write-Host ""
    Write-Host "Happy recording!" -ForegroundColor Green
} else {
    Write-Host ""
    Write-Host "ERROR: DEPLOYMENT FAILED!" -ForegroundColor Red
    Write-Host ""
    Write-Host "Troubleshooting:" -ForegroundColor Yellow
    Write-Host "  1. Check the error messages above" -ForegroundColor Gray
    Write-Host "  2. SSH into EC2 and check logs:" -ForegroundColor Gray
    Write-Host "     ssh -i $KeyPath ubuntu@${EC2_IP}" -ForegroundColor White
    Write-Host "  3. Try manual deployment:" -ForegroundColor Gray
    Write-Host "     cd /home/ubuntu/goondvr" -ForegroundColor White
    Write-Host "     sudo docker compose logs" -ForegroundColor White
    exit 1
}
