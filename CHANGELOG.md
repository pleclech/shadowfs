# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **IPC mechanism for FUSE cache invalidation:**
  - Unix socket IPC server for communication between CLI restore operations and FUSE filesystem process
  - Automatic dirty file tracking: files restored via CLI are marked dirty, triggering `DIRECT_IO` for subsequent reads
  - Abstract Unix socket support for long paths (>100 chars) to avoid Unix socket path length limits (~108 chars on Linux)
  - Works in both daemon and non-daemon mode
  - Graceful degradation: restore operations continue even if IPC unavailable (FUSE process not running)

### Fixed
- **Git auto-versioning race conditions:**
  - Fixed hanging issue during rename operations when Git auto-versioning is enabled
  - Implemented pause/resume mechanism for auto-commit system during critical Git operations
  - Prevents conflicts between auto-commit timers and manual Git operations (rename commits, restore operations)
  - Maintains natural expiration order (FIFO) for pending commits during pause periods
  - Applied pause/resume to rename, deletion, and restore operations
- **FUSE cache invalidation after restore:**
  - CLI restore operations (`shadowfs version restore`) now properly invalidate FUSE kernel cache
  - Restored files are immediately visible with correct content through mount point
  - Eliminates stale cache issue where restored files showed old content until next read/write
- **Mkdir idempotent behavior:**
  - `Mkdir` now correctly handles idempotent operations (succeeds if directory already exists)
  - Fixed type replacement scenario: creating a directory after deleting a file with same name now works correctly
  - Test helper `ShouldCreateDir` now checks if directory exists before attempting creation (idempotent)
- **FUSE operation deadlocks:**
  - Fixed hang in `Flush()` by making `NotifyContent` non-blocking (called in goroutine)
  - Prevents deadlocks when cache invalidation notifications are sent from within FUSE operation handlers

### Changed
- `version restore` command: Refactored to support multiple `--path` flags instead of separate `--file`, `--dir`, `--workspace` flags
  - Usage: `shadowfs version restore --path file1.txt --path dir1/ --path dir2/file.txt <commit>`
  - If no `--path` is specified, restores entire workspace
- `restoreFileFromCommit`: Now preserves file permissions (mode) from Git commit and marks files as dirty via IPC for cache invalidation

## [1.1.0] - "Stability & Compatibility" - 2025-11-14

### Added
- `--allow-other` mount option to allow other users to access the mount (required for VS Code compatibility)
- `default_permissions` mount option for better permission handling and error messages
- VS Code compatibility documentation and troubleshooting guide
- Statfs implementation for filesystem statistics

### Changed
- Standardized flag naming convention to follow Unix standards:
  - Single-letter flags use single dash (`-v`, `-h`)
  - Multi-letter flags use double dash (`--version`, `--debug`, `--mount-point`)
  - Updated all CLI help text and documentation to reflect new convention
  - Version flag now accepts `-v` or `--version` (removed `-version` variant)

### Fixed
- Version flag handler now correctly rejects `-version` (single dash with multi-letter flag)
- VS Code compatibility issue: VS Code can now launch from shadowfs mounts when using `--allow-other` flag
- Documentation: Clarified flag positioning (flags must come before positional arguments)
- Documentation: Clarified `/etc/fuse.conf` requirements (only needed for non-root users)
- Major improvements to FUSE operations: Enhanced reliability and correctness of file operations, rename/move operations, directory listings, and cache management. All operations now properly maintain cache independence and handle edge cases correctly

## [1.0.0] - "First Light" - 2025-11-07

### Added
- **Core Filesystem Features:**
  - Basic overlay filesystem functionality with FUSE
  - Copy-on-write (COW) caching mechanism
  - Session persistence across mount/unmount cycles
  - Path validation and traversal protection
  - Comprehensive error handling

- **Git Auto-Versioning:**
  - Automatic Git versioning with idle-based commit strategy
  - Batch commits for multiple files edited together
  - Commit-on-unmount to prevent data loss
  - Change detection to skip commits for unchanged files
  - Async git operations (non-blocking)
  - Git repository management commands (`version list`, `version diff`, `version restore`, `version log`)

- **Daemon Mode:**
  - Background operation with process management
  - PID file management
  - Daemon lifecycle commands (`stop`, `list`, `info`)

- **Mount Status and Information:**
  - List all active mounts (`list` command)
  - Detailed statistics for mount points (`info` command)
  - Cache size and file count tracking
  - Git status integration

- **Sync Operations:**
  - Sync cache to source directory (`sync` command)
  - Backup and rollback functionality
  - Dry-run mode for preview
  - Selective sync by file or directory

- **Checkpoint Management:**
  - Manual checkpoint creation (`checkpoint` command)
  - File-specific checkpoints
  - Automatic checkpoint of all changed files

- **CLI Features:**
  - Version information flag (`--version`, `-v`)
  - Debug flags (`--debug`, `--debug-fuse`)
  - Environment variable support (`SHADOWFS_CACHE_DIR`, `SHADOWFS_LOG_LEVEL`, `SHADOWFS_DEBUG_FUSE`)
  - Comprehensive help system

### Performance
- Buffer pools for file operations (reduces allocations)
- Optimized path operations
- Async git operations don't block filesystem
- Efficient cache directory structure

### Security
- Path validation and traversal protection
- Comprehensive error handling
- Safe resource management
- Mount point validation

### Documentation
- Comprehensive README with examples
- API reference documentation
- Troubleshooting guide
- Development setup instructions

### Testing
- Comprehensive unit test suite
- Integration tests with FUSE
- Race condition detection tests
- Stress tests for reliability

[Unreleased]: https://github.com/pleclech/shadowfs/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/pleclech/shadowfs/releases/tag/v1.1.0
[1.0.0]: https://github.com/pleclech/shadowfs/releases/tag/v1.0.0

