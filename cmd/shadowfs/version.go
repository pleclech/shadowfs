package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	shadowfs "github.com/pleclech/shadowfs/fs"
)

func runVersionCommand(args []string) {
	if len(args) == 0 {
		printVersionUsage()
		os.Exit(1)
	}

	command := args[0]
	switch command {
	case "list":
		runVersionList(args[1:])
	case "diff":
		runVersionDiff(args[1:])
	case "restore":
		runVersionRestore(args[1:])
	case "log":
		runVersionLog(args[1:])
	default:
		log.Fatalf("Unknown version command: %s\n\n%s", command, getVersionUsage())
	}
}

func printVersionUsage() {
	fmt.Print(getVersionUsage())
}

func getVersionUsage() string {
	return `Usage: shadowfs version <command> [options]

Commands:
  list      List version history (commits)
  diff      Show diff between versions
  restore   Restore files/directories/workspace to a previous version
  log       Enhanced version history with more options

Use "shadowfs version <command> --help" for command-specific help.
`
}

func runVersionList(args []string) {
	fs := flag.NewFlagSet("version list", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	pathFlags := fs.String("path", "", "Filter by file/directory path or glob pattern (can be used multiple times)")
	limit := fs.Int("limit", 0, "Limit number of commits shown")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	gm, err := shadowfs.GetGitRepository(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to get git repository: %v", err)
	}

	// Collect all path arguments (from flag and positional args after --)
	var pathPatterns []string
	if *pathFlags != "" {
		// Support comma-separated paths
		paths := strings.Split(*pathFlags, ",")
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p != "" {
				pathPatterns = append(pathPatterns, p)
			}
		}
	}
	// Also check for paths after -- separator
	if len(fs.Args()) > 0 {
		pathPatterns = append(pathPatterns, fs.Args()...)
	}

	// Expand glob patterns
	var expandedPaths []string
	if len(pathPatterns) > 0 {
		var err error
		expandedPaths, err = shadowfs.ExpandGlobPatterns(gm.GetWorkspacePath(), pathPatterns)
		if err != nil {
			log.Fatalf("Failed to expand patterns: %v", err)
		}
	}

	// Use GitManager's Log method
	options := shadowfs.LogOptions{
		Format: "%h %ad %s",
		Limit:  *limit,
		Paths:  expandedPaths,
	}
	if err := gm.Log(options, os.Stdout); err != nil {
		log.Fatalf("Failed to run git log: %v", err)
	}
}

func runVersionDiff(args []string) {
	fs := flag.NewFlagSet("version diff", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	pathFlags := fs.String("path", "", "Filter by file/directory path or glob pattern (can be used multiple times)")
	stat := fs.Bool("stat", false, "Show statistics only")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	gm, err := shadowfs.GetGitRepository(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to get git repository: %v", err)
	}

	// Get commit arguments from positional args (before any -- separator)
	var commitArgs []string
	var pathPatterns []string
	argsAfterDash := false
	for _, arg := range fs.Args() {
		if arg == "--" {
			argsAfterDash = true
			continue
		}
		if argsAfterDash {
			pathPatterns = append(pathPatterns, arg)
		} else {
			commitArgs = append(commitArgs, arg)
		}
	}

	// Process commit arguments
	if len(commitArgs) == 0 {
		// Show uncommitted changes
		commitArgs = []string{}
	} else if len(commitArgs) == 1 {
		// Diff against current working tree
		commitArgs = []string{commitArgs[0]}
	} else {
		// Diff between two commits
		commitArgs = []string{commitArgs[0], commitArgs[1]}
	}

	// Collect path patterns from flag
	if *pathFlags != "" {
		paths := strings.Split(*pathFlags, ",")
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p != "" {
				pathPatterns = append(pathPatterns, p)
			}
		}
	}

	// Expand glob patterns
	var expandedPaths []string
	if len(pathPatterns) > 0 {
		var err error
		expandedPaths, err = shadowfs.ExpandGlobPatterns(gm.GetWorkspacePath(), pathPatterns)
		if err != nil {
			log.Fatalf("Failed to expand patterns: %v", err)
		}
	}

	// Use GitManager's Diff method
	options := shadowfs.DiffOptions{
		Stat:       *stat,
		CommitArgs: commitArgs,
		Paths:      expandedPaths,
	}
	if err := gm.Diff(options, os.Stdout); err != nil {
		log.Fatalf("Failed to run git diff: %v", err)
	}
}

func runVersionRestore(args []string) {
	fs := flag.NewFlagSet("version restore", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	filePath := fs.String("file", "", "Restore single file")
	dirPath := fs.String("dir", "", "Restore directory tree")
	workspace := fs.Bool("workspace", false, "Restore entire workspace")
	force := fs.Bool("force", false, "Overwrite uncommitted changes")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	gm, err := shadowfs.GetGitRepository(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to get git repository: %v", err)
	}

	if len(fs.Args()) == 0 {
		log.Fatal("Commit hash is required")
	}
	commitHash := fs.Args()[0]

	// Validate commit hash
	if err := validateCommitHash(gm, commitHash); err != nil {
		log.Fatalf("Invalid commit hash: %v", err)
	}

	// Determine what to restore (precedence: file > dir > workspace)
	var paths []string
	if *filePath != "" {
		paths = []string{*filePath}
	} else if *dirPath != "" {
		paths = []string{*dirPath}
	} else if *workspace {
		// Restore entire workspace
		paths = []string{"."}
	} else {
		// Default to workspace if nothing specified
		paths = []string{"."}
	}

	// Use GitManager's Checkout method
	if err := gm.Checkout(commitHash, paths, *force, os.Stdout, os.Stderr); err != nil {
		log.Fatalf("Failed to restore: %v", err)
	}

	fmt.Printf("Successfully restored to commit %s\n", commitHash)
}

func runVersionLog(args []string) {
	fs := flag.NewFlagSet("version log", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	pathFlags := fs.String("path", "", "Filter by file/directory path or glob pattern (can be used multiple times)")
	oneline := fs.Bool("oneline", false, "One line per commit")
	graph := fs.Bool("graph", false, "Show commit graph")
	stat := fs.Bool("stat", false, "Show file statistics")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	gm, err := shadowfs.GetGitRepository(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to get git repository: %v", err)
	}

	// Collect all path arguments
	var pathPatterns []string
	if *pathFlags != "" {
		paths := strings.Split(*pathFlags, ",")
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p != "" {
				pathPatterns = append(pathPatterns, p)
			}
		}
	}
	// Also check for paths after -- separator
	if len(fs.Args()) > 0 {
		pathPatterns = append(pathPatterns, fs.Args()...)
	}

	// Expand glob patterns
	var expandedPaths []string
	if len(pathPatterns) > 0 {
		var err error
		expandedPaths, err = shadowfs.ExpandGlobPatterns(gm.GetWorkspacePath(), pathPatterns)
		if err != nil {
			log.Fatalf("Failed to expand patterns: %v", err)
		}
	}

	// Use GitManager's Log method
	options := shadowfs.LogOptions{
		Oneline: *oneline,
		Graph:   *graph,
		Stat:    *stat,
		Paths:   expandedPaths,
	}
	if err := gm.Log(options, os.Stdout); err != nil {
		log.Fatalf("Failed to run git log: %v", err)
	}
}
