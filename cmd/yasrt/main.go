// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

// Command yasrt decides whether a repository should be released, and publishes
// the release once the artefact exists.
//
// The CLI uses the standard library flag package. Every Go binary in this
// estate does, and a release tool is a poor place to spend a dependency.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"git.ole-hartwig.eu/devops/yasrt/internal/analyze"
	"git.ole-hartwig.eu/devops/yasrt/internal/logging"
	"git.ole-hartwig.eu/devops/yasrt/internal/release"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes are a public interface: consumer pipelines switch on them.
const (
	exitOK             = 0
	exitError          = 1 // configuration or git failure
	exitUsage          = 2 // invalid arguments
	exitNoBump         = 3 // --fail-on-skip and nothing to release
	exitNotDeliverable = 4 // --fail-on-skip and nothing shippable changed
	exitConflict       = 5 // the repository moved under us
)

const usage = `yasrt — decide and publish releases from Conventional Commits.

Usage:
  yasrt next     [flags]   analyse the repository and emit the result (writes nothing)
  yasrt release  [flags]   publish notes, tag, changelog, release and triggers
  yasrt check    [flags]   validate the configuration and probe the environment
  yasrt version            print the binary version

Run "yasrt <command> -h" for the flags of a command.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "next":
		return classify(cmdNext(rest))
	case "release":
		return classify(cmdRelease(rest))
	case "check":
		return classify(cmdCheck(rest))
	case "version":
		fmt.Println(version)
		return exitOK
	case "-h", "--help", "help":
		fmt.Print(usage)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

// exitCodeError lets a command choose its own exit code.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

func codeErr(code int, format string, a ...any) error {
	return &exitCodeError{code: code, err: fmt.Errorf(format, a...)}
}

// classify turns an error into the documented exit code.
func classify(err error) int {
	if err == nil {
		return exitOK
	}
	var ec *exitCodeError
	if errors.As(err, &ec) {
		if ec.err != nil && !errors.Is(ec.err, errSilent) {
			fmt.Fprintln(os.Stderr, "yasrt: "+ec.err.Error())
		}
		return ec.code
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.Is(err, release.ErrCommitMoved), errors.Is(err, release.ErrTagMismatch):
		fmt.Fprintln(os.Stderr, "yasrt: "+err.Error())
		return exitConflict
	case errors.Is(err, analyze.ErrVersionNotHigher):
		fmt.Fprintln(os.Stderr, "yasrt: "+err.Error())
		return exitUsage
	}
	fmt.Fprintln(os.Stderr, "yasrt: "+err.Error())
	return exitError
}

// errSilent marks an error whose message was already printed.
var errSilent = errors.New("already reported")

// commonFlags are shared by every subcommand.
type commonFlags struct {
	config    string
	logFormat string
	verbose   bool
	json      bool
}

func (c *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.config, "config", envOr("YASRT_CONFIG", ".yasrt.yaml"), "path to the configuration file")
	fs.StringVar(&c.logFormat, "log-format", envOr("YASRT_LOG_FORMAT", "text"), "log format: text or json")
	fs.BoolVar(&c.verbose, "verbose", false, "log every decision, including ignored commits")
	fs.BoolVar(&c.json, "json", false, "emit a machine-readable summary on stdout")
}

func (c *commonFlags) logger(secrets ...string) *slog.Logger {
	format := logging.FormatText
	if strings.EqualFold(c.logFormat, "json") {
		format = logging.FormatJSON
	}
	return logging.New(os.Stderr, logging.Options{
		Format:  format,
		Verbose: c.verbose,
		Secrets: secrets,
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parse(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return &exitCodeError{code: exitOK, err: errSilent}
		}
		return &exitCodeError{code: exitUsage, err: errSilent}
	}
	if fs.NArg() > 0 {
		return codeErr(exitUsage, "unexpected argument %q", fs.Arg(0))
	}
	return nil
}
