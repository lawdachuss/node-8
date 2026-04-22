# 🚨 CRITICAL EDGE CASES FOUND

## Status: ⚠️ FIXES WILL NOT WORK PROPERLY - NEED CORRECTIONS

---

## 🔴 CRITICAL ISSUE #1: Monitor Flag Race Condition

**Location**: `.github/workflows/release.yml` line 378

### The Problem
```bash
MONITOR_FLAG="/tmp/monitor_$$_flag"
echo "0" > "$MONITOR_FLAG"
```

**Edge Cases:**

1. **Multiple channels with same PID**
   - `$$` is the shell PID, not goondvr PID
   - All 20 channels in the same job share the same `$$`
   - Flag file will be overwritten by other channels
   - **Result**: Channel A's monitor can kill Channel B's recording

2. **Flag file not accessible in subshell**
   - Monitor runs in background subshell `( ... ) &`
   - Subshell has different `$$`
   - Flag path will be different
   - **Result**: Monitor and main script use different flag files

3. **Cleanup happens before monitor checks**
   - Main script: `rm -f "$MONITOR_FLAG"`
   - Monitor (still running): tries to write to deleted file
   - **Result**: Monitor can't signal it killed the process

### The Fix Needed
```bash
# Use unique identifier with channel name and timestamp
MONITOR_FLAG="/tmp/monitor_${USERNAME}_${SITE}_$$_flag"
export MONITOR_FLAG  # Make available to subshell

# In monitor subshell, use exported variable
echo "1" > "$MONITOR_FLAG"
```

---

## 🔴 CRITICAL ISSUE #2: File Locking Won't Work

**Location**: `.github/workflows/release.yml` lines 915-940

### The Problem
```bash
exec 200>"$LOCK_FILE"
if flock -w 30 200; then
  # ... merge ...
  flock -u 200
  rm -f "$LOCK_FILE"
fi
```

**Edge Cases:**

1. **File descriptor leak**
   - `exec 200>` opens FD 200
   - If `flock` fails, FD 200 is never closed
   - After multiple failures, runs out of file descriptors
   - **Result**: "Too many open files" error

2. **Lock file not cleaned up on failure**
   - If script crashes between `flock` and `rm`
   - Lock file remains forever
   - Next run waits 30 seconds, then skips merge
   - **Result**: Lost database updates

3. **flock might not be available**
   - Some minimal Docker images don't have `flock`
   - Ubuntu runners should have it, but not guaranteed
   - **Result**: Command not found error

4. **Lock released too early**
   - `flock -u 200` releases lock
   - `mv "$DEST.tmp" "$DEST"` happens AFTER unlock
   - Another process can start merging before mv completes
   - **Result**: Still possible race condition

### The Fix Needed
```bash
# Check if flock is available
if ! command -v flock &> /dev/null; then
  echo "  ⚠️  flock not available, using simple merge"
  # Fallback to simple merge
fi

# Proper FD management
{
  flock -w 30 200 || exit 1
  # Merge happens while lock is held
  jq -s '...' "$DEST" "$REC_FILE" > "$DEST.tmp"
  mv "$DEST.tmp" "$DEST"
  # Lock automatically released when FD closes
} 200>"$LOCK_FILE"

# Cleanup lock file (safe even if locked)
rm -f "$LOCK_FILE" 2>/dev/null || true
```

---

## 🔴 CRITICAL ISSUE #3: Monitor Kills Wrong Process

**Location**: `.github/workflows/release.yml` lines 407-410

### The Problem
```bash
pkill -TERM -f "goondvr.*$USERNAME"
```

**Edge Cases:**

1. **Username substring matching**
   - Channel: `kim`
   - Channel: `kimchi`
   - `pkill -f "goondvr.*kim"` matches BOTH
   - **Result**: Recording for `kimchi` gets killed when `kim` stalls

2. **Special characters in username**
   - Username: `user.name` or `user[123]`
   - Regex interprets `.` as "any character" and `[]` as character class
   - Matches unintended processes
   - **Result**: Wrong processes killed

3. **No process found**
   - If goondvr already exited naturally
   - `pkill` returns non-zero
   - Script continues but monitor thinks it killed it
   - **Result**: Incorrect exit code handling

