// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package hooks runs external programs at defined points of a release.
//
// This is yasrt's extension mechanism, and it is deliberately out-of-process.
// semantic-release's plugin chain is what produced the 384 to 516 unpinned
// transitive packages and the twenty-one seconds of npm install per run that
// this tool exists to remove; resolving plugins at runtime would reintroduce
// exactly that. A hook here is any executable — a Go binary, a shell script, a
// PHP script — so the binary itself stays dependency-free and a plugin needs no
// package manager in the release path.
//
// A hook receives the release context as JSON on stdin and as RELEASE_*
// environment variables, and signals failure with a non-zero exit code.
package hooks

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Event names the point in the release at which a hook runs.
type Event string

const (
	// AfterAnalysis runs in `yasrt next`, after the decision is made. It cannot
	// change the decision: `next` stays read-only, and a hook that could rewrite
	// the answer would make the handshake meaningless.
	AfterAnalysis Event = "after_analysis"
	// BeforeTag runs in `yasrt release` before anything is written. A failure
	// here aborts the release with nothing pushed.
	BeforeTag Event = "before_tag"
	// AfterTag runs once the tag is on the remote, before the release commit.
	AfterTag Event = "after_tag"
	// AfterRelease runs last. Failures are reported and never fatal, because
	// the release has already happened.
	AfterRelease Event = "after_release"
)

// Events lists every supported hook point, in the order they occur.
func Events() []Event { return []Event{AfterAnalysis, BeforeTag, AfterTag, AfterRelease} }

// DefaultTimeout bounds a single hook.
const DefaultTimeout = 5 * time.Minute

// Hook is one configured external program.
type Hook struct {
	// Run is the executable. It is resolved relative to the repository when it
	// starts with ./ or ../, and through PATH otherwise.
	Run string `yaml:"run"`
	// Args are passed verbatim; no shell is involved, so nothing is word-split
	// or glob-expanded behind your back.
	Args []string `yaml:"args"`
	// Name labels the hook in logs and in the report. Defaults to Run.
	Name string `yaml:"name"`
	// Timeout overrides DefaultTimeout, as a Go duration such as "90s".
	Timeout string `yaml:"timeout"`
	// AllowFailure overrides the per-event default: after_release tolerates
	// failure, every earlier event does not.
	AllowFailure *bool `yaml:"allow_failure"`
}

func (h Hook) label() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Run
}

func (h Hook) timeout() (time.Duration, error) {
	if strings.TrimSpace(h.Timeout) == "" {
		return DefaultTimeout, nil
	}
	d, err := time.ParseDuration(h.Timeout)
	if err != nil {
		return 0, fmt.Errorf("hook %q: timeout %q: %w", h.label(), h.Timeout, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("hook %q: timeout must be positive", h.label())
	}
	return d, nil
}

// fatal reports whether a failure of this hook must stop the release.
func (h Hook) fatal(e Event) bool {
	if h.AllowFailure != nil {
		return !*h.AllowFailure
	}
	return e != AfterRelease && e != AfterAnalysis
}

// Context is what a hook is told. It carries no credentials: a hook that needs
// a token reads it from its own environment, so yasrt never hands one out.
type Context struct {
	Event      string   `json:"event"`
	Status     string   `json:"status"`
	Version    string   `json:"version,omitzero"`
	Tag        string   `json:"tag,omitzero"`
	Previous   string   `json:"previous,omitzero"`
	Bump       string   `json:"bump,omitzero"`
	Reason     string   `json:"reason,omitzero"`
	Commit     string   `json:"commit"`
	Notes      string   `json:"notes,omitzero"`
	ProjectURL string   `json:"project_url,omitzero"`
	Breaking   []string `json:"breaking_changes,omitzero"`
	DryRun     bool     `json:"dry_run,omitzero"`
}

// Result records one hook run, for the release report.
type Result struct {
	Name     string `json:"name"`
	Event    string `json:"event"`
	ExitCode int    `json:"exit_code"`
	Duration string `json:"duration"`
	Output   string `json:"output,omitzero"`
	Error    string `json:"error,omitzero"`
	Fatal    bool   `json:"fatal,omitzero"`
}

// ErrHookFailed marks a failure that must stop the release.
var ErrHookFailed = errors.New("hook failed")

// Run executes the hooks configured for one event, in order. It stops at the
// first fatal failure and returns what ran up to that point.
func Run(ctx context.Context, dir string, event Event, hs []Hook, c Context, log *slog.Logger) ([]Result, error) {
	if len(hs) == 0 {
		return nil, nil
	}
	c.Event = string(event)
	payload, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}

	var results []Result
	for _, h := range hs {
		res, err := runOne(ctx, dir, event, h, payload, c, log)
		results = append(results, res)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func runOne(ctx context.Context, dir string, event Event, h Hook, payload []byte, c Context, log *slog.Logger) (Result, error) {
	res := Result{Name: h.label(), Event: string(event)}

	timeout, err := h.timeout()
	if err != nil {
		res.Error = err.Error()
		res.Fatal = true
		return res, fmt.Errorf("%w: %v", ErrHookFailed, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, h.Run, h.Args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(), envFor(c)...)

	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	start := time.Now()
	runErr := cmd.Run()
	res.Duration = time.Since(start).Round(time.Millisecond).String()
	res.Output = strings.TrimRight(out.String(), "\n")
	res.ExitCode = cmd.ProcessState.ExitCode()

	if runErr == nil {
		log.Info("hook ok", "event", string(event), "hook", res.Name, "duration", res.Duration)
		if res.Output != "" {
			log.Debug("hook output", "hook", res.Name, "output", res.Output)
		}
		return res, nil
	}

	res.Error = runErr.Error()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.Error = fmt.Sprintf("timed out after %s", timeout)
	}
	res.Fatal = h.fatal(event)

	if !res.Fatal {
		log.Warn("hook failed, continuing", "event", string(event), "hook", res.Name,
			"exit", res.ExitCode, "err", res.Error, "output", res.Output)
		return res, nil
	}
	log.Error("hook failed", "event", string(event), "hook", res.Name,
		"exit", res.ExitCode, "err", res.Error, "output", res.Output)
	return res, fmt.Errorf("%w: %s (%s): %s", ErrHookFailed, res.Name, string(event), res.Error)
}

// envFor exposes the same handshake a downstream CI job sees, so a shell hook
// needs no JSON parsing to do something simple.
func envFor(c Context) []string {
	return []string{
		"YASRT_EVENT=" + c.Event,
		"RELEASE_STATUS=" + c.Status,
		"RELEASE_VERSION=" + c.Version,
		"RELEASE_TAG=" + c.Tag,
		"RELEASE_PREVIOUS=" + c.Previous,
		"RELEASE_BUMP=" + c.Bump,
		"RELEASE_REASON=" + c.Reason,
		"RELEASE_COMMIT=" + c.Commit,
	}
}
