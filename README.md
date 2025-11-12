# ShadowFS

A FUSE-based versioned overlay filesystem that provides versioning and caching capabilities for Linux filesystems. It creates a shadow copy of files in a cache layer while preserving the original source files, allowing for safe modifications and version tracking.

## Table of Contents

- [Features](#features)
- [Quick Start](#quick-start)
  - [Requirements](#requirements)
  - [Installation](#installation)
  - [Basic Usage](#basic-usage)
- [Complete Workflow](#complete-workflow)
- [Advanced Usage Examples](#advanced-usage-examples)
- [Git Auto-Versioning](#git-auto-versioning)
- [Important Notes](#important-notes)
- [Architecture](#architecture)
- [Sync to Source](#sync-to-source)
- [Version Management](#version-management)
- [Daemon Management](#daemon-management)
- [Mount Status and Information](#mount-status-and-information)
- [Security](#security)
- [Development](#development)
  - [Project Structure](#project-structure)
  - [Building](#building)
  - [Releases](#releases)
  - [Testing](#testing)
- [Configuration](#configuration)
- [API Reference](#api-reference)
- [Performance](#performance)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [License](#license)
- [Changelog](#changelog)
- [Support](#support)
- [Acknowledgments](#acknowledgments)

## Features

- **Overlay Architecture**: Shadow/caching overlay filesystem using FUSE
- **Git Auto-Versioning**: Automatic Git commits with idle-based commit strategy
- **Daemon Mode**: Run filesystem in background with process management
- **Mount Status**: List and inspect active mounts with detailed statistics
- **Copy-on-Write**: Files copied to cache only on first write for efficiency
- **Session Persistence**: Maintains cache across mount/unmount cycles
- **Performance Optimized**: Buffer pools, async operations, no reflection overhead
- **Security**: Path validation and traversal protection
- **Linux Optimized**: Built specifically for Linux with syscall optimizations
- **Crash Resistant**: Comprehensive error handling and safe resource management

## Quick Start

### Requirements

#### Runtime Requirements (for using the binary)

- **Linux**: Fully supported and tested
  - **FUSE library**: Runtime library (`libfuse.so`) must be installed
    - Ubuntu/Debian: `sudo apt-get install fuse3` (or `fuse` for older versions)
    - CentOS/RHEL: `sudo yum install fuse3` (or `fuse` for older versions)
  - **Root/sudo privileges**: Required for mounting filesystems
  - **Extended Attributes (xattr) support**: Required for file deletion tracking
    - Supported filesystems: ext4, xfs, btrfs (fully supported)
    - Most modern Linux filesystems support xattr
    - **Not supported**: FAT32, some network filesystems without xattr support

- **macOS**: **EXPERIMENTAL/UNTESTED** - Code compiles but requires testing on macOS hardware
  - **macFUSE**: Must be installed (download from https://osxfuse.github.io/ or https://github.com/macfuse/macfuse)
  - **Extended Attributes**: macOS supports extended attributes natively
  - **Git**: Required for version management commands
  - **Note**: This implementation has not been tested on macOS hardware. Use at your own risk and please report any issues.

- **Windows**: Use via WSL (Windows Subsystem for Linux)
  - The Linux version works perfectly in WSL environments
  - Install WSL2 and follow Linux installation instructions
  - Native Windows support is not currently implemented

- **Git**: Required for version management commands (`version`, `checkpoint`, `sync`)
  - Used for viewing history, creating checkpoints, and syncing operations
  - Must be installed and available in PATH

#### Development Requirements (for building from source)

- **Go 1.19 or later**: Required to compile the project
- **FUSE development headers**: Required for building
  - **Linux**: 
    - Ubuntu/Debian: `sudo apt-get install libfuse-dev`
    - CentOS/RHEL: `sudo yum install fuse-devel`
  - **macOS**: macFUSE development headers (included with macFUSE installation)
- **Git**: Required for cloning the repository and version control

### Installation

#### Using Pre-built Binary (Recommended)

1. Download the latest release from the [releases page](https://github.com/pleclech/shadowfs/releases)
2. Extract the binary: `tar -xzf shadowfs_linux_amd64.tar.gz`
3. Move to PATH: `sudo mv shadowfs /usr/local/bin/`
4. Install FUSE runtime library (if not already installed):
   ```bash
   # Ubuntu/Debian
   sudo apt-get install fuse3
   
   # CentOS/RHEL
   sudo yum install fuse3
   ```

#### Building from Source

**Linux:**
```bash
# Install development dependencies
# Ubuntu/Debian
sudo apt-get install libfuse-dev

# CentOS/RHEL
sudo yum install fuse-devel

# Clone the repository
git clone https://github.com/pleclech/shadowfs.git
cd shadowfs

# Build the binary
go build -o shadowfs ./cmd/shadowfs
```

**macOS (EXPERIMENTAL/UNTESTED):**
```bash
# Install macFUSE (includes development headers)
# Download from https://osxfuse.github.io/ or https://github.com/macfuse/macfuse
# Or use Homebrew:
brew install macfuse

# Clone the repository
git clone https://github.com/pleclech/shadowfs.git
cd shadowfs

# Build for macOS
go build -o shadowfs ./cmd/shadowfs

# Or cross-compile from Linux:
GOOS=darwin GOARCH=amd64 go build -o shadowfs-darwin-amd64 ./cmd/shadowfs
GOOS=darwin GOARCH=arm64 go build -o shadowfs-darwin-arm64 ./cmd/shadowfs
```

**Note**: macOS support is experimental and untested. The code compiles but requires testing on macOS hardware.

### Basic Usage

```bash
# Create source and mount directories
mkdir -p /home/user/source /home/user/mount

# Mount the overlay filesystem (foreground mode)
sudo ./shadowfs /home/user/mount /home/user/source

# Or mount as daemon (background mode)
sudo ./shadowfs -daemon /home/user/mount /home/user/source

# Use the filesystem normally
ls /home/user/mount
echo "new content" > /home/user/mount/newfile.txt
cat /home/user/mount/newfile.txt

# Unmount when done
# For foreground mode:
sudo umount /home/user/mount

# For daemon mode:
shadowfs stop --mount-point /home/user/mount

# List all active mounts
shadowfs list

# Get detailed information about a mount
shadowfs info --mount-point /home/user/mount
```

## Complete Workflow

This section demonstrates the complete workflow from mounting to syncing changes back to the source, including version management and rollback capabilities.

### Step-by-Step Complete Workflow

```bash
# ============================================
# STEP 1: Mount and Work Safely
# ============================================

# Mount the filesystem with Git auto-versioning enabled
sudo ./shadowfs -auto-git /mnt/overlay /path/to/source

# Make changes in the cache (source directory is untouched)
vim /mnt/overlay/file1.txt
vim /mnt/overlay/file2.txt
echo "new content" > /mnt/overlay/newfile.txt

# Files are automatically committed after idle period (default: 30s)
# Check Git history
shadowfs version list --mount-point /mnt/overlay

# ============================================
# STEP 2: Go Back in Time (Version Management)
# ============================================

# View version history
shadowfs version list --mount-point /mnt/overlay

# Show diff between versions
shadowfs version diff --mount-point /mnt/overlay HEAD~2 HEAD

# Show diff for specific files using glob patterns
shadowfs version diff --mount-point /mnt/overlay HEAD~1 HEAD --path "*.txt"

# Restore a file to a previous version (if needed)
shadowfs version restore --mount-point /mnt/overlay --file file1.txt <commit-hash>

# Or restore entire workspace to a previous state (--workspace flag is optional)
shadowfs version restore --mount-point /mnt/overlay --workspace <commit-hash>
# Or simply (workspace is the default):
shadowfs version restore --mount-point /mnt/overlay <commit-hash>

# Create a manual checkpoint before major changes
shadowfs checkpoint --mount-point /mnt/overlay

# ============================================
# STEP 3: Preview Changes Before Syncing
# ============================================

# Preview what would be synced to source (dry-run)
shadowfs sync --mount-point /mnt/overlay --dry-run

# This shows:
# - Files that would be added/modified
# - Files that would be deleted
# - Conflicts (if source was modified while mounted)

# ============================================
# STEP 4: Apply Changes to Source Directory
# ============================================

# Sync cache changes to source (automatic backup created)
shadowfs sync --mount-point /mnt/overlay

# Output shows:
# - Backup ID created
# - Files synced
# - Files deleted
# - Any conflicts or errors

# Sync specific file or directory only
shadowfs sync --mount-point /mnt/overlay --file path/to/file.txt
shadowfs sync --mount-point /mnt/overlay --dir path/to/directory

# Force sync even if conflicts detected (use with caution)
shadowfs sync --mount-point /mnt/overlay --force

# ============================================
# STEP 5: Rollback Source if Needed
# ============================================

# List all backups
shadowfs backups list

# List backups for this mount point
shadowfs backups list --mount-point /mnt/overlay

# View backup details
shadowfs backups info <backup-id>

# Rollback source to a previous backup
shadowfs sync --mount-point /mnt/overlay --rollback --backup-id <backup-id>

# ============================================
# STEP 6: Cleanup and Maintenance
# ============================================

# Cleanup old backups (older than 7 days)
shadowfs backups cleanup --older-than 7

# Cleanup backups for specific mount point
shadowfs backups cleanup --mount-point /mnt/overlay --older-than 7

# Delete specific backup
shadowfs backups delete <backup-id>

# Unmount when done
sudo umount /mnt/overlay
```

### Workflow Summary

1. **Mount**: `sudo ./shadowfs -auto-git /mnt/overlay /path/to/source`
   - Source directory is read-only and protected
   - All changes go to cache layer
   - Git auto-versioning tracks all changes

2. **Work**: Make changes normally in `/mnt/overlay`
   - Files automatically committed after idle period
   - Full Git history available

3. **Version Management**: Use `version` commands to:
   - View history: `version list`
   - Compare versions: `version diff`
   - Restore files: `version restore`
   - Create checkpoints: `checkpoint`

4. **Preview**: Use `sync --dry-run` to see what would change

5. **Sync**: Apply changes to source with `sync`
   - Automatic backup created
   - Atomic operations
   - Conflict detection

6. **Rollback**: Use `sync --rollback` if needed
   - Restore source from backup
   - Selective rollback (only changed files)

7. **Cleanup**: Manage backups with `backups` commands

### Key Safety Features

- **Source Protection**: Source directory is never modified during work
- **Automatic Backups**: Created before every sync
- **Version History**: Full Git history in cache
- **Conflict Detection**: Warns if source was modified
- **Atomic Operations**: Files synced atomically
- **Rollback Capability**: Restore source to any backup
- **Transaction Logging**: Track all changes for selective rollback

### Advanced Usage Examples

#### Mounting Read-Only Source Directory

`shadowfs` allows you to overlay a read-only source directory (e.g., a mounted CD-ROM, an immutable snapshot) with a writable layer.
All modifications (creations, edits, deletions) made through the `shadowfs` mount point will be stored in its cache, leaving the original read-only source completely untouched.

**Important**: Since the source is read-only, you **cannot sync** changes back to it using the `shadowfs sync` command. All changes will effectively exist only within the `shadowfs` cache and its Git history.

```bash
# Example: Mount a read-only source directory (e.g., from a CD-ROM or an immutable snapshot)
sudo mount -o ro /dev/sr0 /mnt/cdrom
sudo ./shadowfs /mnt/overlay /mnt/cdrom

# Now you can modify files in /mnt/overlay. These changes are stored in shadowfs's cache
# and do not affect the original read-only /mnt/cdrom.
vim /mnt/overlay/config.txt

# Attempting to sync will fail if the source (/mnt/cdrom) is truly read-only.
# All changes will remain in the shadowfs cache and its Git history.
./shadowfs sync --mount-point /mnt/overlay
```

#### Git Auto-Versioning Workflow

```bash
# Mount with git autocommit enabled
sudo ./shadowfs -auto-git /mnt/workspace /path/to/source

# Work on files - changes are automatically committed after idle period
vim /mnt/workspace/file1.txt
vim /mnt/workspace/file2.txt

# Check git history using version command (recommended)
shadowfs version list --mount-point /mnt/workspace

# Or check git history directly
cd /mnt/workspace
git --git-dir=.gitofs/.git log --oneline

# View changes
git --git-dir=.gitofs/.git diff HEAD~1

# Revert to previous version if needed
git --git-dir=.gitofs/.git checkout HEAD~1 -- file1.txt

# Unmount - all pending changes are committed automatically
sudo umount /mnt/workspace
```

#### Cache Cleanup and Management

```bash
# Check cache size (default location)
du -sh ~/.shadowfs/

# Check cache size (custom location)
du -sh $SHADOWFS_CACHE_DIR/

# List all cache sessions (default location)
ls -la ~/.shadowfs/

# List all cache sessions (custom location)
ls -la $SHADOWFS_CACHE_DIR/

# View cache contents for a specific mount
ls -R ~/.shadowfs/<mount-id>/.root/

# Clean up old/unused cache directories
# First, identify mounts you're no longer using
ls -la ~/.shadowfs/

# Remove specific cache (safe - source files are untouched)
rm -rf ~/.shadowfs/<old-mount-id>/

# Clean up all caches (WARNING: loses all cached modifications)
rm -rf ~/.shadowfs/*/
```

#### Multiple Mount Points

```bash
# Mount different source directories to different mount points
sudo ./shadowfs /mnt/project1 /path/to/project1
sudo ./shadowfs /mnt/project2 /path/to/project2

# Each mount has its own independent cache
# Cache locations:
# ~/.shadowfs/<hash-of-mount1+source1>/
# ~/.shadowfs/<hash-of-mount2+source2>/
```

#### Unmounting and Remounting

```bash
# Unmount filesystem (foreground mode)
sudo umount /mnt/overlay

# Or stop daemon (daemon mode)
shadowfs stop --mount-point /mnt/overlay

# Modify source directory (safe - filesystem is unmounted)
cp newfile.txt /path/to/source/
rm /path/to/source/oldfile.txt

# Remount - cache persists, sees updated source
sudo ./shadowfs /mnt/overlay /path/to/source

# Your previous cache modifications are still there
# New files from source are now visible
ls /mnt/overlay
```

#### Daemon Mode

Run shadowfs in the background for long-running mounts:

```bash
# Start filesystem as daemon
sudo ./shadowfs -daemon /mnt/overlay /path/to/source

# Start with Git auto-versioning enabled
sudo ./shadowfs -daemon -auto-git /mnt/overlay /path/to/source

# Daemon runs in background - terminal is free
# PID file is stored in ~/.shadowfs/daemons/<mount-id>.pid

# Stop daemon gracefully
shadowfs stop --mount-point /mnt/overlay

# Check if daemon is running (if PID file exists)
ls ~/.shadowfs/daemons/

# Daemon automatically:
# - Commits pending Git changes on shutdown
# - Unmounts filesystem cleanly
# - Removes PID file on exit
```

**Daemon Mode Benefits:**
- Run filesystem in background without keeping terminal open
- Manage multiple mounts independently
- Automatic cleanup on shutdown
- PID file tracking for process management

**PID File Location:**
- Stored in `~/.shadowfs/daemons/<mount-id>.pid`
- Contains JSON with PID, mount point, source directory, and start time
- Automatically cleaned up on graceful shutdown

### Git Auto-Versioning

Enable automatic Git versioning to track all file changes:

```bash
# Enable git autocommit with default 30 second idle timeout
sudo ./shadowfs -auto-git /home/user/mount /home/user/source

# Customize idle timeout (commits after 60 seconds of inactivity)
sudo ./shadowfs -auto-git -git-idle-timeout=60s /home/user/mount /home/user/source

# Enable debug output
sudo ./shadowfs -debug -auto-git /home/user/mount /home/user/source
```

**Git Auto-Versioning Features:**
- Automatic commits after idle period (default: 30 seconds)
- Batch commits for multiple files edited together
- Commit-on-unmount to prevent data loss
- Change detection (skips commits for unchanged files)
- Non-blocking async operations (doesn't slow down filesystem)
- Git repository stored in `.gitofs/` directory (separate from source)
- Concise commit messages showing file count (e.g., "Auto-commit: 3 files")
- File names shown in `version list` output for easy identification

## Important Notes

### ⚠️ Source Directory Modifications

**WARNING**: Do not modify the source directory while the overlay filesystem is mounted.

The cache layer maintains its own independent state. If files are added, removed, or modified in the source directory while the filesystem is mounted:

- **Changes may not be visible** until remount
- **Cache modifications may conflict** with source changes
- **Data loss or corruption** may occur
- **Inconsistent state** between cache and source

**Best Practice**: 
1. Unmount the filesystem before modifying the source directory
2. Make your changes to the source directory
3. Remount the filesystem to see the updated source state

**Why this restriction exists:**
The overlay filesystem is designed with cache independence as a core principle. The cache acts as a "safe net" where you can work without compromising the real source directory. Detecting and reconciling source changes during mount would require complex conflict resolution logic and could result in losing your cache work. By keeping the source read-only during mount, we ensure cache modifications are never lost.

## Architecture

### Overlay Design

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   Source FS     │     │  Overlay FS      │     │   Cache FS      │
│  (read-only)    │───▶│   (FUSE)          │───▶│ (read/write)    │
│                 │     │                  │     │                 │
│ file1.txt       │     │ file1.txt        │     │ file1.txt       │
│ file2.txt       │     │ file2.txt        │     │ file2.txt       │
│ dir/            │     │ dir/             │     │ dir/            │
└─────────────────┘     └──────────────────┘     └─────────────────┘
```

### Cache Structure

The filesystem stores cached files in `~/.shadowfs/<mount-id>/` by default, or in the custom cache directory if specified via `-cache-dir` flag or `SHADOWFS_CACHE_DIR` environment variable:

```
<cache-base-dir>/<sha256-hash>/
├── .target          # Contains source directory path
├── .root/          # Cache layer
│   ├── file1.txt    # Modified files
│   ├── newfile.txt  # New files
│   └── dir/         # Directory structure
└── .gitofs/        # Git repository (if auto-versioning enabled)
    ├── HEAD
    ├── config
    └── objects/
```

**Cache Directory Configuration:**
- **Default**: `~/.shadowfs/` (in user's home directory)
- **Custom**: Use `-cache-dir` flag or `SHADOWFS_CACHE_DIR` environment variable
- **Priority**: Command-line flag > Environment variable > Default
- Cache directory is created automatically if it doesn't exist
- Must be writable and on a filesystem supporting extended attributes

**Git Repository:**
- Located in `.gitofs/` directory (not `.git` to avoid conflicts)
- Automatically initialized when `-auto-git` flag is used
- Tracks all changes in the cache layer
- Commits are created automatically after idle periods
- All pending changes are committed on unmount

## Git Auto-Versioning

The filesystem includes optional automatic Git versioning to track all file changes with full history.

### How It Works

1. **Idle-Based Commits**: Files are automatically committed after a configurable idle period (default: 30 seconds)
2. **Batch Commits**: Multiple files edited together are committed in a single commit
3. **Change Detection**: Only files with actual changes are committed
4. **Async Operations**: Git operations run in background, not blocking filesystem operations
5. **Commit-on-Unmount**: All pending changes are committed before unmounting to prevent data loss
6. **Concise Commit Messages**: Commit messages show file count (e.g., "Auto-commit: 3 files") for scalability
7. **File Visibility**: The `version list` command shows changed files by default using `--name-only`

### Commit Message Format

Commit messages are designed to be concise and scalable:
- **Single file**: `Auto-commit: 1 file`
- **Multiple files**: `Auto-commit: N files` (where N is the count)

File names are shown separately in the `version list` output, not in the commit message, allowing the system to scale to any number of files without cluttering commit messages.

### Architecture

```
File Write Operation
    ↓
Activity Tracker (starts/resets timer)
    ↓
[User continues editing... timer resets on each write]
    ↓
[User stops editing... timer expires after idle timeout]
    ↓
Git Manager (async commit queue)
    ↓
git add + git commit (background goroutine)
```

### Configuration

**Command-Line Flags:**
- `-auto-git`: Enable automatic Git versioning
- `-git-idle-timeout`: Set idle timeout before commit (default: 30s)
- `-git-safety-window`: Safety window delay after last write before committing (default: 5s)
- `-cache-dir`: Custom cache directory (overrides `SHADOWFS_CACHE_DIR`)
- `-daemon`: Run filesystem as daemon in background
- `-debug`: Enable debug output for troubleshooting

**Git Repository:**
- Created automatically in `.gitofs/` directory
- Separate from source directory (doesn't interfere with existing repos)
- Uses default Git user: "Overlay FS" <overlay@localhost>
- Can be configured with standard Git commands

### Example Workflow

```bash
# Mount with git autocommit
sudo ./shadowfs -auto-git /mnt/overlay /path/to/source

# Edit files normally
vim /mnt/overlay/file1.txt
vim /mnt/overlay/file2.txt

# Files are automatically committed after 30 seconds of inactivity
# Check git history using version command (recommended)
shadowfs version list --mount-point /mnt/overlay

# Or check git history directly
cd /mnt/overlay
git --git-dir=.gitofs/.git log --oneline

# Unmount - all pending changes are committed automatically
sudo umount /mnt/overlay
```

### Benefits

- **Full History**: Every change is tracked with Git commits
- **Time Travel**: Revert to any previous version using `git checkout`
- **No Data Loss**: Commit-on-unmount ensures changes are saved
- **Efficient**: Batch commits and change detection reduce commit noise
- **Non-Intrusive**: Git operations don't slow down filesystem performance

## Sync to Source

Once you're satisfied with your changes in the cache, you can sync them back to the source directory. The sync operation includes automatic backup creation and rollback capabilities for safety.

### How It Works

1. **Backup Creation**: Before syncing, an automatic backup is created (unless disabled)
2. **File Discovery**: All files in the cache are discovered, including deleted files
3. **Conflict Detection**: Checks if source files were modified while mounted
4. **Atomic Operations**: Files are synced atomically (temp file + rename)
5. **Transaction Logging**: All operations are logged for rollback capability

### Sync Command

```bash
# Basic sync (with automatic backup)
shadowfs sync --mount-point /mnt/overlay

# Dry run (preview changes without syncing)
shadowfs sync --mount-point /mnt/overlay --dry-run

# Sync without backup (dangerous - not recommended)
shadowfs sync --mount-point /mnt/overlay --no-backup

# Force sync (ignore conflicts and warnings)
shadowfs sync --mount-point /mnt/overlay --force

# Sync specific file or directory
shadowfs sync --mount-point /mnt/overlay --file path/to/file.txt
shadowfs sync --mount-point /mnt/overlay --dir path/to/directory
```

**Options:**
- `--mount-point`: Mount point path (required)
- `--dry-run`: Preview changes without actually syncing
- `--backup`: Create backup before syncing (default: true)
- `--no-backup`: Skip backup creation (dangerous)
- `--force`: Force sync even if conflicts detected (use with caution)
- `--file`: Sync only specific file
- `--dir`: Sync only specific directory tree
- `--rollback`: Rollback to a previous backup (requires `--backup-id`)
- `--backup-id`: Backup ID for rollback operation

**What gets synced:**
- All modified files in cache (copy-on-write files)
- All new files created in cache
- All deleted files (removed from source)
- Only files that differ from source are synced

### Rollback

If something goes wrong during sync, you can rollback to the backup:

```bash
# Rollback to a specific backup
shadowfs sync --mount-point /mnt/overlay --rollback --backup-id <backup-id>
```

### Backup Management

Manage your backups with the `backups` command:

```bash
# List all backups
shadowfs backups list

# List backups for specific mount point
shadowfs backups list --mount-point /mnt/overlay

# Show backup details (mount point, timestamp, file count, size)
shadowfs backups info <backup-id>

# Delete a backup (with confirmation prompt)
shadowfs backups delete <backup-id>

# Delete without confirmation prompt
shadowfs backups delete --force <backup-id>

# Cleanup old backups (older than 30 days by default)
shadowfs backups cleanup

# Cleanup backups older than 7 days
shadowfs backups cleanup --older-than 7

# Cleanup for specific mount point
shadowfs backups cleanup --mount-point /mnt/overlay --older-than 7
```

**Backup Commands:**

1. **`backups list`**: List all backups or backups for a specific mount point
   - Shows backup ID, mount point, timestamp, and file count
   - Use `--mount-point` to filter by mount point

2. **`backups info <backup-id>`**: Show detailed information about a backup
   - Shows mount point, source directory, timestamp, file count, and total size
   - Backup ID format: `YYYYMMDD-HHMMSS-hexstring`

3. **`backups delete <backup-id>`**: Delete a specific backup
   - Prompts for confirmation unless `--force` is used
   - Use `--force` to skip confirmation prompt

4. **`backups cleanup`**: Remove old backups
   - Default: removes backups older than 30 days
   - Use `--older-than <days>` to specify custom age threshold
   - Use `--mount-point` to cleanup backups for specific mount point only

### Backup Storage

Backups are stored in `~/.shadowfs/backups/` by default, or in the directory specified by `SHADOWFS_BACKUP_DIR` environment variable.

Each backup is stored in a directory named with format: `{timestamp}-{mount-id}`

```
~/.shadowfs/backups/
├── 20250110-150405-abc123def456/
│   ├── .backup-info          # Backup metadata
│   └── [backed up files...]
└── 20250110-160230-def456ghi789/
    ├── .backup-info
    └── [backed up files...]
```

### Safety Features

- **Automatic Backups**: Created before every sync (can be disabled)
- **Conflict Detection**: Warns if source files were modified while mounted
- **Atomic Operations**: Files synced atomically to prevent corruption
- **Transaction Logging**: All changes tracked for selective rollback
- **Verification**: Copy integrity verified before finalizing sync

### Example Workflow

```bash
# 1. Mount filesystem
sudo ./shadowfs -auto-git /mnt/overlay /path/to/source

# 2. Make changes in cache
vim /mnt/overlay/file1.txt
vim /mnt/overlay/file2.txt

# 3. Preview what would be synced
shadowfs sync --mount-point /mnt/overlay --dry-run

# 4. Sync changes to source (with automatic backup)
shadowfs sync --mount-point /mnt/overlay

# 5. If something went wrong, rollback
shadowfs sync --mount-point /mnt/overlay --rollback --backup-id <backup-id>

# 6. Cleanup old backups
shadowfs backups cleanup --older-than 7
```

## Version Management

The filesystem includes comprehensive version management commands for viewing history, comparing versions, and restoring files.

### Version Commands

#### version list

The `version list` command shows commit history with file names by default, making it easy to see what changed in each commit:

```bash
# List version history (shows file names by default)
shadowfs version list --mount-point /mnt/overlay

# Example output:
# 501a569 2025-11-12 Auto-commit: 3 files
# 	file1.txt
# 	file2.go
# 	readme.md
# 8b2c3d4 2025-11-12 Auto-commit: 1 file
# 	config.json

# List history for specific file
shadowfs version list --mount-point /mnt/overlay --path file.txt

# List history for multiple files (comma-separated)
shadowfs version list --mount-point /mnt/overlay --path "file1.txt,file2.txt"

# List history using glob patterns
shadowfs version list --mount-point /mnt/overlay --path "*.txt"
shadowfs version list --mount-point /mnt/overlay --path "src/**/*.go"

# List history for multiple patterns
shadowfs version list --mount-point /mnt/overlay --path "*.txt,*.go"

# Limit number of commits shown
shadowfs version list --mount-point /mnt/overlay --limit 10
```

**Options:**
- `--mount-point`: Mount point path (required)
- `--path`: Filter by file/directory path or glob pattern (comma-separated for multiple patterns)
- `--limit`: Limit number of commits shown (0 = no limit)

#### version diff

The `version diff` command shows differences between versions or uncommitted changes:

```bash
# Show uncommitted changes (no commit arguments)
shadowfs version diff --mount-point /mnt/overlay

# Diff against working tree (single commit)
shadowfs version diff --mount-point /mnt/overlay HEAD

# Show diff between two commits
shadowfs version diff --mount-point /mnt/overlay <commit1> <commit2>
shadowfs version diff --mount-point /mnt/overlay HEAD~1 HEAD

# Show diff for specific file or pattern
shadowfs version diff --mount-point /mnt/overlay HEAD~1 HEAD --path "*.txt"
shadowfs version diff --mount-point /mnt/overlay HEAD~1 HEAD --path "src/**/*.go"

# Show statistics only (file names and change counts)
shadowfs version diff --mount-point /mnt/overlay HEAD~1 HEAD --stat
```

**Options:**
- `--mount-point`: Mount point path (required)
- `--path`: Filter by file/directory path or glob pattern
- `--stat`: Show statistics only (file names and change counts, no actual diff)

**Behavior:**
- **No commits**: Shows uncommitted changes in working tree
- **One commit**: Shows diff between that commit and current working tree
- **Two commits**: Shows diff between the two commits

#### version restore

The `version restore` command restores files, directories, or the entire workspace to a previous version:

```bash
# Restore single file to previous version
shadowfs version restore --mount-point /mnt/overlay --file path/to/file.txt <commit>

# Restore directory tree to previous version
shadowfs version restore --mount-point /mnt/overlay --dir path/to/directory <commit>

# Restore entire workspace (--workspace flag is optional, defaults to workspace)
shadowfs version restore --mount-point /mnt/overlay --workspace <commit>
# Or simply (workspace is the default):
shadowfs version restore --mount-point /mnt/overlay <commit>

# Force restore even if there are uncommitted changes
shadowfs version restore --mount-point /mnt/overlay --file file.txt <commit> --force
```

**Options:**
- `--mount-point`: Mount point path (required)
- `--file`: Restore single file
- `--dir`: Restore directory tree
- `--workspace`: Restore entire workspace (default if no file/dir specified)
- `--force`: Overwrite uncommitted changes (use with caution)

**Note**: If neither `--file`, `--dir`, nor `--workspace` is specified, the entire workspace is restored by default.

#### version log

The `version log` command provides enhanced version history with more formatting options than `version list`:

```bash
# Basic log (similar to version list but with more options)
shadowfs version log --mount-point /mnt/overlay

# Filter by pattern
shadowfs version log --mount-point /mnt/overlay --path "*.txt"

# Show commit graph (visual representation of branches/merges)
shadowfs version log --mount-point /mnt/overlay --graph

# Show file statistics (lines added/removed per file)
shadowfs version log --mount-point /mnt/overlay --stat

# Combine options
shadowfs version log --mount-point /mnt/overlay --path "*.txt" --stat --graph

# Compact one-line output (no file lists)
shadowfs version log --mount-point /mnt/overlay --oneline
```

**Options:**
- `--mount-point`: Mount point path (required)
- `--path`: Filter by file/directory path or glob pattern
- `--oneline`: One line per commit (compact output without file lists)
- `--graph`: Show commit graph (visual representation)
- `--stat`: Show file statistics (lines added/removed)

**When to use `version log` vs `version list`:**
- Use `version list` for quick overview with file names (default behavior)
- Use `version log` when you need:
  - Visual commit graph (`--graph`)
  - File change statistics (`--stat`)
  - Compact one-line output (`--oneline`)
  - More Git log formatting options

### Empty Repository Handling

If you run `version list` on a repository with no commits yet, you'll see a friendly message:

```
No commits found yet. Make some changes and they will be automatically committed.
```

This replaces the technical git error message for a better user experience.

### Pattern Matching

The `--path` flag supports:
- **Single files**: `--path file.txt`
- **Multiple files**: `--path "file1.txt,file2.txt"` (comma-separated)
- **Glob patterns**: `--path "*.txt"`, `--path "src/**/*.go"`
- **Directories**: `--path "src/"` (shows history for all files in directory)

Patterns are expanded relative to the cache workspace root. If a pattern matches no files, Git will handle it gracefully (no error, just no results).

### Checkpoint Command

Create manual checkpoints for known-good states. Unlike auto-commits, checkpoints are created immediately when you run the command:

```bash
# Create checkpoint for current state (all changed files)
shadowfs checkpoint --mount-point /mnt/overlay

# Create checkpoint for specific file
shadowfs checkpoint --mount-point /mnt/overlay --file path/to/file.txt
```

**Options:**
- `--mount-point`: Mount point path (required)
- `--file`: Create checkpoint for specific file (optional, defaults to all changed files)

**Behavior:**
- If no `--file` is specified, all files with uncommitted changes are checkpointed
- If no uncommitted changes exist, the command reports "No uncommitted changes found"
- Checkpoints are created immediately (unlike auto-commits which wait for idle period)
- Checkpoints use commit message "Manual checkpoint"

**Use Cases:**
- Create a checkpoint before making risky changes
- Mark a known-good state for easy restoration
- Create checkpoints at project milestones

## Daemon Management

ShadowFS supports running as a background daemon process, allowing you to manage long-running mounts without keeping a terminal open.

### Starting a Daemon

```bash
# Start filesystem as daemon
sudo ./shadowfs -daemon /mnt/overlay /path/to/source

# Start with Git auto-versioning enabled
sudo ./shadowfs -daemon -auto-git /mnt/overlay /path/to/source

# Start with custom cache directory
sudo ./shadowfs -daemon -cache-dir /custom/cache /mnt/overlay /path/to/source
```

When started with `-daemon` flag:
- Process forks and runs in background
- Parent process exits immediately
- PID file is created in `~/.shadowfs/daemons/<mount-id>.pid`
- Daemon continues running until stopped

### Stopping a Daemon

```bash
# Stop daemon by mount point
shadowfs stop --mount-point /mnt/overlay

# The stop command:
# - Finds daemon process by mount point
# - Sends SIGTERM for graceful shutdown
# - Commits pending Git changes (if enabled)
# - Unmounts filesystem cleanly
# - Removes PID file
```

### PID File Management

PID files are stored in `~/.shadowfs/daemons/` directory:

```bash
# List all active mounts (recommended method)
shadowfs list

# Alternative: List PID files directly
ls ~/.shadowfs/daemons/

# View PID file contents (JSON format)
cat ~/.shadowfs/daemons/<mount-id>.pid

# PID file contains:
# {
#   "pid": 12345,
#   "mount_point": "/mnt/overlay",
#   "source_dir": "/path/to/source",
#   "started_at": "2025-01-01T12:00:00Z"
# }
```

**Automatic Cleanup:**
- PID files are automatically removed on graceful shutdown
- Stale PID files (process died) are detected and cleaned up
- Manual cleanup: `rm ~/.shadowfs/daemons/<mount-id>.pid`

### Daemon Logging

When running in daemon mode, all stdout and stderr output is redirected to a log file for debugging:

```bash
# Log files are stored alongside PID files
ls ~/.shadowfs/daemons/
# Output: <mount-id>.pid  <mount-id>.log

# View daemon logs
tail -f ~/.shadowfs/daemons/<mount-id>.log

# Check log file location using info command
shadowfs info --mount-point /mnt/overlay
# Shows: Log File: ~/.shadowfs/daemons/<mount-id>.log
```

**Log File Location:**
- Stored in `~/.shadowfs/daemons/<mount-id>.log`
- Created automatically when daemon starts
- Contains all stdout/stderr output from the daemon process
- Useful for debugging issues like auto-commit not working
- Logs are appended (not overwritten) on each daemon start

### Daemon Mode Benefits

- **Background Operation**: Run without keeping terminal open
- **Process Management**: Easy start/stop with mount point
- **Multiple Mounts**: Manage several mounts independently
- **Automatic Cleanup**: PID files cleaned up on shutdown
- **Graceful Shutdown**: Commits pending changes before exit

### Troubleshooting Daemon Mode

```bash
# Check if daemon is running
ps aux | grep shadowfs

# Check PID file
cat ~/.shadowfs/daemons/<mount-id>.pid

# Force stop if daemon is unresponsive
kill -TERM <pid>

# Clean up stale PID file manually
rm ~/.shadowfs/daemons/<mount-id>.pid
```

## Mount Status and Information

ShadowFS provides commands to list and inspect active mounts, making it easy to monitor your filesystem mounts and gather detailed statistics.

### Listing Active Mounts

The `list` command shows all active mounts (both foreground and daemon processes):

```bash
# List all active mounts
shadowfs list
```

**Output Format:**
```
Mount Point                  Source Directory              Status     PID      Started
/mnt/overlay                /path/to/source              daemon     12345    2025-01-01 12:00:00
/home/user/mount            /home/user/source            active     -        -
```

**Status Values:**
- `active`: Foreground mount (running in terminal)
- `daemon`: Background daemon process (running in background)
- `stale`: Process not running but PID file exists (needs cleanup)

**Use Cases:**
- Quick overview of all active mounts
- Identify daemon processes and their PIDs
- Find stale mounts that need cleanup
- Verify mount status before operations

### Getting Detailed Mount Information

The `info` command provides detailed statistics for a specific mount point:

```bash
# Get detailed information for a mount point
shadowfs info --mount-point /mnt/overlay
```

**Output Includes:**

1. **Mount Information:**
   - Mount point path
   - Source directory
   - Cache directory location
   - Status (active/daemon/stale)
   - Process ID (for daemon processes)
   - Start time

2. **Cache Statistics:**
   - Total cache size (human-readable format)
   - File count
   - Directory count

3. **Git Status** (if enabled):
   - Whether Git auto-versioning is enabled
   - Last commit hash and timestamp
   - Last commit message
   - List of uncommitted files (if any)

**Example Output:**
```
Mount Point: /mnt/overlay
Source Directory: /path/to/source
Cache Directory: /home/user/.shadowfs/<mount-id>
Status: daemon
PID: 12345
Started: 2025-01-01 12:00:00

Cache Statistics:
  Size: 125.3 MB
  Files: 1,234
  Directories: 56

Git Status:
  Enabled: Yes
  Last Commit: 501a569 (2025-01-01 12:30:00)
  Message: Auto-commit: Modified 3 files
  Uncommitted Changes: 2 file(s)
    - config.txt
    - data.json
```

**Use Cases:**
- Check cache size and file counts
- Verify Git status and recent commits
- Identify uncommitted changes
- Troubleshoot mount issues
- Monitor mount health and status

### Integration with Other Commands

The status commands work seamlessly with other ShadowFS operations:

```bash
# List all mounts
shadowfs list

# Get info for a specific mount
shadowfs info --mount-point /mnt/overlay

# Use mount point for other commands
shadowfs version list --mount-point /mnt/overlay
shadowfs checkpoint --mount-point /mnt/overlay
shadowfs sync --mount-point /mnt/overlay --dry-run

# Stop daemon if needed
shadowfs stop --mount-point /mnt/overlay
```

### Troubleshooting with Status Commands

```bash
# Check if mount is active
shadowfs list | grep /mnt/overlay

# Verify mount details
shadowfs info --mount-point /mnt/overlay

# Check for uncommitted changes
shadowfs info --mount-point /mnt/overlay | grep "Uncommitted"

# Identify stale mounts
shadowfs list | grep stale
```

## Security

The filesystem includes several security features:

- **Path Validation**: All file paths are validated to prevent traversal attacks
- **Path Traversal Protection**: Blocks `..` sequences and absolute paths in file operations
- **Safe Error Handling**: Comprehensive error handling prevents crashes
- **Resource Management**: Proper cleanup of file descriptors and resources
- **Permission Preservation**: Maintains original file permissions from source

## Development

### Project Structure

```
shadowfs/
├── cmd/
│   └── shadowfs/              # Main binary source
│       ├── main.go            # Main entry point
│       ├── version.go         # Version management commands
│       ├── checkpoint.go      # Checkpoint command
│       ├── sync.go            # Sync and backup commands
│       └── validation.go     # Input validation helpers
├── fs/                         # Core filesystem implementation
│   ├── shadow.go              # Core ShadowNode struct and main FUSE operations
│   ├── shadow_fuse_directory.go # FUSE directory operations (Mkdir, Rmdir, Opendir, Readdir)
│   ├── shadow_fuse_readwrite.go # FUSE read/write operations (Read, Write, CopyFileRange)
│   ├── shadow_helpers.go      # Helper functions (buffer pools, file copy)
│   ├── shadowfs_linux.go      # Linux-specific filesystem code
│   ├── git_manager.go         # Git auto-versioning manager
│   ├── activity_tracker.go    # File activity tracking for git commits
│   ├── version.go             # Version management helpers
│   ├── sync.go                # Sync operations
│   ├── dirstream.go           # Directory streaming interface
│   ├── dirstream_linux.go     # Linux directory streaming implementation
│   ├── file_operations.go     # File operation helpers
│   ├── path_manager.go        # Path rebasing and caching
│   ├── logger.go              # Logging infrastructure
│   ├── constants.go           # Global constants (HomeName, RootName)
│   ├── test_helpers.go        # Test infrastructure
│   ├── shadow_test.go         # Unit tests
│   ├── shadow_fuse_test.go    # FUSE operation tests
│   ├── fuse_functionality_test.go  # FUSE functionality tests
│   ├── git_manager_test.go    # Git manager tests
│   ├── git_integration_test.go # Git integration tests
│   ├── version_test.go        # Version management tests
│   ├── sync_test.go           # Sync operation tests
│   ├── stress_test.go         # Performance/stress tests
│   ├── benchmark_test.go      # Performance benchmarks
│   ├── xattr/                 # Extended attributes package
│   │   └── xattr.go          # XAttr types and operations
│   ├── utils/                 # Utility functions package
│   │   ├── validation.go     # Path validation utilities
│   │   └── permissions.go    # Permission management utilities
│   ├── rootinit/              # Root initialization package
│   │   ├── setup.go          # Directory and file creation utilities
│   │   └── root.go           # Mount point validation and root creation
│   ├── pathutil/              # Path utilities package
│   │   └── path.go           # Path rebasing utilities
│   └── cache/                 # Cache/mirror operations package
│       └── mirror.go          # Cache mirroring and file copy operations
├── filesystem_integration_test.go  # Full FUSE integration tests
├── LICENSE                     # MIT License
├── go.mod                      # Go module definition
├── go.sum                      # Go module checksums
├── .goreleaser.yml            # GoReleaser configuration
├── .github/                   # GitHub Actions workflows
│   └── workflows/
│       └── release.yml        # Automated release workflow
└── README.md                   # This file
```

**Package Organization:**

The filesystem implementation is organized into logical packages for better separation of concerns:

- **`fs/xattr/`**: Extended attributes handling for file deletion tracking
- **`fs/utils/`**: Reusable utilities for validation and permissions management
- **`fs/rootinit/`**: Root filesystem initialization and mount point validation
- **`fs/pathutil/`**: Path rebasing utilities for converting between source, cache, and mount paths
- **`fs/cache/`**: Cache mirroring operations and file copy utilities
- **`fs/shadow_fuse_directory.go`**: FUSE directory operations (Mkdir, Rmdir, Opendir, Readdir)
- **`fs/shadow_fuse_readwrite.go`**: FUSE read/write operations (Read, Write, CopyFileRange)
- **`fs/shadow.go`**: Core ShadowNode struct and remaining FUSE operations (Lookup, Getattr, Setattr, Create, Open, Unlink, Symlink, Readlink, Link, Rename)

### Building

```bash
# Build for development
go build -o shadowfs ./cmd/shadowfs

# Build with race detection
go build -race -o shadowfs ./cmd/shadowfs

# Build for production
go build -ldflags="-s -w" -o shadowfs ./cmd/shadowfs
```

### Releases

ShadowFS uses [GoReleaser](https://goreleaser.com) for automated releases. The project is configured to build for Linux amd64 architecture and publish releases to GitHub.

#### Prerequisites

- GoReleaser installed: `go install github.com/goreleaser/goreleaser@latest`
- GitHub Personal Access Token with `repo` scope (for local releases)
- For cross-compilation: Install cross-compiler toolchains (optional, for future ARM builds)

#### Configuration

The `.goreleaser.yml` file is configured for GitHub:
- **Owner**: `pleclech`
- **Name**: `shadowfs`

#### Testing Builds Locally

```bash
# Test build for current architecture (snapshot)
goreleaser build --snapshot --clean --single-target

# Test full release build (all architectures, snapshot)
goreleaser release --snapshot --skip=publish --clean

# Validate configuration
goreleaser check
```

#### Creating a Release

```bash
# 1. Tag your release
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0

# 2. Set GitHub token (for local releases)
export GITHUB_TOKEN=your_token_here

# 3. Run GoReleaser
goreleaser release
```

#### GitHub Actions Integration

The project includes a GitHub Actions workflow (`.github/workflows/release.yml`) for automated releases. When you push a tag (e.g., `v1.0.0`), the workflow automatically:

1. Builds binary for Linux amd64 architecture
2. Creates a GitHub release
3. Uploads artifacts and checksums

The workflow is triggered automatically on tag pushes:
```bash
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

**Note**: The `GITHUB_TOKEN` environment variable is automatically available in GitHub Actions workflows, so you don't need to set it manually.

#### Cross-Compilation

**Note**: Currently, releases are built only for Linux amd64. Cross-compilation for ARM architectures requires proper C cross-compiler toolchains and is not currently included in automated releases.

For future ARM builds, you would need to install cross-compiler toolchains:

```bash
# Ubuntu/Debian
sudo apt-get install gcc-arm-linux-gnueabihf gcc-aarch64-linux-gnu gcc-i686-linux-gnu

# Or use Docker with pre-configured toolchains
# See: https://github.com/goreleaser/goreleaser-cross
```

**Important**: CGO is required because `go-fuse/v2` depends on libfuse (C library). Cross-compilation requires proper C cross-compiler toolchains.

#### Release Artifacts

Each release includes:
- Binary for Linux amd64 architecture
- Archive file (tar.gz) with LICENSE and README.md
- SHA256 checksums file
- Release notes with changelog

### Testing

The project includes comprehensive test coverage:

```bash
# Run all unit tests
go test ./fs -v

# Run FUSE functionality tests
go test ./fs -run TestFUSE -v

# Run stress tests
go test ./fs -run TestStress -v

# Run integration tests (requires FUSE setup)
go test -tags=integration -v

# Run tests with coverage
go test -cover ./fs

# Run benchmarks
go test -bench=. ./fs

# Run race condition tests
go test -race ./fs
```

#### Test Categories

- **Unit Tests**: Core functionality and utilities
  - Path rebasing and caching
  - Extended attributes handling
  - File operations
  - Git manager operations
  - Activity tracking
  - Logger functionality
  - Path utilities
  - Permission and validation utilities
  - Pattern filtering and glob expansion

- **FUSE Tests**: FUSE operation testing
  - File create, read, write, delete
  - Directory operations
  - Attribute handling

- **Stress Tests**: Performance and edge cases
  - Concurrent operations
  - Large file handling
  - Memory usage

- **Integration Tests**: Full filesystem mounting and CLI commands
  - End-to-end filesystem operations (29 tests in `filesystem_integration_test.go`)
  - CLI command integration tests (12 tests in `cmd/shadowfs/cli_test.go`)
  - Cache persistence
  - Git auto-versioning
  - Version commands (list, diff, restore, log)
  - Sync and backup operations
  - Daemon mode operations

#### Test Requirements

- **FUSE**: Integration tests require FUSE support and root/sudo privileges
- **Permissions**: Some tests require write access to `/tmp`
- **Git**: Git auto-versioning tests require `git` command in PATH
- **Extended Attributes**: Tests require filesystem with xattr support

#### Running Tests in CI/CD

```bash
# Run tests without integration tests (no root required)
go test ./fs -v

# Run with race detection
go test -race ./fs

# Generate coverage report
go test -coverprofile=coverage.out ./fs
go tool cover -html=coverage.out
```

## Configuration

### Environment Variables

- `SHADOWFS_CACHE_DIR`: Base directory for cache storage (overridden by `-cache-dir` flag if set)
- `HOME`: User home directory (for default cache location `~/.shadowfs`)
- `TMPDIR`: Temporary directory location

### Command-Line Options

```bash
# Basic mount
sudo ./shadowfs <mountpoint> <srcdir>

# Enable Git auto-versioning
sudo ./shadowfs -auto-git <mountpoint> <srcdir>

# Customize git idle timeout
sudo ./shadowfs -auto-git -git-idle-timeout=60s <mountpoint> <srcdir>

# Enable debug output
sudo ./shadowfs -debug <mountpoint> <srcdir>

# Combine options
sudo ./shadowfs -debug -auto-git -git-idle-timeout=45s <mountpoint> <srcdir>

# Use custom cache directory
sudo ./shadowfs -cache-dir=/mnt/fast-ssd/cache <mountpoint> <srcdir>

# Use environment variable for cache directory
export SHADOWFS_CACHE_DIR=/mnt/fast-ssd/cache
sudo ./shadowfs <mountpoint> <srcdir>
```

**Available Flags:**
- `-auto-git`: Enable automatic Git versioning
- `-git-idle-timeout`: Idle timeout before commit (e.g., `30s`, `1m`, `2m30s`, default: `30s`)
- `-git-safety-window`: Safety window delay after last write before committing (e.g., `5s`, `10s`, default: `5s`)
- `-debug`: Enable FUSE debug output
- `-cache-dir`: Custom cache directory (default: `~/.shadowfs`, or `$SHADOWFS_CACHE_DIR` environment variable)

**Environment Variables:**
- `SHADOWFS_CACHE_DIR`: Base directory for cache storage (overridden by `-cache-dir` flag)
- `HOME`: User home directory (for default cache location)
- `TMPDIR`: Temporary directory location

### FUSE Mount Options

The filesystem supports standard FUSE mount options:

```bash
# Allow other users to access
sudo ./shadowfs -o allow_other /mount /source

# Set default permissions
sudo ./shadowfs -o default_permissions /mount /source
```

## API Reference

### Core Operations

#### File Operations

- `Create()`: Create new files
  - Returns: `(*fs.Inode, fs.FileHandle, uint32, syscall.Errno)`
  - Creates empty file in cache, copies source content on first write (copy-on-write)
  - Example: `inode, fh, flags, errno := node.Create(ctx, "file.txt", flags, mode, &out)`

- `Open()`: Open existing files
  - Returns: `(fs.FileHandle, uint32, syscall.Errno)`
  - Read-only: Accesses source directly if not in cache
  - Write mode: Creates cache file if needed, copies source on first write
  - Example: `fh, flags, errno := node.Open(ctx, flags)`

- `Read()`: Read file contents
  - Returns: `(fuse.ReadResult, syscall.Errno)`
  - Reads from cache if file is cached, otherwise from source
  - Example: `result, errno := node.Read(ctx, fh, dest, offset)`

- `Write()`: Write file contents
  - Returns: `(uint32, syscall.Errno)` - bytes written
  - Performs copy-on-write if cache file is empty and source exists
  - Example: `written, errno := node.Write(ctx, fh, data, offset)`

- `Unlink()`: Delete files
  - Returns: `syscall.Errno`
  - Marks file as deleted using extended attributes (soft delete)
  - Example: `errno := node.Unlink(ctx, "file.txt")`

#### Directory Operations

- `Mkdir()`: Create directories
  - Returns: `(*fs.Inode, syscall.Errno)`
  - Creates directory in cache with same permissions as source (if exists)
  - Example: `inode, errno := node.Mkdir(ctx, "dirname", mode, &out)`

- `Rmdir()`: Remove directories
  - Returns: `syscall.Errno`
  - Removes directory from cache (must be empty)
  - Example: `errno := node.Rmdir(ctx, "dirname")`

- `Readdir()`: List directory contents
  - Returns: `(fs.DirStream, syscall.Errno)`
  - Merges entries from cache and source, filters deleted files
  - Example: `stream, errno := node.Readdir(ctx)`

- `Lookup()`: Find files/directories
  - Returns: `(*fs.Inode, syscall.Errno)`
  - Checks cache first, then source
  - Example: `inode, errno := node.Lookup(ctx, "filename", &out)`

#### Metadata Operations

- `Getattr()`: Get file attributes
  - Returns: `syscall.Errno`
  - Returns attributes from cache if available, otherwise from source
  - Example: `errno := node.Getattr(ctx, fh, &out)`

- `Setattr()`: Set file attributes
  - Returns: `syscall.Errno`
  - Modifies attributes on cached files only
  - Example: `errno := node.Setattr(ctx, fh, in, &out)`

- `Getxattr()`: Get extended attributes
  - Returns: `([]byte, syscall.Errno)`
  - Reads `user.shadow-fs` extended attribute for file status
  - Example: `data, errno := node.Getxattr(ctx, "user.shadow-fs", &out)`

- `Setxattr()`: Set extended attributes
  - Returns: `syscall.Errno`
  - Sets `user.shadow-fs` extended attribute for file status tracking
  - Example: `errno := node.Setxattr(ctx, "user.shadow-fs", data, flags)`

### Path Management

#### Rebase Operations

```go
// Convert source path to cache path
cachePath := node.RebasePathUsingCache(sourcePath)

// Convert cache path to source path
sourcePath := node.RebasePathUsingSrc(cachePath)

// Convert mount point path to cache path
cachePath := node.RebasePathUsingMountPoint(mountPath)
```

#### Cache Status

```go
// Check if path is in cache
isCached := node.IsCached(path)

// Get full paths
sourceFullPath := node.FullPath(false)  // Source path
cacheFullPath := node.FullPath(true)    // Cache path
```

### Thread Safety

- **FUSE Operations**: Each FUSE operation is handled sequentially per inode by the go-fuse library
- **Path Manager**: Uses `sync.Map` for concurrent-safe path caching
- **Activity Tracker**: Uses `sync.RWMutex` for thread-safe file activity tracking
- **Git Manager**: Uses channels and goroutines for async commit processing
- **Buffer Pool**: Uses `sync.Pool` for thread-safe buffer reuse

### Error Handling

All operations return `syscall.Errno` values:
- `0` (syscall.EOK): Success
- `syscall.ENOENT`: File or directory not found
- `syscall.EPERM`: Permission denied
- `syscall.EEXIST`: File already exists
- `syscall.EINVAL`: Invalid argument
- Use `fs.ToErrno(err)` to convert Go errors to syscall.Errno

## Performance

### Benchmarks

Typical performance characteristics:

- **Path rebasing**: ~2-3ms for 1000 operations
- **File creation**: ~10-20ms per file
- **Directory listing**: ~5-10ms for 100 entries
- **Memory usage**: ~40KB for 2000 path operations
- **Git commits**: Async, non-blocking (background processing)

### Optimization Features

- **Buffer Pools**: Reusable 64KB buffers for file operations (reduces allocations)
- **Path Caching**: Efficient path rebasing with cache
- **No Reflection**: Removed reflection overhead for better performance
- **Async Git Operations**: Git commits don't block filesystem operations
- **Change Detection**: Skips commits for unchanged files
- **Batch Commits**: Multiple files committed together for efficiency
- **Copy-on-Write**: Files copied to cache only on first write
- **Lazy Loading**: Files mirrored on-demand
- **Minimal Syscalls**: Optimized system call usage

## Troubleshooting

### Common Issues

#### Permission Denied

```bash
# Ensure proper permissions
sudo usermod -a -G fuse $USER
# Or run with sudo
sudo ./shadowfs /mount /source
```

#### Mount Fails

```bash
# Check FUSE is available
lsmod | grep fuse
# Install FUSE if missing
sudo modprobe fuse
```

#### Performance Issues

```bash
# Use debug mode to identify bottlenecks
sudo ./shadowfs -debug /mount /source

# Monitor cache size
du -sh ~/.shadowfs/

# Check git repository size (if using auto-versioning)
du -sh ~/.shadowfs/*/.gitofs/.git/
```

#### Git Auto-Versioning Issues

```bash
# Check if git is available
which git

# Verify git repository was created
ls -la <mountpoint>/.gitofs/.git/

# Check git logs using version command (recommended - shows file names)
shadowfs version list --mount-point <mountpoint>

# Or check git logs directly
cd <mountpoint>
git --git-dir=.gitofs/.git log --oneline

# If commits aren't happening, check idle timeout
# Increase timeout if files are being edited frequently
sudo ./shadowfs -auto-git -git-idle-timeout=60s /mount /source
```

#### Xattr-Related Errors

```bash
# Check if your filesystem supports extended attributes
getfattr -d /path/to/test/file 2>&1 | head -1

# If you get "Operation not supported", your filesystem doesn't support xattr
# You'll need to use a different filesystem (ext4, xfs, btrfs recommended)

# Test xattr support
touch /tmp/test_xattr
setfattr -n user.test -v "test" /tmp/test_xattr
if [ $? -eq 0 ]; then
    echo "xattr supported"
    setfattr -x user.test /tmp/test_xattr
else
    echo "xattr NOT supported - filesystem incompatible"
fi
rm /tmp/test_xattr
```

#### Cache Corruption Recovery

```bash
# If cache becomes corrupted, you can safely remove it
# Cache is stored in <cache-base-dir>/<mount-id>/
# Default location: ~/.shadowfs/<mount-id>/

# List all cache directories (default location)
ls -la ~/.shadowfs/

# List all cache directories (custom location)
ls -la $SHADOWFS_CACHE_DIR/

# Remove specific cache (replace <mount-id> with actual hash)
rm -rf ~/.shadowfs/<mount-id>/

# Remove all caches (WARNING: loses all cached modifications)
rm -rf ~/.shadowfs/*/

# After cleanup, remount to create fresh cache
sudo ./shadowfs /mount /source
```

### Debug Mode

Enable detailed logging:

```bash
# Enable FUSE debug output
sudo ./shadowfs -debug /mount /source

# Enable filesystem logging
export SHADOWFS_DEBUG=1
sudo ./shadowfs /mount /source
```

**Note**: The `-debug` flag enables FUSE debug output. For filesystem-specific debug logging, use the `SHADOWFS_DEBUG` environment variable.

## Contributing

### Development Setup

```bash
# Clone repository
git clone https://github.com/pleclech/shadowfs.git
cd shadowfs

# Install dependencies
go mod download

# Run tests to verify setup
go test ./fs -v
go test -tags=integration -v
```

### Code Style

- Follow Go formatting conventions (`gofmt`)
- Use explicit error handling
- Document public functions
- Add tests for new features
- Use build tags for platform-specific code

### Submitting Changes

1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure all tests pass
5. Submit a pull request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

Contact: Twitter [@pleclech](https://twitter.com/pleclech)

## Changelog

### v1.0.0 (Latest) - "First Light"

**Initial Release:**
- Basic overlay filesystem functionality
- Comprehensive test suite
- Linux support with FUSE

**Git Auto-Versioning:**
- Automatic Git versioning with idle-based commits
- Batch commits for multiple files edited together
- Commit-on-unmount to prevent data loss
- Change detection to skip commits for unchanged files
- Async git operations (non-blocking)

**Performance:**
- Buffer pools for file operations (reduces allocations)
- Optimized path operations
- Async git operations don't block filesystem

**Security:**
- Path validation and traversal protection
- Comprehensive error handling
- Safe resource management

**Features:**
- Daemon mode for background operation
- Mount status and information commands
- Sync to source with automatic backups
- Version management (list, diff, restore)
- Backup management and rollback capabilities

## Support

For issues and questions:

- Create an issue on GitHub
- Check existing issues for solutions
- Provide detailed error messages and system information

## Acknowledgments

- [go-fuse](https://github.com/hanwen/go-fuse) - FUSE bindings for Go
- Linux FUSE documentation and community