### The Fix Needed
```bash
# Escape special regex characters in username
SAFE_USERNAME=$(printf '%s\n' "$USERNAME" | sed 's/[.[\*^$]/\\&/g')

# Use word boundaries and exact matching
pkill -TERM -f "goondvr.*-u[[:space:]]\\+${SAFE_USERNAME}[[:space:]]" || true

# Or better: use PID file
echo $! > "/tmp/goondvr_${USERNAME}_${SITE}.pid"
# Later:
kill -TERM $(cat "/tmp/goondvr_${USERNAME}_${SITE}.pid" 2>/dev/null) 2>/dev/null || true
```

---

## 🟡 MEDIUM ISSUE #4: Stall Detection Still Has False Positives

**Location**: `.github/workflows/release.yml` lines 395-420

### The Problem
```bash
for f in videos/completed/*.ts videos/completed/*.mp4 videos/*.ts videos/*.mp4; do
  [[ -f "$f" ]] || continue
  FCOUNT=$((FCOUNT + 1))
  FSIZE=$(stat -c%s "$f" 2>/dev/null || echo 0)
  CURR=$((CURR + FSIZE))
done
```

**Edge Cases:**

1. **Glob expansion with no matches**
   - If no files exist, glob returns literal string
   - `[[ -f "videos/*.ts" ]]` is false, but loop still runs once
   - Not a bug, but inefficient

2. **File deleted during iteration**
   - FFmpeg conversion deletes source file
   - `stat` on deleted file returns 0
   - Size appears to shrink
   - **Result**: False positive stall detection

3. **File being written**
   - goondvr writes to file
   - `stat` reads size
   - OS hasn't flushed buffers yet
   - Size appears unchanged for 60+ seconds
   - **Result**: Still possible false positive

### The Fix Needed
```bash
# Use nullglob to handle no matches
shopt -s nullglob

# Check only completed files (not being written)
for f in videos/completed/*.{ts,mp4}; do
  [[ -f "$f" ]] || continue
  FCOUNT=$((FCOUNT + 1))
  FSIZE=$(stat -c%s "$f" 2>/dev/null || echo 0)
  CURR=$((CURR + FSIZE))
done

# Also check if goondvr process is still running
if ! pgrep -f "goondvr.*$USERNAME" > /dev/null; then
  # Process died, not stalled
  break
fi
```

---

## 🟡 MEDIUM ISSUE #5: Upload Timeout Calculation Overflow

**Location**: `.github/workflows/release.yml` lines 730-738

### The Problem
```bash
UL_TIMEOUT=$(awk "BEGIN{t=int($FSIZE/524288)+60; print t}")
if [[ "$UL_TIMEOUT" -gt 360 ]]; then
  UL_TIMEOUT=360
fi
```

**Edge Cases:**

1. **Integer overflow in awk**
   - File size: 50 GB = 53,687,091,200 bytes
   - `53687091200 / 524288 = 102,400`
   - Timeout: 102,400 minutes = 71 days
   - **Result**: Job hangs for days (hits 6-hour job timeout)

2. **Negative file size**
   - If `stat` fails, `FSIZE=0`
   - `0 / 524288 + 60 = 60` minutes
   - For a 0-byte file, this is too long
   - **Result**: Wastes time on empty files

3. **Timeout command syntax**
   - `timeout ${UL_TIMEOUT}m` expects integer
   - If awk returns float, command fails
   - **Result**: Upload runs without timeout

### The Fix Needed
```bash
# Sanity check file size
if [[ "$FSIZE" -lt 1000 ]]; then
  echo "  ⚠️  File too small ($FSIZE bytes), skipping upload"
  continue
fi

# Calculate with proper bounds
# Assume 0.5 MB/s minimum speed
UL_TIMEOUT=$(awk -v size="$FSIZE" 'BEGIN{
  t = int(size/524288) + 60;
  if (t < 5) t = 5;        # Minimum 5 minutes
  if (t > 360) t = 360;    # Maximum 6 hours
  print t
}')

echo "  Upload timeout: ${UL_TIMEOUT} minutes (file: ${FSIZE_MB} MB)"
```

---

## 🟡 MEDIUM ISSUE #6: Git Retry Loop Can Infinite Loop

**Location**: `.github/workflows/release.yml` lines 970-1010

### The Problem
```bash
MAX_RETRIES=5
for RETRY in $(seq 1 $MAX_RETRIES); do
  git commit -m "..." || true
  
  if git pull --rebase origin "..."; then
    if git push origin "..."; then
      break
    else
      git reset --soft HEAD~1 2>/dev/null || true
    fi
  fi
  
  sleep $(( RETRY * 5 ))
done
```

**Edge Cases:**

