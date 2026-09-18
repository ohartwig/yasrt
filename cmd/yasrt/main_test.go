// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/yasrt/internal/output"
	"github.com/ohartwig/yasrt/internal/testrepo"
)

// fixture builds a repository with a config file and returns paths for --dir
// and --config, so no test has to change the working directory.
type fixture struct {
	tr      *testrepo.Repo
	cfgPath string
	envPath string
}

// ambientCIVars are set for real when the suite runs inside a CI job. A test
// that left them in place would build a live API client and publish to
// whichever project it happened to be running in — which is exactly what the
// first CI run tried to do. The GitHub and Forgejo markers are cleared for the
// same reason.
var ambientCIVars = []string{
	"GITLAB_CI", "CI_API_V4_URL", "CI_PROJECT_ID", "CI_PROJECT_URL", "CI_JOB_TOKEN",
	"CI_DEFAULT_BRANCH", "CI_COMMIT_BRANCH", "CI_SERVER_HOST", "GPG_SEM_REL_B64",
	"YASRT_DEFAULTS", "YASRT_CONFIG", "YASRT_FORGE",
	"GITLAB_USER_NAME", "GITLAB_USER_EMAIL",
	"GITHUB_ACTIONS", "GITHUB_REPOSITORY", "GITHUB_TOKEN", "GITHUB_API_URL",
	"GITHUB_SERVER_URL", "GITHUB_REF", "GITHUB_BASE_REF",
	"FORGEJO_ACTIONS", "FORGEJO_TOKEN", "GITEA_ACTIONS",
	output.KeyStatus, output.KeyVersion, output.KeyTag,
	output.KeyPrevious, output.KeyBump, output.KeyReason, output.KeyCommit,
}

func newFixture(t *testing.T, cfg string) *fixture {
	t.Helper()
	for _, k := range ambientCIVars {
		t.Setenv(k, "")
	}
	tr := testrepo.New(t)
	tr.Write(".yasrt.yaml", cfg)
	tr.Git("add", "-A")
	tr.Git("commit", "-q", "-m", "chore: configuration")
	return &fixture{
		tr:      tr,
		cfgPath: filepath.Join(tr.Dir, ".yasrt.yaml"),
		envPath: filepath.Join(t.TempDir(), ".release.env"),
	}
}

func (f *fixture) next(args ...string) int {
	return run(append([]string{"next", "--dir", f.tr.Dir, "--config", f.cfgPath}, args...))
}

func (f *fixture) release(args ...string) int {
	return run(append([]string{"release", "--dir", f.tr.Dir, "--config", f.cfgPath}, args...))
}

func (f *fixture) check(args ...string) int {
	return run(append([]string{"check", "--dir", f.tr.Dir, "--config", f.cfgPath}, args...))
}

func TestUsageAndVersion(t *testing.T) {
	if got := run(nil); got != exitUsage {
		t.Errorf("no arguments = %d, want %d", got, exitUsage)
	}
	if got := run([]string{"definitely-not-a-command"}); got != exitUsage {
		t.Errorf("unknown command = %d", got)
	}
	if got := run([]string{"version"}); got != exitOK {
		t.Errorf("version = %d", got)
	}
	if got := run([]string{"help"}); got != exitOK {
		t.Errorf("help = %d", got)
	}
}

func TestNextWritesTheHandshake(t *testing.T) {
	f := newFixture(t, "product: image\n")
	f.tr.CommitFile("src/main.go", "1", "feat: something shippable")

	if got := f.next("--output", f.envPath); got != exitOK {
		t.Fatalf("exit = %d", got)
	}
	kv, err := output.ReadDotenv(f.envPath)
	if err != nil {
		t.Fatal(err)
	}
	if kv[output.KeyStatus] != "release" || kv[output.KeyVersion] != "1.0.0" {
		t.Errorf("handshake = %+v", kv)
	}
}

