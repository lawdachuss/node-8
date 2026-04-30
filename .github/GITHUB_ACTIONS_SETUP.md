# 🚀 GitHub Actions Setup Guide

This repository includes automated CI/CD workflows for deploying and managing your GoOnDVR recorder on AWS EC2.

## 📋 Workflows

### 1. **Deploy to EC2** (`deploy.yml`)
- **Triggers:** Push to `main` branch, or manual trigger
- **What it does:**
  - Pulls latest code on EC2
  - Rebuilds Docker containers
  - Cleans build cache
  - Verifies deployment

### 2. **Build and Test** (`build-test.yml`)
- **Triggers:** Pull requests, pushes to non-main branches
- **What it does:**
  - Builds Docker image
  - Runs Go linting
  - Tests docker-compose configuration

### 3. **Cleanup EC2 Disk** (`cleanup.yml`)
- **Triggers:** Daily at 3 AM UTC, or manual trigger
- **What it does:**
  - Runs disk cleanup script
  - Removes old Docker images and cache
  - Deletes old recordings
  - Alerts if disk usage > 85%

## 🔧 Setup Instructions

### Step 1: Add GitHub Secrets

Go to your GitHub repository → Settings → Secrets and variables → Actions → New repository secret

Add these secrets:

#### `EC2_SSH_KEY`
Your EC2 private key (the content of `aws-secrets/aws-key.pem`)

```bash
# On Windows PowerShell:
Get-Content aws-secrets/aws-key.pem | clip

# Then paste into GitHub secret
```

#### `EC2_HOST`
Your EC2 public IP address

```
32.193.245.111
```

### Step 2: Enable GitHub Actions

1. Go to your repository → **Actions** tab
2. Click **"I understand my workflows, go ahead and enable them"**
3. All workflows are now active!

## 🎯 How to Use

### Automatic Deployment
Every time you push to `main` branch:
```bash
git add .
git commit -m "Update feature"
git push origin main
```
→ GitHub Actions automatically deploys to EC2! 🚀

### Manual Deployment
1. Go to **Actions** tab
2. Select **"Deploy to EC2"** workflow
3. Click **"Run workflow"**
4. Select branch and click **"Run workflow"**

### Manual Cleanup
1. Go to **Actions** tab
2. Select **"Cleanup EC2 Disk"** workflow
3. Click **"Run workflow"**

### Test Before Merging
Create a pull request:
```bash
git checkout -b feature-branch
git add .
git commit -m "New feature"
git push origin feature-branch
```
→ GitHub Actions automatically tests your code! ✅

## 📊 Monitoring

### View Workflow Status
- Go to **Actions** tab
- Click on any workflow run to see logs
- Green ✅ = Success
- Red ❌ = Failed (check logs)

### Deployment Logs
Each deployment shows:
- Container status
- Disk usage
- Docker system usage

### Cleanup Logs
Each cleanup shows:
- Before/after disk usage
- Freed space
- Docker cache cleaned

## 🔔 Notifications

### Enable Email Notifications
1. Go to your GitHub profile → Settings
2. Notifications → Actions
3. Enable "Send notifications for failed workflows"

### Enable Slack/Discord Notifications
Add to your workflow:
```yaml
- name: Notify Slack
  if: failure()
  uses: slackapi/slack-github-action@v1
  with:
    webhook-url: ${{ secrets.SLACK_WEBHOOK }}
    payload: |
      {
        "text": "Deployment failed! Check logs."
      }
```

## 🛡️ Security Best Practices

### Protect Secrets
- ✅ Never commit `aws-secrets/aws-key.pem` to Git
- ✅ Use GitHub Secrets for sensitive data
- ✅ Rotate SSH keys periodically

### Protect Main Branch
1. Go to Settings → Branches
2. Add branch protection rule for `main`
3. Enable:
   - Require pull request reviews
   - Require status checks to pass
   - Require branches to be up to date

## 🔄 Workflow Examples

### Deploy on Tag
```yaml
on:
  push:
    tags:
      - 'v*'
```

### Deploy on Schedule
```yaml
on:
  schedule:
    - cron: '0 0 * * 0'  # Weekly on Sunday
```

### Deploy with Approval
```yaml
jobs:
  deploy:
    environment:
      name: production
      url: http://32.193.245.111:8080
```

## 🐛 Troubleshooting

### Deployment Fails
1. Check workflow logs in Actions tab
2. Verify secrets are set correctly
3. Test SSH connection manually:
   ```bash
   ssh -i aws-secrets/aws-key.pem ubuntu@32.193.245.111
   ```

### Build Fails
1. Check Go version compatibility
2. Run locally: `go build`
3. Check Docker build: `docker build .`

### Cleanup Fails
1. Check disk space: `df -h`
2. Run cleanup manually:
   ```bash
   ssh -i aws-secrets/aws-key.pem ubuntu@32.193.245.111
   cd /home/ubuntu/goondvr
   ./scripts/cleanup-disk.sh
   ```

## 📚 Additional Resources

- [GitHub Actions Documentation](https://docs.github.com/en/actions)
- [Docker Build Push Action](https://github.com/docker/build-push-action)
- [SSH Action](https://github.com/appleboy/ssh-action)

## 🎉 You're All Set!

Your repository now has:
- ✅ Automatic deployment on push
- ✅ Automatic testing on PRs
- ✅ Daily disk cleanup
- ✅ Manual workflow triggers

**Just push to main and watch the magic happen!** 🚀