1. **Commit already exists**
   - `git commit` with `|| true` always succeeds
   - Even if there's nothing to commit
   - Creates empty commits
   - **Result**: Polluted git history

2. **Reset fails silently**
   - `git reset --soft HEAD~1 || true`
   - If reset fails, commit remains
   - Next iteration tries to commit again
   - **Result**: Duplicate commits

3. **Network timeout**
   - `git pull` hangs indefinitely
   - No timeout on git operations
   - Blocks for hours
   - **Result**: Job timeout

4. **Rebase creates conflicts**
   - If database files have real conflicts
   - `git rebase --abort` doesn't restore state
   - Next iteration fails again
   - **Result**: Infinite loop until MAX_RETRIES

### The Fix Needed
```bash
# Check if there's anything to commit
if git diff --staged --quiet; then
  echo "⚠️  No changes to commit"
  exit 0
fi

MAX_RETRIES=5
for RETRY in $(seq 1 $MAX_RETRIES); do
  # Commit only if not already committed
  if git log -1 --pretty=%B | grep -q "update recording database"; then
    echo "  Commit already exists, skipping"
  else
    git commit -m "chore: update recording database [skip ci]"
  fi
  
  # Pull with timeout
  if timeout 60 git pull --rebase origin "..."; then
    # Push with timeout
    if timeout 60 git push origin "..."; then
      echo "✓ Success"
      break
    fi
  fi
  
  # Clean state for retry
  git rebase --abort 2>/dev/null || true
  git reset --soft HEAD~1 2>/dev/null || true
  
  if [[ "$RETRY" -eq "$MAX_RETRIES" ]]; then
    echo "::error::Failed after $MAX_RETRIES attempts"
    # Don't exit 1 - let workflow continue
    exit 0
  fi
  
  sleep $(( RETRY * 5 ))
done
```

---

## 🟢 LOW ISSUE #7: Disk Space Check Timing

**Location**: `.github/workflows/release.yml` lines 226-235

### The Problem
```bash
AVAIL_GB=$(df -BG . | awk 'NR==2{gsub(/G/,""); print $4}')
if [[ "$AVAIL_GB" -lt 4 ]]; then
  exit 1
fi
```

**Edge Cases:**

1. **Rounding errors**
   - `df -BG` rounds to nearest GB
   - 3.9 GB shows as "3G"
   - Check fails even though there's enough space
   - **Result**: False negative

2. **Disk fills during build**
   - Check passes with 4.1 GB
   - Go build uses 1.5 GB
   - Docker image uses 1 GB
   - Recording starts with 1.6 GB (< 2 GB required)
   - **Result**: Recording fails anyway

### The Fix Needed
```bash
# Use MB for more precision
AVAIL_MB=$(df -BM . | awk 'NR==2{gsub(/M/,""); print $4}')
AVAIL_GB=$(awk "BEGIN{printf \"%.1f\", $AVAIL_MB/1024}")

echo "Disk available: ${AVAIL_GB}GB (${AVAIL_MB}MB)"

# Require 5 GB to be safe (build + docker + recording + buffer)
if [[ "$AVAIL_MB" -lt 5120 ]]; then
  echo "::error::Not enough disk space (${AVAIL_GB}GB < 5GB required)"
  echo "::error::Breakdown: Build ~1.5GB + Docker ~1GB + Recording ~2GB + Buffer ~0.5GB"
  exit 1
fi
```

---

## Summary of Edge Cases

| Issue | Severity | Will It Work? | Impact |
|-------|----------|---------------|--------|
| #1: Monitor flag race | 🔴 Critical | **NO** | Wrong channel killed |
| #2: File locking | 🔴 Critical | **MAYBE** | FD leak, race condition |
| #3: Process killing | 🔴 Critical | **NO** | Wrong processes killed |
| #4: Stall detection | 🟡 Medium | **MOSTLY** | Some false positives |
| #5: Upload timeout | 🟡 Medium | **MOSTLY** | Large files might hang |
| #6: Git retry loop | 🟡 Medium | **MOSTLY** | Might create duplicates |
| #7: Disk space check | 🟢 Low | **YES** | Minor rounding issues |

## Overall Assessment

**Current Status**: ⚠️ **WILL NOT WORK RELIABLY**

**Critical Issues**: 3 out of 7 will cause failures
- Monitor flag race: **Guaranteed to fail** with multiple channels
- Process killing: **Will kill wrong channels**
- File locking: **Might work, but has edge cases**

**Recommendation**: **MUST FIX** issues #1, #2, #3 before deploying