// The documented exit codes are a public interface: consumer pipelines and the
// component's own script switch on them.
func TestExitCodes(t *testing.T) {
	t.Run("no-bump is 3 only with --fail-on-skip", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		f.tr.Tag("1.0.0")
		f.tr.CommitFile("src/main.go", "2", "style: reformat")

		if got := f.next(); got != exitOK {
			t.Errorf("without the flag the analysis still succeeded: %d", got)
		}
		if got := f.next("--fail-on-skip"); got != exitNoBump {
			t.Errorf("exit = %d, want %d", got, exitNoBump)
		}
	})

	t.Run("not-deliverable is 4", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		f.tr.Tag("1.0.0")
		f.tr.CommitFile("docs/guide.md", "d", "feat: document it")

		if got := f.next("--fail-on-skip"); got != exitNotDeliverable {
			t.Errorf("exit = %d, want %d", got, exitNotDeliverable)
		}
	})

	t.Run("bad arguments are 2", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		f.tr.Tag("2.0.0")
		f.tr.CommitFile("src/b.go", "2", "fix: second")

		if got := f.next("--version", "1.0.0"); got != exitUsage {
			t.Errorf("a lower --version = %d, want %d", got, exitUsage)
		}
		if got := f.next("--force", "enormous"); got != exitUsage {
			t.Errorf("an invalid --force = %d, want %d", got, exitUsage)
		}
		if got := f.next("--force", "minor", "--version", "3.0.0"); got != exitUsage {
			t.Errorf("mutually exclusive flags = %d, want %d", got, exitUsage)
		}
		if got := f.next("--nonexistent-flag"); got != exitUsage {
			t.Errorf("unknown flag = %d", got)
		}
		if got := f.next("stray-argument"); got != exitUsage {
			t.Errorf("stray argument = %d", got)
		}
	})

	t.Run("missing config is 1", func(t *testing.T) {
		tr := testrepo.New(t)
		tr.CommitFile("src/main.go", "1", "feat: first")
		if got := run([]string{"next", "--dir", tr.Dir, "--config", filepath.Join(tr.Dir, "absent.yaml")}); got != exitError {
			t.Errorf("exit = %d, want %d", got, exitError)
		}
	})

	t.Run("shallow clone is 1", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("a.go", "1", "feat: one")
		f.tr.CommitFile("b.go", "2", "feat: two")
		shallow := f.tr.Clone(1)
		if got := run([]string{"next", "--dir", shallow.Dir, "--config", f.cfgPath}); got != exitError {
			t.Errorf("exit = %d, want %d", got, exitError)
		}
	})
}

// Someone pushed between the analysis and the release: exit 5, nothing written.
func TestReleaseConflictIsExit5(t *testing.T) {
	f := newFixture(t, "product: image\n")
	f.tr.CommitFile("src/main.go", "1", "feat: first")
	f.tr.WithRemote()

	if got := f.next("--output", f.envPath); got != exitOK {
		t.Fatalf("next = %d", got)
	}
	f.tr.CommitFile("src/b.go", "2", "feat: pushed in the meantime")

	got := f.release("--input", f.envPath, "--report", filepath.Join(t.TempDir(), "r.json"))
	if got != exitConflict {
		t.Errorf("exit = %d, want %d", got, exitConflict)
	}
	if tags := f.tr.RemoteTags(); len(tags) != 0 {
		t.Errorf("nothing should have been pushed, got %q", tags)
	}
}

func TestReleaseRefusesWhenTheHandshakeSaysNo(t *testing.T) {
	f := newFixture(t, "product: image\n")
	f.tr.CommitFile("src/main.go", "1", "feat: first")
	f.tr.Tag("1.0.0")
	f.tr.CommitFile("src/main.go", "2", "style: reformat")
	f.tr.WithRemote()

	if got := f.next("--output", f.envPath); got != exitOK {
		t.Fatalf("next = %d", got)
	}
	if got := f.release("--input", f.envPath, "--report", filepath.Join(t.TempDir(), "r.json")); got != exitError {
		t.Errorf("exit = %d, want %d", got, exitError)
	}
}

func TestReleaseDryRunWritesNothing(t *testing.T) {
	f := newFixture(t, "product: image\n")
	f.tr.CommitFile("src/main.go", "1", "feat: first")
	f.tr.WithRemote()

	if got := f.next("--output", f.envPath); got != exitOK {
		t.Fatalf("next = %d", got)
	}
	if got := f.release("--input", f.envPath, "--dry-run"); got != exitOK {
		t.Fatalf("release = %d", got)
	}
	if tags := f.tr.RemoteTags(); len(tags) != 0 {
		t.Errorf("dry run pushed %q", tags)
	}
	if f.tr.Exists("CHANGELOG.md") {
		t.Error("dry run wrote the changelog")
	}
}

