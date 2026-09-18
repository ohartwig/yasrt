// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/git"
)

// Signing key formats. Which one a key is gets detected from the material
// itself rather than configured: an OpenSSH private key and an armoured PGP
// block are unmistakable, and one fewer knob is one fewer thing to get wrong.
const (
	sshKeyHeader = "-----BEGIN OPENSSH PRIVATE KEY-----"
	// The armour header yasrt matches in order to RECOGNISE a PGP key, not a
	// key: forty characters of delimiter and no key material. Suppressed rather
	// than obfuscated, because a constant spelled out is what makes the
	// detection readable.
	//
	// The suppression is bare rather than rule-scoped, and trailing rather than
	// above: semgrep honours it only on the finding's own line or the one
	// immediately preceding, and the rule-scoped form did not take here even
	// with the id copied from the SARIF output. On a line that is one constant
	// string, suppressing every rule is a blast radius of one line.
	pgpKeyHeader = "-----BEGIN PGP PRIVATE KEY BLOCK-----" // nosemgrep
)

// setupSigning prepares the optional signing key and tells git to use it.
// `auto` degrades to unsigned; `required` fails here, before anything is
// written. The returned cleanup removes any key material written to disk.
func setupSigning(repo *git.Repo, cfg *config.Config, keyB64 string, log *slog.Logger) (bool, func(), error) {
	noop := func() {}

	// Deciding not to sign has to be stated, not merely left unsaid: a global
	// or system git config with commit.gpgsign or tag.gpgsign true would
	// otherwise try to sign with a key this job does not have, and a hardware
	// key would sit waiting for a touch that never comes.
	unsigned := func() (bool, func(), error) {
		for _, k := range []string{"commit.gpgsign", "tag.gpgsign"} {
			if err := repo.Config(k, "false"); err != nil {
				return false, noop, err
			}
		}
		return false, noop, nil
	}

	switch cfg.ReleaseCommit.Sign {
	case config.SignOff:
		return unsigned()
	case config.SignAuto, config.SignRequired:
	}

	required := cfg.ReleaseCommit.Sign == config.SignRequired

	if strings.TrimSpace(keyB64) == "" {
		if required {
			return false, noop, fmt.Errorf("%w: no key was provided", ErrSigningRequired)
		}
		log.Info("no signing key present, continuing unsigned")
		return unsigned()
	}

	key, err := decodeKey(keyB64)
	if err != nil {
		if required {
			return false, noop, fmt.Errorf("%w: %w", ErrSigningRequired, err)
		}
		log.Warn("signing key unusable, continuing unsigned", "err", err)
		return unsigned()
	}

	var cleanup func()
	switch {
	case strings.Contains(key, sshKeyHeader):
		cleanup, err = configureSSHSigning(repo, key)
	case strings.Contains(key, pgpKeyHeader):
		cleanup, err = configureGPGSigning(repo, key)
	default:
		err = errors.New("key is neither an OpenSSH private key nor an armoured PGP block")
	}
	if err != nil {
		if required {
			return false, noop, fmt.Errorf("%w: %w", ErrSigningRequired, err)
		}
		log.Warn("signing key unusable, continuing unsigned", "err", err)
		if cleanup != nil {
			cleanup()
		}
		return unsigned()
	}
	log.Info("signing enabled", "format", formatOf(key))
	return true, cleanup, nil
}

func formatOf(key string) string {
	if strings.Contains(key, sshKeyHeader) {
		return "ssh"
	}
	return "openpgp"
}

// decodeKey accepts the key base64-encoded, which is how it survives GitLab's
// variable masking — armour is multi-line and gets mangled otherwise — but also
// accepts raw armour, so a locally exported key works without ceremony.
func decodeKey(v string) (string, error) {
	v = strings.TrimSpace(v)
	if strings.Contains(v, "-----BEGIN") {
		return v, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(v), ""))
	if err != nil {
		return "", fmt.Errorf("decoding the signing key: %w", err)
	}
	return string(raw), nil
}

// configureSSHSigning writes the key to a private file and points git at it.
// GitLab, GitHub and Forgejo all verify SSH signatures, and organisations
// that already trust SSH keys for human commits need no second key type.
func configureSSHSigning(repo *git.Repo, key string) (func(), error) {
	dir, err := os.MkdirTemp("", "yasrt-signing-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	path := filepath.Join(dir, "signing_key")
	// ssh-keygen refuses a key file that others can read, and so does git.
	if err := os.WriteFile(path, []byte(ensureTrailingNewline(key)), 0o600); err != nil {
		cleanup()
		return nil, err
	}
	for _, kv := range [][2]string{
		{"gpg.format", "ssh"},
		{"user.signingkey", path},
	} {
		if err := repo.Config(kv[0], kv[1]); err != nil {
			cleanup()
			return nil, err
		}
	}
	return cleanup, nil
}

// configureGPGSigning imports an armoured key into a keyring of its own and
// points git at it. The keyring is a private temp directory -- GNUPGHOME for
// this run only -- so the key never lands in whatever keyring the machine's
// user keeps, and disappears with the run, symmetric with the SSH path.
func configureGPGSigning(repo *git.Repo, key string) (func(), error) {
	home, err := os.MkdirTemp(shortTempDir(), "yasrt-gnupg-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}
	env := append(os.Environ(), "GNUPGHOME="+home)
	cleanup := func() {
		// The agent holds the key material in memory; stop it before the
		// directory goes, or it lingers with a socket that no longer exists.
		kill := exec.Command("gpgconf", "--kill", "gpg-agent")
		kill.Env = env
		_ = kill.Run()
		_ = os.RemoveAll(home)
	}

	imp := exec.Command("gpg", "--batch", "--import")
	imp.Env = env
	imp.Stdin = strings.NewReader(key)
	if out, err := imp.CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("importing the signing key: %w: %s", err, out)
	}
	keyID, err := firstSecretKeyID(env)
	if err != nil {
		cleanup()
		return nil, err
	}
	repo.SetEnv("GNUPGHOME", home)
	for _, kv := range [][2]string{
		{"gpg.format", "openpgp"},
		{"user.signingkey", keyID},
	} {
		if err := repo.Config(kv[0], kv[1]); err != nil {
			cleanup()
			return nil, err
		}
	}
	return cleanup, nil
}

// shortTempDir prefers /tmp where it exists: gpg-agent's socket lives inside
// GNUPGHOME and a Unix socket path is limited to about a hundred characters,
// which macOS's per-user temp directory alone nearly exhausts.
func shortTempDir() string {
	if st, err := os.Stat("/tmp"); err == nil && st.IsDir() {
		return "/tmp"
	}
	return os.TempDir()
}

func firstSecretKeyID(env []string) (string, error) {
	list := exec.Command("gpg", "--list-secret-keys", "--with-colons")
	list.Env = env
	out, err := list.Output()
	if err != nil {
		return "", fmt.Errorf("listing secret keys: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if f := strings.Split(line, ":"); len(f) > 4 && f[0] == "sec" {
			return f[4], nil
		}
	}
	return "", errors.New("no secret key found after import")
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
