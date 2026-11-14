// Command perforce2lore replays a Perforce depot path into a Lore repository,
// one revision per changelist, credited to the changelist's author.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/KaosSpectrum/perforce2lore/internal/importer"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitFailed        = 1
	exitMissingAuthor = 2
)

func main() {
	configPath := flag.String("config", "perforce2lore.json", "settings file")
	watch := flag.Bool("watch", false, "keep running and import new changelists as they are submitted")
	dryRun := flag.Bool("dry-run", false, "list pending changelists and their authors without importing")
	check := flag.Bool("check", false, "check the Perforce and Lore setup without importing")
	limit := flag.Int("limit", 0, "import at most this many changelists per run (0 = all)")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("perforce2lore", version)
		return
	}
	cfg, err := importer.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	im := importer.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case *check:
		os.Exit(runCheck(ctx, im))
	case *dryRun:
		if err := im.DryRun(ctx, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	lock, err := im.Lock()
	if err != nil {
		log.Fatal(err)
	}
	defer lock.Close()

	if !*watch {
		n, err := im.Run(ctx, *limit)
		log.Printf("imported %d changelist(s)", n)
		if err != nil {
			report(cfg, err)
			lock.Close()
			os.Exit(exitCode(err))
		}
		return
	}
	log.Printf("perforce2lore %s checking for changelists every %ds", version, cfg.PollSeconds)
	for {
		n, err := im.Run(ctx, *limit)
		if n > 0 {
			log.Printf("imported %d changelist(s)", n)
		}
		if err != nil && ctx.Err() == nil {
			report(cfg, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(cfg.PollSeconds) * time.Second):
		}
	}
}

// runCheck prints the result of the setup check and returns the exit code.
func runCheck(ctx context.Context, im *importer.Importer) int {
	warnings, err := im.Check(ctx)
	if err != nil {
		log.Printf("FAILED: %v", err)
		return exitFailed
	}
	for _, w := range warnings {
		log.Printf("warning: %s", w)
	}
	statuses, err := im.Status(ctx)
	if err != nil {
		log.Printf("FAILED: %v", err)
		return exitFailed
	}
	for _, s := range statuses {
		switch {
		case s.Branch == "":
			log.Printf("%s: last imported %d, %d pending", s.Name, s.Last, len(s.Pending))
		case !s.Exists:
			log.Printf("%s -> %s: new branch from %s, %d pending", s.Name, s.Branch, s.Parent, len(s.Pending))
		default:
			log.Printf("%s -> %s: last imported %d, %d pending", s.Name, s.Branch, s.Last, len(s.Pending))
		}
	}
	log.Printf("OK")
	return 0
}

// report logs why the import stopped and, for a missing author, the entry to add.
func report(cfg *importer.Config, err error) {
	var miss *importer.MissingAuthorError
	if !errors.As(err, &miss) {
		log.Printf("FAILED: %v", err)
		return
	}
	log.Printf("HALTED: %v", miss)
	log.Printf("Add %q or %q to %s; the import resumes from changelist %d.",
		miss.User, importer.ChangeKey(miss.Change), cfg.AuthorsFile, miss.Change)
}

func exitCode(err error) int {
	var miss *importer.MissingAuthorError
	if errors.As(err, &miss) {
		return exitMissingAuthor
	}
	return exitFailed
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintln(out, "Usage: perforce2lore [-config file] [-check | -dry-run | -watch] [-limit n]")
	flag.PrintDefaults()
}