func TestFullHandshakeAndRelease(t *testing.T) {
	f := newFixture(t, "product: image\n")
	f.tr.CommitFile("src/main.go", "1", "feat: first release")
	f.tr.WithRemote()

	if got := f.next("--output", f.envPath); got != exitOK {
		t.Fatalf("next = %d", got)
	}
	report := filepath.Join(t.TempDir(), "release-report.json")
	if got := f.release("--input", f.envPath, "--report", report); got != exitOK {
		t.Fatalf("release = %d", got)
	}

	if tags := f.tr.RemoteTags(); len(tags) != 1 || tags[0] != "1.0.0" {
		t.Errorf("remote tags = %q", tags)
	}
	if !strings.Contains(f.tr.Read("CHANGELOG.md"), "## [1.0.0]") {
		t.Errorf("changelog:\n%s", f.tr.Read("CHANGELOG.md"))
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tag":"1.0.0"`) {
		t.Errorf("report = %s", b)
	}
}

// A maintenance release runs on 1.x, and its changelog commit belongs there —
// not on the default branch, which is what CI_DEFAULT_BRANCH alone would say.
func TestReleaseCommitLandsOnTheBuiltBranch(t *testing.T) {
	f := newFixture(t, "product: image\nversioning:\n  maintenance: [\"1.x\"]\n")
	f.tr.CommitFile("src/main.go", "1", "feat: first")
	f.tr.Tag("1.0.0")
	f.tr.CommitFile("src/main.go", "2", "feat: second")
	f.tr.Tag("2.0.0")
	bare := f.tr.WithRemote()
	f.tr.Git("push", "-q", "origin", "--tags")
	f.tr.Git("checkout", "-q", "-b", "1.x", "1.0.0")
	f.tr.CommitFile("src/main.go", "1b", "fix: backport")
	f.tr.Git("push", "-q", "-u", "origin", "1.x")
	t.Setenv("GITLAB_CI", "true")
	t.Setenv("CI_COMMIT_BRANCH", "1.x")
	t.Setenv("CI_DEFAULT_BRANCH", "main")

	if got := f.next("--output", f.envPath); got != exitOK {
		t.Fatalf("next = %d", got)
	}
	if got := f.release("--input", f.envPath, "--report", filepath.Join(t.TempDir(), "r.json")); got != exitOK {
		t.Fatalf("release = %d", got)
	}
	log := f.tr.Git("--git-dir", bare, "log", "--format=%s", "-1", "1.x")
	if !strings.HasPrefix(log, "chore(release): 1.0.1") {
		t.Errorf("1.x head = %q, want the release commit", log)
	}
	mainLog := f.tr.Git("--git-dir", bare, "log", "--format=%s", "-1", "main")
	if strings.HasPrefix(mainLog, "chore(release)") {
		t.Errorf("release commit landed on main: %q", mainLog)
	}
}

// The handshake also arrives as environment variables, because that is how a
// GitLab dotenv report reaches a later job.
func TestReleaseReadsTheHandshakeFromTheEnvironment(t *testing.T) {
	f := newFixture(t, "product: image\n")
	f.tr.CommitFile("src/main.go", "1", "feat: first release")
	f.tr.WithRemote()

	if got := f.next("--output", f.envPath); got != exitOK {
		t.Fatalf("next = %d", got)
	}
	kv, err := output.ReadDotenv(f.envPath)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}

	// --input names a file that does not exist, so only the environment is left.
	got := f.release("--input", filepath.Join(t.TempDir(), "absent.env"),
		"--report", filepath.Join(t.TempDir(), "r.json"))
	if got != exitOK {
		t.Fatalf("release = %d", got)
	}
	if tags := f.tr.RemoteTags(); len(tags) != 1 {
		t.Errorf("remote tags = %q", tags)
	}
}

