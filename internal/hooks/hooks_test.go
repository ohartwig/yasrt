// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package hooks_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohartwig/yasrt/internal/hooks"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// script writes an executable shell script into dir and returns its path
// relative to dir, which is how a hook is normally configured.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return "./" + name
}

func ctxFor() hooks.Context {
	return hooks.Context{
		Status: "release", Version: "3.4.0", Tag: "3.4.0", Previous: "3.3.2",
		Bump: "minor", Reason: "feat", Commit: "0123456789abcdef",
		Notes: "### Features\n\n- a thing", ProjectURL: "https://git/x",
	}
}

func TestNoHooksIsNotAnError(t *testing.T) {
	res, err := hooks.Run(context.Background(), t.TempDir(), hooks.BeforeTag, nil, ctxFor(), nil, discard)
	if err != nil || res != nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestHookReceivesContextAsJSONOnStdin(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "capture.sh", "cat > payload.json")

	_, err := hooks.Run(context.Background(), dir, hooks.AfterTag, []hooks.Hook{{Run: run}}, ctxFor(), nil, discard)
	if err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got hooks.Context
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("hook payload is not valid JSON: %v\n%s", err, b)
	}
	if got.Event != string(hooks.AfterTag) {
		t.Errorf("event = %q", got.Event)
	}
	if got.Version != "3.4.0" || got.Tag != "3.4.0" || got.Commit != "0123456789abcdef" {
		t.Errorf("context = %+v", got)
	}
	if got.Notes == "" {
		t.Error("a hook that publishes wants the notes")
	}
}

// A shell hook should not have to parse JSON to do something simple.
func TestHookReceivesReleaseEnvironment(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "env.sh", `printf '%s|%s|%s|%s\n' "$YASRT_EVENT" "$RELEASE_VERSION" "$RELEASE_TAG" "$RELEASE_STATUS"`)

	res, err := hooks.Run(context.Background(), dir, hooks.AfterRelease, []hooks.Hook{{Run: run}}, ctxFor(), nil, discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := res[0].Output; got != "after_release|3.4.0|3.4.0|release" {
		t.Errorf("output = %q", got)
	}
}

func TestArgumentsArePassedWithoutAShell(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "args.sh", `printf '%s\n' "$1" "$2"`)

	// A value containing a space and a glob must arrive intact.
	res, err := hooks.Run(context.Background(), dir, hooks.AfterTag, []hooks.Hook{{Run: run, Args: []string{"two words", "*.go"}}}, ctxFor(), nil, discard)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Output != "two words\n*.go" {
		t.Errorf("output = %q — arguments must not be word-split or glob-expanded", res[0].Output)
	}
}

func TestFailingHookAbortsBeforeTag(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "no.sh", "echo 'not today' >&2; exit 3")

	res, err := hooks.Run(context.Background(), dir, hooks.BeforeTag, []hooks.Hook{{Run: run}}, ctxFor(), nil, discard)
	if !errors.Is(err, hooks.ErrHookFailed) {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 1 || res[0].ExitCode != 3 || !res[0].Fatal {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res[0].Output, "not today") {
		t.Errorf("the hook's own message must survive: %q", res[0].Output)
	}
}

// The release already happened by then, so a late failure is reported, not fatal.
func TestFailingHookAfterReleaseIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "no.sh", "exit 1")

	res, err := hooks.Run(context.Background(), dir, hooks.AfterRelease, []hooks.Hook{{Run: run}}, ctxFor(), nil, discard)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 1 || res[0].ExitCode != 1 || res[0].Fatal {
		t.Errorf("res = %+v", res)
	}
}

func TestAllowFailureOverridesTheDefault(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "no.sh", "exit 1")
	yes, no := true, false

	if _, err := hooks.Run(context.Background(), dir, hooks.BeforeTag, []hooks.Hook{{Run: run, AllowFailure: &yes}}, ctxFor(), nil, discard); err != nil {
		t.Errorf("allow_failure true should tolerate a failure: %v", err)
	}
	if _, err := hooks.Run(context.Background(), dir, hooks.AfterRelease, []hooks.Hook{{Run: run, AllowFailure: &no}}, ctxFor(), nil, discard); !errors.Is(err, hooks.ErrHookFailed) {
		t.Errorf("allow_failure false should make it fatal: %v", err)
	}
}

