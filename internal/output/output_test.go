// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package output_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/yasrt/internal/analyze"
	"github.com/ohartwig/yasrt/internal/deliver"
	"github.com/ohartwig/yasrt/internal/output"
	"github.com/ohartwig/yasrt/internal/semver"
)

func releaseResult() *analyze.Result {
	return &analyze.Result{
		Status:      analyze.StatusRelease,
		Version:     semver.Version{Major: 3, Minor: 4, Patch: 0},
		Tag:         "3.4.0",
		Previous:    "3.3.2",
		Bump:        semver.Minor,
		Reason:      "feat",
		Commit:      "0123456789abcdef0123456789abcdef01234567",
		HasPrevious: true,
		Delivery:    deliver.Result{Deliverable: true, Delivering: []string{"src/main.go"}},
	}
}

// The seven keys are a public interface; consumer pipelines switch on them.
func TestDotenvContract(t *testing.T) {
	got := output.BuildEnv(releaseResult()).String()
	want := strings.Join([]string{
		"RELEASE_STATUS=release",
		"RELEASE_VERSION=3.4.0",
		"RELEASE_TAG=3.4.0",
		"RELEASE_PREVIOUS=3.3.2",
		"RELEASE_BUMP=minor",
		"RELEASE_REASON=feat",
		"RELEASE_COMMIT=0123456789abcdef0123456789abcdef01234567",
		"",
	}, "\n")
	if got != want {
		t.Errorf("dotenv contract changed:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A consumer that forgets to check the status must not be handed a version.
func TestVersionAndTagEmptyWhenNotReleasing(t *testing.T) {
	for _, status := range []analyze.Status{analyze.StatusNoBump, analyze.StatusNotDeliverable} {
		r := releaseResult()
		r.Status = status
		env := output.BuildEnv(r)
		if env.Get(output.KeyVersion) != "" || env.Get(output.KeyTag) != "" {
			t.Errorf("%s: version=%q tag=%q, both must be empty",
				status, env.Get(output.KeyVersion), env.Get(output.KeyTag))
		}
		if env.Get(output.KeyStatus) != string(status) {
			t.Errorf("status = %q", env.Get(output.KeyStatus))
		}
	}
}

func TestAlreadyReleasedKeepsTheVersion(t *testing.T) {
	r := releaseResult()
	r.Status = analyze.StatusAlreadyReleased
	if got := output.BuildEnv(r).Get(output.KeyVersion); got != "3.4.0" {
		t.Errorf("version = %q — a re-run needs to know what is already out", got)
	}
}

func TestValuesAreUnquoted(t *testing.T) {
	// GitLab's dotenv reader does not unquote, so a quoted value would arrive
	// with its quotes attached.
	out := output.BuildEnv(releaseResult()).String()
	if strings.Contains(out, `"`) || strings.Contains(out, "'") {
		t.Errorf("dotenv values must be bare:\n%s", out)
	}
}

func TestRoundTripThroughDotenvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".release.env")
	if err := output.WriteFile(path, output.BuildEnv(releaseResult())); err != nil {
		t.Fatal(err)
	}
	kv, err := output.ReadDotenv(path)
	if err != nil {
		t.Fatal(err)
	}
	if kv[output.KeyTag] != "3.4.0" || kv[output.KeyStatus] != "release" {
		t.Errorf("kv = %+v", kv)
	}
}

func TestReadDotenvToleratesQuotesAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".release.env")
	body := "# written by hand\nRELEASE_STATUS=\"release\"\n\nRELEASE_TAG='v1.2.3'\nnonsense\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	kv, err := output.ReadDotenv(path)
	if err != nil {
		t.Fatal(err)
	}
	if kv[output.KeyStatus] != "release" || kv[output.KeyTag] != "v1.2.3" {
		t.Errorf("kv = %+v", kv)
	}
}

func TestJSONSummary(t *testing.T) {
	var buf bytes.Buffer
	if err := output.WriteJSON(&buf, output.BuildSummary(releaseResult())); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, want := range []string{`"status":"release"`, `"version":"3.4.0"`, `"deliverable":true`} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %s:\n%s", want, s)
		}
	}
}

func TestHumanMessages(t *testing.T) {
	r := releaseResult()
	if got := output.Human(r); !strings.Contains(got, "would release 3.4.0") {
		t.Errorf("release: %q", got)
	}

	r.Status = analyze.StatusNoBump
	if got := output.Human(r); !strings.Contains(got, "no bump") {
		t.Errorf("no-bump: %q", got)
	}

	r.Status = analyze.StatusNotDeliverable
	r.Delivery = deliver.Result{Excluded: []string{"docs/a.md", "docs/b.md"}}
	got := output.Human(r)
	if !strings.Contains(got, "not deliverable") || !strings.Contains(got, "docs/a.md") {
		t.Errorf("not-deliverable: %q", got)
	}

	r.Delivery = deliver.Result{Excluded: []string{"a", "b", "c", "d", "e"}}
	if got := output.Human(r); !strings.Contains(got, "and 2 more") {
		t.Errorf("long list should be summarised: %q", got)
	}

	r.Status = analyze.StatusAlreadyReleased
	if got := output.Human(r); !strings.Contains(got, "already released") {
		t.Errorf("already-released: %q", got)
	}
}
