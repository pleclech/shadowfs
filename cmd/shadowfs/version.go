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
		// Provide helpful error messages based on error type
		errMsg := err.Error()
		if strings.Contains(errMsg, "git repository directory not found") || strings.Contains(errMsg, "git repository not found") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Did you enable git with --auto-git flag? Try: shadowfs --auto-git %s <srcdir>", err, *mountPoint)
		} else if strings.Contains(errMsg, "git repository locked") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git operations may be in progress. Wait a moment and try again.", err)
		} else if strings.Contains(errMsg, "HEAD file not found") || strings.Contains(errMsg, "not recognized by Git") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git repository may not be fully initialized. Try restarting shadowfs.", err)
		}
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
	// Add --name-only by default to show changed files
	options := shadowfs.LogOptions{
		Format:   "%h %ad %s",
		Limit:    *limit,
		Paths:    expandedPaths,
		NameOnly: true, // Show file names by default
	}
	if err := gm.Log(options, os.Stdout); err != nil {
		if err == shadowfs.ErrNoCommits {
			fmt.Println("No commits found yet. Make some changes and they will be automatically committed.")
			return
		}
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
		// Provide helpful error messages based on error type
		errMsg := err.Error()
		if strings.Contains(errMsg, "git repository directory not found") || strings.Contains(errMsg, "git repository not found") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Did you enable git with --auto-git flag? Try: shadowfs --auto-git %s <srcdir>", err, *mountPoint)
		} else if strings.Contains(errMsg, "git repository locked") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git operations may be in progress. Wait a moment and try again.", err)
		} else if strings.Contains(errMsg, "HEAD file not found") || strings.Contains(errMsg, "not recognized by Git") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git repository may not be fully initialized. Try restarting shadowfs.", err)
		}
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
		if err == shadowfs.ErrNoCommits {
			fmt.Println("No commits found yet. Make some changes and they will be automatically committed.")
			return
		}
		log.Fatalf("Failed to run git diff: %v", err)
	}
}

func runVersionRestore(args []string) {
	initLogger()

	// Immediate output to verify command started
	log.Printf("Restore command started")

	fs := flag.NewFlagSet("version restore", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	force := fs.Bool("force", false, "Overwrite uncommitted changes")

	// Collect all --path flags manually (Go flag package doesn't support multiple flags with same name)
	// Filter out --path flags from args before parsing to avoid flag.Parse errors
	var paths []string
	var filteredArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--path" && i+1 < len(args) {
			// Handle --path value format
			value := args[i+1]
			// Support comma-separated values
			if strings.Contains(value, ",") {
				for _, p := range strings.Split(value, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						paths = append(paths, p)
					}
				}
			} else {
				paths = append(paths, value)
			}
			i++ // Skip the value (don't add either --path or its value to filteredArgs)
		} else if strings.HasPrefix(args[i], "--path=") {
			// Handle --path=value format
			value := strings.TrimPrefix(args[i], "--path=")
			if strings.Contains(value, ",") {
				for _, p := range strings.Split(value, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						paths = append(paths, p)
					}
				}
			} else {
				paths = append(paths, value)
			}
			// Don't add --path=value to filteredArgs
		} else {
			// Keep other args for flag parsing
			filteredArgs = append(filteredArgs, args[i])
		}
	}

	fs.Parse(filteredArgs)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	gm, err := shadowfs.GetGitRepository(*mountPoint)
	if err != nil {
		// Provide helpful error messages based on error type
		errMsg := err.Error()
		if strings.Contains(errMsg, "git repository directory not found") || strings.Contains(errMsg, "git repository not found") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Did you enable git with --auto-git flag? Try: shadowfs --auto-git %s <srcdir>", err, *mountPoint)
		} else if strings.Contains(errMsg, "git repository locked") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git operations may be in progress. Wait a moment and try again.", err)
		} else if strings.Contains(errMsg, "HEAD file not found") || strings.Contains(errMsg, "not recognized by Git") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git repository may not be fully initialized. Try restarting shadowfs.", err)
		}
		log.Fatalf("Failed to get git repository: %v", err)
	}

	// Get commit hash from positional args (first arg after flags, --path flags already filtered)
	if len(fs.Args()) == 0 {
		log.Fatal("Commit hash is required")
	}
	commitHashInput := fs.Args()[0]

	// Resolve commit hash (handles relative commits like HEAD~N, HEAD^, etc.)
	commitHash, err := gm.ResolveCommitHash(commitHashInput)
	if err != nil {
		log.Fatalf("Invalid commit hash or reference: %v", err)
	}

	// Validate commit hash exists
	if err := validateCommitHash(gm, commitHash); err != nil {
		log.Fatalf("Invalid commit hash: %v", err)
	}

	// Check for uncommitted changes if --force is not set
	if !*force {
		uncommitted, err := gm.StatusPorcelain()
		if err == nil && len(uncommitted) > 0 {
			log.Fatalf("Uncommitted changes detected. Use --force to overwrite.")
		}
	}

	// Use step-by-step restoration engine
	log.Printf("Starting restore operation: commit=%s, paths=%v", commitHash, paths)
	if err := gm.RestoreCommit(commitHash, paths); err != nil {
		log.Fatalf("Failed to restore: %v", err)
	}

	log.Printf("Restore operation completed successfully")
	if len(paths) == 0 {
		fmt.Printf("Successfully restored entire workspace to commit %s\n", commitHash)
	} else {
		fmt.Printf("Successfully restored %d path(s) to commit %s\n", len(paths), commitHash)
	}
}

func runVersionLog(args []string) {
	fs := flag.NewFlagSet("version log", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	pathFlags := fs.String("path", "", "Filter by file/directory path or glob pattern (can be used multiple times)")
	formatFlag := fs.String("format", "", "Custom format string (e.g., %H for full hash, %h for short hash)")
	limitFlag := fs.Int("limit", 0, "Limit number of commits shown")
	reverseFlag := fs.Bool("reverse", false, "Reverse order of commits")
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
		// Provide helpful error messages based on error type
		errMsg := err.Error()
		if strings.Contains(errMsg, "git repository directory not found") || strings.Contains(errMsg, "git repository not found") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Did you enable git with --auto-git flag? Try: shadowfs --auto-git %s <srcdir>", err, *mountPoint)
		} else if strings.Contains(errMsg, "git repository locked") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git operations may be in progress. Wait a moment and try again.", err)
		} else if strings.Contains(errMsg, "HEAD file not found") || strings.Contains(errMsg, "not recognized by Git") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Git repository may not be fully initialized. Try restarting shadowfs.", err)
		}
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
	// Add --name-only by default unless --oneline is used (oneline is more compact)
	options := shadowfs.LogOptions{
		Oneline:  *oneline,
		Reverse:  *reverseFlag,
		Graph:    *graph,
		Stat:     *stat,
		NameOnly: !*oneline, // Show file names unless oneline is requested
		Paths:    expandedPaths,
	}
	// Override format if specified
	if *formatFlag != "" {
		options.Format = *formatFlag
		options.NameOnly = false // Disable name-only when custom format is used
	}
	// Override limit if specified
	if *limitFlag > 0 {
		options.Limit = *limitFlag
	}
	if err := gm.Log(options, os.Stdout); err != nil {
		if err == shadowfs.ErrNoCommits {
			fmt.Println("No commits found yet. Make some changes and they will be automatically committed.")
			return
		}
		log.Fatalf("Failed to run git log: %v", err)
	}
}