func TestCheck(t *testing.T) {
	t.Run("healthy repository", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		f.tr.WithRemote()
		t.Setenv("GITLAB_CI", "true")
		t.Setenv("CI_JOB_TOKEN", "pretend-token")
		if got := f.check(); got != exitOK {
			t.Errorf("exit = %d", got)
		}
	})

	t.Run("github environment is recognised", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		f.tr.WithRemote()
		t.Setenv("GITHUB_ACTIONS", "true")
		t.Setenv("GITHUB_REPOSITORY", "o/r")
		t.Setenv("GITHUB_TOKEN", "pretend-token")
		if got := f.check(); got != exitOK {
			t.Errorf("exit = %d", got)
		}
	})

	t.Run("no token is fatal", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		f.tr.WithRemote()
		t.Setenv("GITLAB_CI", "true")
		if got := f.check(); got != exitError {
			t.Errorf("exit = %d, want %d", got, exitError)
		}
	})

	t.Run("invalid configuration fails", func(t *testing.T) {
		f := newFixture(t, "product: chart\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		if got := f.check(); got != exitError {
			t.Errorf("exit = %d, want %d", got, exitError)
		}
	})

	t.Run("shallow clone is reported", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("a.go", "1", "feat: one")
		f.tr.CommitFile("b.go", "2", "feat: two")
		shallow := f.tr.Clone(1)
		t.Setenv("GITLAB_CI", "true")
		t.Setenv("CI_JOB_TOKEN", "pretend-token")
		if got := run([]string{"check", "--dir", shallow.Dir, "--config", f.cfgPath}); got != exitError {
			t.Errorf("exit = %d, want %d", got, exitError)
		}
	})

	t.Run("json output", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		f.tr.CommitFile("src/main.go", "1", "feat: first")
		f.tr.WithRemote()
		t.Setenv("GITLAB_CI", "true")
		t.Setenv("CI_JOB_TOKEN", "pretend-token")
		if got := f.check("--json"); got != exitOK {
			t.Errorf("exit = %d", got)
		}
	})
}

// The estate keeps release configuration central: ninety repositories carry
// none of their own. These prove the CLI honours that both ways.
func TestDefaultsFromFlagAndEnvironment(t *testing.T) {
	t.Run("a repository with no config of its own releases from defaults", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		// Remove the fixture's own config; only the defaults remain.
		if err := os.Remove(f.cfgPath); err != nil {
			t.Fatal(err)
		}
		defaults := filepath.Join(t.TempDir(), "defaults.yaml")
		if err := os.WriteFile(defaults, []byte("product: image\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.tr.CommitFile("src/main.go", "1", "feat: something shippable")

		got := run([]string{"next", "--dir", f.tr.Dir, "--config", f.cfgPath,
			"--defaults", defaults, "--output", f.envPath})
		if got != exitOK {
			t.Fatalf("exit = %d", got)
		}
		kv, err := output.ReadDotenv(f.envPath)
		if err != nil {
			t.Fatal(err)
		}
		if kv[output.KeyStatus] != "release" || kv[output.KeyVersion] != "1.0.0" {
			t.Errorf("handshake = %+v", kv)
		}
	})

	t.Run("YASRT_DEFAULTS is honoured, which is how the component passes them", func(t *testing.T) {
		f := newFixture(t, "product: image\n")
		if err := os.Remove(f.cfgPath); err != nil {
			t.Fatal(err)
		}
		defaults := filepath.Join(t.TempDir(), "defaults.yaml")
		if err := os.WriteFile(defaults, []byte("product: package\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("YASRT_DEFAULTS", defaults)
		f.tr.CommitFile("src/main.go", "1", "feat: something shippable")

		if got := run([]string{"next", "--dir", f.tr.Dir, "--config", f.cfgPath, "--output", f.envPath}); got != exitOK {
			t.Fatalf("exit = %d", got)
		}
		kv, _ := output.ReadDotenv(f.envPath)
		// product: package derives a v-prefixed tag format.
		if kv[output.KeyTag] != "v1.0.0" {
			t.Errorf("tag = %q — the defaults' product should drive the derivation", kv[output.KeyTag])
		}
	})

	t.Run("the repository still wins over the defaults", func(t *testing.T) {
		f := newFixture(t, "tag_format: \"${version}\"\n")
		defaults := filepath.Join(t.TempDir(), "defaults.yaml")
		if err := os.WriteFile(defaults, []byte("product: package\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.tr.CommitFile("src/main.go", "1", "feat: shippable")

		if got := run([]string{"next", "--dir", f.tr.Dir, "--config", f.cfgPath,
			"--defaults", defaults, "--output", f.envPath}); got != exitOK {
			t.Fatalf("exit = %d", got)
		}
		kv, _ := output.ReadDotenv(f.envPath)
		if kv[output.KeyTag] != "1.0.0" {
			t.Errorf("tag = %q — the repository's tag_format must override the derivation", kv[output.KeyTag])
		}
	})
}
