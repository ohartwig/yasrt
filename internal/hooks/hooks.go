// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

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
//
// A hook does not inherit the job's credentials. Its environment is curated
// (see Environment): identifiers such as PATH, HOME and CI_*, the proxy and
// certificate settings, and the handshake — never a variable whose name
// says it holds a credential, nor one whose value contains a secret the
// release registered. A hook that needs a token names it in `env`, and gets
// exactly that.
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
	"slices"
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
	// OnFailure runs when `yasrt release` is about to exit with an error, so
	// that somebody can be told. It is told what failed. Its own failure is
	// logged and changes nothing: the release is already lost.
	OnFailure Event = "on_failure"
)

// Events lists every supported hook point, in the order they occur.
func Events() []Event { return []Event{AfterAnalysis, BeforeTag, AfterTag, AfterRelease, OnFailure} }

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
	// Env names job variables passed to this hook as they are, credentials
	// included: the explicit opt-in for a hook that needs a token.
	Env []string `yaml:"env"`
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
	return e != AfterRelease && e != AfterAnalysis && e != OnFailure
}

// Context is what a hook is told. It carries no credentials, and neither does
// the hook's environment unless the hook names a variable in Hook.Env.
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
	// Error and FailedStep are set for on_failure only.
	Error      string `json:"error,omitzero"`
	FailedStep string `json:"failed_step,omitzero"`
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

// Masker removes secrets from text before it is recorded. A hook may still
// come by one — a variable it named in Hook.Env, a file it reads — and its
// output ends up in the release report, which is a job artefact. Environment
// also uses it to recognise a variable whose value contains a secret.
type Masker func(string) string

// Run executes the hooks configured for one event, in order. It stops at the
// first fatal failure and returns what ran up to that point. mask is applied
// to every hook's captured output; nil records output verbatim.
func Run(ctx context.Context, dir string, event Event, hs []Hook, c Context, mask Masker, log *slog.Logger) ([]Result, error) {
	if len(hs) == 0 {
		return nil, nil
	}
	if mask == nil {
		mask = func(s string) string { return s }
	}
	c.Event = string(event)
	payload, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}

	var results []Result
	for _, h := range hs {
		res, err := runOne(ctx, dir, event, h, payload, c, mask, log)
		results = append(results, res)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func runOne(ctx context.Context, dir string, event Event, h Hook, payload []byte, c Context, mask Masker, log *slog.Logger) (Result, error) {
	res := Result{Name: h.label(), Event: string(event)}

	timeout, err := h.timeout()
	if err != nil {
		res.Error = err.Error()
		res.Fatal = true
		return res, fmt.Errorf("%w: %w", ErrHookFailed, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, h.Run, h.Args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(Environment(os.Environ(), h.Env, mask), envFor(c)...)

	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	start := time.Now()
	runErr := cmd.Run()
	res.Duration = time.Since(start).Round(time.Millisecond).String()
	res.Output = mask(strings.TrimRight(out.String(), "\n"))
	res.ExitCode = cmd.ProcessState.ExitCode()

	if runErr == nil {
		log.Info("hook ok", "event", string(event), "hook", res.Name, "duration", res.Duration)
		if res.Output != "" {
			log.Debug("hook output", "hook", res.Name, "output", res.Output)
		}
		return res, nil
	}

	res.Error = mask(runErr.Error())
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
		"YASRT_ERROR=" + c.Error,
		"YASRT_FAILED_STEP=" + c.FailedStep,
	}
}

// passNames are passed to every hook by exact name; passPrefixes by prefix.
// Everything else stays in the job.
var (
	passNames = []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "PWD", "HOSTNAME",
		"LANG", "LANGUAGE", "TZ", "TMPDIR",
		"CI", "GITLAB_CI",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	}
	passPrefixes = []string{"LC_", "CI_", "GITLAB_", "GITHUB_", "RUNNER_", "YASRT_", "RELEASE_"}
	// credentialWords in a name keep a variable from a hook even when its
	// prefix would pass it: CI_JOB_TOKEN, CI_REGISTRY_PASSWORD, GITLAB_TOKEN,
	// GITHUB_TOKEN, CI_JOB_JWT and their kind.
	credentialWords = []string{"TOKEN", "PASSWORD", "PASSWD", "SECRET", "PRIVATE", "KEY", "JWT", "CREDENTIAL", "AUTH", "COOKIE", "SESSION"}
	// withheldNames carry a credential without saying so: the clone URL embeds
	// the job token.
	withheldNames = []string{"CI_REPOSITORY_URL"}
)

// Environment returns the part of environ a hook may see (AUD-030-F02):
// the names in passNames and passPrefixes, minus any whose name contains a
// credential word, minus any whose value the masker changes — that is, one
// that contains a secret the release registered (the job token, the
// signing key) — plus the variables the hook names in pass, as they are.
// mask may be nil.
func Environment(environ, pass []string, mask Masker) []string {
	var out []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if slices.Contains(pass, name) {
			out = append(out, kv)
			continue
		}
		if !passes(name) {
			continue
		}
		if mask != nil && mask(value) != value {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func passes(name string) bool {
	if slices.Contains(withheldNames, name) {
		return false
	}
	// AUTHOR (CI_COMMIT_AUTHOR) is an identifier, not AUTH.
	upper := strings.ReplaceAll(strings.ToUpper(name), "AUTHOR", "")
	if slices.ContainsFunc(credentialWords, func(w string) bool { return strings.Contains(upper, w) }) {
		return false
	}
	return slices.Contains(passNames, name) || slices.ContainsFunc(passPrefixes, func(p string) bool { return strings.HasPrefix(name, p) })
}
