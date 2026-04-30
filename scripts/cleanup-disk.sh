#!/bin/bash
# Automatic Disk Cleanup Script
# Cleans Docker cache, old images, unused volumes, and manages recordings

echo "🧹 Starting Disk Cleanup..."
echo ""

# Get current disk usage
DISK_USAGE=$(df / | tail -1 | awk '{print $5}' | sed 's/%//')
echo "📊 Current disk usage: ${DISK_USAGE}%"
echo ""

# 1. Clean Docker build cache (ALWAYS - no time filter)
echo "🗑️  Cleaning ALL Docker build cache..."
CACHE_BEFORE=$(sudo docker system df | grep "Build Cache" | awk '{print $4}')
sudo docker builder prune -af
CACHE_AFTER=$(sudo docker system df | grep "Build Cache" | awk '{print $4}')
echo "   Before: $CACHE_BEFORE → After: $CACHE_AFTER"
echo ""

# 2. Remove unused Docker images
echo "🗑️  Removing unused Docker images..."
sudo docker image prune -af --filter "until=72h"
echo ""

# 3. Remove unused volumes
echo "🗑️  Removing unused Docker volumes..."
sudo docker volume prune -f
echo ""

# 4. Clean old recordings (keep last 7 days)
echo "🗑️  Cleaning old recordings (older than 7 days)..."
VIDEOS_DIR="/home/ubuntu/goondvr/videos"
if [ -d "$VIDEOS_DIR" ]; then
    OLD_FILES=$(find "$VIDEOS_DIR" -name "*.mp4" -type f -mtime +7 2>/dev/null | wc -l)
    if [ $OLD_FILES -gt 0 ]; then
        echo "   Found $OLD_FILES old recordings"
        find "$VIDEOS_DIR" -name "*.mp4" -type f -mtime +7 -delete 2>/dev/null
        echo "   ✅ Deleted $OLD_FILES old recordings"
    else
        echo "   No old recordings to delete"
    fi
fi
echo ""

# 5. Clean system logs
echo "🗑️  Cleaning system logs..."
sudo journalctl --vacuum-time=7d
echo ""

# 6. Clean apt cache
echo "🗑️  Cleaning apt cache..."
sudo apt-get clean
sudo apt-get autoclean
echo ""

# Final disk usage
DISK_USAGE_AFTER=$(df / | tail -1 | awk '{print $5}' | sed 's/%//')
FREED=$((DISK_USAGE - DISK_USAGE_AFTER))

echo "========================================="
echo "✅ Cleanup Complete!"
echo "========================================="
echo "Disk usage: ${DISK_USAGE}% → ${DISK_USAGE_AFTER}%"
echo "Freed: ${FREED}% disk space"
echo ""

# Show current usage
df -h /
