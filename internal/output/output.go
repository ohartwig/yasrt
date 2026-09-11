// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package output renders an analysis for other jobs to consume.
//
// The dotenv keys are a public interface: consumer .gitlab-ci.yml files switch
// on RELEASE_STATUS in their scripts, because GitLab evaluates rules: before
// this file exists. Renaming a key breaks other people's pipelines.
package output

import (
	"bufio"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"strings"

	"git.ole-hartwig.eu/devops/yasrt/internal/analyze"
)

// Keys of the dotenv contract.
const (
	KeyStatus   = "RELEASE_STATUS"
	KeyVersion  = "RELEASE_VERSION"
	KeyTag      = "RELEASE_TAG"
	KeyPrevious = "RELEASE_PREVIOUS"
	KeyBump     = "RELEASE_BUMP"
	KeyReason   = "RELEASE_REASON"
	KeyCommit   = "RELEASE_COMMIT"
)

// Env is the ordered dotenv view of a result.
type Env [][2]string

// BuildEnv renders the seven keys. Version and tag are empty for anything that
// is not a release, so a consumer that forgets to check the status cannot
// accidentally build something with a stale version.
func BuildEnv(r *analyze.Result) Env {
	version, tag := "", ""
	if r.Status == analyze.StatusRelease || r.Status == analyze.StatusAlreadyReleased {
		version, tag = r.Version.String(), r.Tag
	}
	bump := ""
	if r.Bump != 0 {
		bump = r.Bump.String()
	}
	return Env{
		{KeyStatus, string(r.Status)},
		{KeyVersion, version},
		{KeyTag, tag},
		{KeyPrevious, r.Previous},
		{KeyBump, bump},
		{KeyReason, r.Reason},
		{KeyCommit, r.Commit},
	}
}

// String renders dotenv text. Values are written bare: GitLab's dotenv reader
// does not unquote, so a quoted value would arrive with its quotes.
func (e Env) String() string {
	var b strings.Builder
	for _, kv := range e {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return b.String()
}

// Get returns one value.
func (e Env) Get(key string) string {
	for _, kv := range e {
		if kv[0] == key {
			return kv[1]
		}
	}
	return ""
}

// WriteFile writes the dotenv artefact.
func WriteFile(path string, e Env) error {
	return os.WriteFile(path, []byte(e.String()), 0o644)
}

// Summary is the machine-readable view behind --json. It carries more than the
// dotenv contract because a human or a report reads it, not a shell.
type Summary struct {
	Status   string `json:"status"`
	Version  string `json:"version,omitzero"`
	Tag      string `json:"tag,omitzero"`
	Previous string `json:"previous,omitzero"`
	Bump     string `json:"bump,omitzero"`
	Reason   string `json:"reason,omitzero"`
	Commit   string `json:"commit"`

	Deliverable bool     `json:"deliverable"`
	Delivering  []string `json:"delivering,omitzero"`
	Excluded    []string `json:"excluded,omitzero"`

	Counted       int `json:"counted_commits"`
	Ignored       int `json:"ignored_commits"`
	NonConforming int `json:"non_conforming_commits"`

	Breaking []string `json:"breaking_changes,omitzero"`
	Warnings []string `json:"warnings,omitzero"`
}

// BuildSummary converts a result for JSON output.
func BuildSummary(r *analyze.Result) Summary {
	e := BuildEnv(r)
	return Summary{
		Status:        e.Get(KeyStatus),
		Version:       e.Get(KeyVersion),
		Tag:           e.Get(KeyTag),
		Previous:      e.Get(KeyPrevious),
		Bump:          e.Get(KeyBump),
		Reason:        e.Get(KeyReason),
		Commit:        r.Commit,
		Deliverable:   r.Delivery.Deliverable,
		Delivering:    r.Delivery.Delivering,
		Excluded:      r.Delivery.Excluded,
		Counted:       len(r.Decision.Counted),
		Ignored:       len(r.Decision.Ignored),
		NonConforming: len(r.Decision.NonConforming),
		Breaking:      r.Decision.Breaking(),
		Warnings:      r.Warnings,
	}
}

// WriteJSON emits a summary.
func WriteJSON(w io.Writer, s Summary) error {
	b, err := json.Marshal(s, json.FormatNilSliceAsNull(false))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// Human renders the one-line answer a person reads in a job log.
func Human(r *analyze.Result) string {
	switch r.Status {
	case analyze.StatusRelease:
		return fmt.Sprintf("would release %s (%s, from %s)", r.Version, r.Bump, reasonOrNone(r))
	case analyze.StatusAlreadyReleased:
		return fmt.Sprintf("already released as %s", r.Tag)
	case analyze.StatusNoBump:
		return "no bump: nothing in the range asks for a release"
	case analyze.StatusNotDeliverable:
		return "not deliverable: " + describeExcluded(r)
	}
	return string(r.Status)
}

func reasonOrNone(r *analyze.Result) string {
	if r.Reason == "" {
		return "no reason recorded"
	}
	return r.Reason
}

func describeExcluded(r *analyze.Result) string {
	if len(r.Delivery.Excluded) == 0 {
		return "nothing changed since the last release"
	}
	shown := r.Delivery.Excluded
	const max = 3
	if len(shown) > max {
		return fmt.Sprintf("only non-release paths changed (%s and %d more)",
			strings.Join(shown[:max], ", "), len(shown)-max)
	}
	return "only non-release paths changed (" + strings.Join(shown, ", ") + ")"
}

// ReadDotenv parses a dotenv file written by BuildEnv. It is deliberately
// forgiving about quoting, because a human may have edited it.
func ReadDotenv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}