func TestHooksRunInOrderAndStopAtTheFirstFatalOne(t *testing.T) {
	dir := t.TempDir()
	first := script(t, dir, "first.sh", "echo first >> order.txt")
	boom := script(t, dir, "boom.sh", "echo boom >> order.txt; exit 1")
	third := script(t, dir, "third.sh", "echo third >> order.txt")

	res, err := hooks.Run(context.Background(), dir, hooks.BeforeTag, []hooks.Hook{
		{Run: first}, {Run: boom}, {Run: third},
	}, ctxFor(), nil, discard)
	if !errors.Is(err, hooks.ErrHookFailed) {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 2 {
		t.Errorf("only the hooks that ran should be reported: %+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "order.txt"))
	if strings.TrimSpace(string(b)) != "first\nboom" {
		t.Errorf("order = %q — nothing may run after a fatal hook", b)
	}
}

func TestTimeout(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "slow.sh", "sleep 5")

	res, err := hooks.Run(context.Background(), dir, hooks.BeforeTag, []hooks.Hook{{Run: run, Timeout: "150ms"}}, ctxFor(), nil, discard)
	if !errors.Is(err, hooks.ErrHookFailed) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(res[0].Error, "timed out") {
		t.Errorf("error should name the timeout: %+v", res[0])
	}
}

func TestInvalidTimeoutIsReportedNotIgnored(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "ok.sh", "true")
	_, err := hooks.Run(context.Background(), dir, hooks.BeforeTag, []hooks.Hook{{Run: run, Timeout: "soon"}}, ctxFor(), nil, discard)
	if !errors.Is(err, hooks.ErrHookFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingExecutableIsAFailure(t *testing.T) {
	_, err := hooks.Run(context.Background(), t.TempDir(), hooks.BeforeTag, []hooks.Hook{{Run: "./definitely-not-here"}}, ctxFor(), nil, discard)
	if !errors.Is(err, hooks.ErrHookFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestNameLabelsTheHook(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "ok.sh", "true")
	res, err := hooks.Run(context.Background(), dir, hooks.AfterTag, []hooks.Hook{{Run: run, Name: "publish to TER"}}, ctxFor(), nil, discard)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Name != "publish to TER" {
		t.Errorf("name = %q", res[0].Name)
	}
}

func TestContextCancellationStopsHooks(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "slow.sh", "sleep 5")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hooks.Run(ctx, dir, hooks.BeforeTag, []hooks.Hook{{Run: run}}, ctxFor(), nil, discard); err == nil {
		t.Fatal("expected the cancelled context to surface")
	}
}

func TestEventsCoverEveryHookPoint(t *testing.T) {
	if got := len(hooks.Events()); got != 5 {
		t.Errorf("Events() = %d", got)
	}
}

// A failure hook that fails itself must not hide the original failure.
func TestFailingOnFailureHookIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "fail.sh", "#!/bin/sh\nexit 3\n")
	res, err := hooks.Run(context.Background(), dir, hooks.OnFailure, []hooks.Hook{{Run: run}}, ctxFor(), nil, discard)
	if err != nil {
		t.Fatalf("on_failure must not be fatal: %v", err)
	}
	if len(res) != 1 || res[0].ExitCode != 3 || res[0].Fatal {
		t.Errorf("res = %+v", res)
	}
}

// A hook that prints a secret must not put it into the report.
func TestHookOutputIsMasked(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "leak.sh", "#!/bin/sh\necho \"token=$LEAKY\"\nexit 1\n")
	t.Setenv("LEAKY", "s3cret")
	mask := func(s string) string { return strings.ReplaceAll(s, "s3cret", "***") }
	// Named in env: the hook gets it, and its output is still masked.
	res, _ := hooks.Run(context.Background(), dir, hooks.AfterRelease, []hooks.Hook{{Run: run, Env: []string{"LEAKY"}}}, ctxFor(), mask, discard)
	if len(res) != 1 || strings.Contains(res[0].Output, "s3cret") || !strings.Contains(res[0].Output, "***") {
		t.Errorf("output = %+v", res)
	}
}

// AUD-030-F02: a hook sees identifiers, not the job's credentials.
func TestEnvironmentWithholdsCredentials(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin", "HOME=/root", "CI_PROJECT_ID=831", "CI_COMMIT_AUTHOR=A <a@x>",
		"HTTPS_PROXY=http://proxy:3128", "LC_ALL=C",
		"CI_JOB_TOKEN=glcbt-abcdef", "CI_REGISTRY_PASSWORD=p", "CI_JOB_JWT_V2=jwt",
		"GITLAB_TOKEN=glpat-x", "GITHUB_MIRROR_TOKEN=ghp", "PINUP_TRIGGER_TOKEN=t",
		"COSIGN_PRIVATE_KEY=k", "COSIGN_PASSWORD=p", "GPG_SEM_REL_B64=LS0tLS1CRUdJTiBrZXk=",
		"CI_REPOSITORY_URL=https://gitlab-ci-token:glcbt-abcdef@gitlab/x.git",
		"CI_SOME_URL=https://x/?t=glcbt-abcdef", "UNRELATED=1",
	}
	mask := func(s string) string { return strings.ReplaceAll(s, "glcbt-abcdef", "***") }
	got := hooks.Environment(environ, []string{"PINUP_TRIGGER_TOKEN"}, mask)
	for _, want := range []string{"PATH=/usr/bin", "HOME=/root", "CI_PROJECT_ID=831", "CI_COMMIT_AUTHOR=A <a@x>", "HTTPS_PROXY=http://proxy:3128", "LC_ALL=C", "PINUP_TRIGGER_TOKEN=t"} {
		if !slices.Contains(got, want) {
			t.Errorf("hook env lacks %s", want)
		}
	}
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH", "HOME", "CI_PROJECT_ID", "CI_COMMIT_AUTHOR", "HTTPS_PROXY", "LC_ALL", "PINUP_TRIGGER_TOKEN":
		default:
			t.Errorf("hook env carries %s", kv)
		}
	}
}

func TestHookDoesNotSeeTheJobToken(t *testing.T) {
	dir := t.TempDir()
	run := script(t, dir, "env.sh", "env")
	t.Setenv("CI_JOB_TOKEN", "glcbt-canary")
	t.Setenv("GPG_SEM_REL_B64", "a2V5LWNhbmFyeQ==")
	t.Setenv("CI_PROJECT_PATH", "yasrt/cli")
	res, err := hooks.Run(context.Background(), dir, hooks.BeforeTag, []hooks.Hook{{Run: run}}, ctxFor(), nil, discard)
	if err != nil || len(res) != 1 {
		t.Fatalf("res %+v, %v", res, err)
	}
	if strings.Contains(res[0].Output, "glcbt-canary") || strings.Contains(res[0].Output, "GPG_SEM_REL_B64") {
		t.Errorf("hook saw a credential:\n%s", res[0].Output)
	}
	if !strings.Contains(res[0].Output, "CI_PROJECT_PATH=yasrt/cli") || !strings.Contains(res[0].Output, "RELEASE_VERSION=") {
		t.Errorf("hook lacks identifiers or the handshake:\n%s", res[0].Output)
	}
}
