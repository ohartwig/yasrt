// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package logging_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"git.ole-hartwig.eu/devops/yasrt/internal/logging"
)

const secret = "glcbt-SUPERSECRETTOKEN"

func TestSecretNeverReachesTheLog(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{Secrets: []string{secret}, Verbose: true})

	log.Info("pushing with " + secret)
	log.Info("push failed", "url", "https://gitlab-ci-token:"+secret+"@git.example.com/a/b.git")
	log.Warn("wrapped", "err", errors.New("fatal: auth failed for "+secret))
	log.Info("grouped", "outer", "x", "token", secret)

	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("the token leaked:\n%s", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("expected masking, got:\n%s", out)
	}
}

func TestURLCredentialsMaskedWithoutBeingRegistered(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(&buf, logging.Options{})
	log.Info("remote", "url", "https://gitlab-ci-token:unknown-value@git.example.com/a/b.git")

	out := buf.String()
	if strings.Contains(out, "unknown-value") {
		t.Fatalf("credentials in a URL must be masked even when not registered:\n%s", out)
	}
}

func TestVerboseControlsDebug(t *testing.T) {
	var quiet, loud bytes.Buffer
	logging.New(&quiet, logging.Options{}).Debug("hidden")
	logging.New(&loud, logging.Options{Verbose: true}).Debug("shown")

	if strings.Contains(quiet.String(), "hidden") {
		t.Error("debug should be off by default")
	}
	if !strings.Contains(loud.String(), "shown") {
		t.Error("--verbose should turn debug on")
	}
}

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logging.New(&buf, logging.Options{Format: logging.FormatJSON}).Info("hello", "k", "v")
	out := buf.String()
	if !strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, `"msg":"hello"`) {
		t.Errorf("not JSON:\n%s", out)
	}
}

func TestTextFormatOmitsTimestamps(t *testing.T) {
	// The CI log already timestamps every line; a second one is noise.
	var buf bytes.Buffer
	logging.New(&buf, logging.Options{}).Info("hello")
	if strings.Contains(buf.String(), "time=") {
		t.Errorf("unexpected timestamp:\n%s", buf.String())
	}
}

func TestMaskHelper(t *testing.T) {
	if got := logging.Mask("token "+secret, []string{secret}); strings.Contains(got, secret) {
		t.Errorf("got %q", got)
	}
	if got := logging.Mask("nothing to do", nil); got != "nothing to do" {
		t.Errorf("got %q", got)
	}
	if got := logging.Mask("x", []string{"", "   "}); got != "x" {
		t.Errorf("empty secrets must be ignored, got %q", got)
	}
}
