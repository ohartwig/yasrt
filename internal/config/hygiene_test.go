// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// forbiddenWordHashes keeps customer and organisation names out of this
// repository. The repository is published to a public mirror, and design notes
// and fixtures once carried the names of real installations and organisations
// (AUD-053-F01). Use neutral placeholders such as "example-org" instead.
//
// The names are SHA-256 hashes of the lowercased word, because a plain denylist
// would put the very word it guards against back into the tree. To add one:
// printf '%s' name | shasum -a 256
var forbiddenWordHashes = map[string]struct{}{
	"8ec48b4bfe7adb56f00fdc90760e11ff982ed0955f58f8ef3af170a1c4cb35af": {},
	"507f43168eacc4929060ac361ea604fdaab66b1d8d07de4473201d485e4adcdc": {},
	"edd6fd530a500ec43321c3ec9d6897680275aeb7bae020a62f1ca5c363e824a1": {},
	"570ffd808f50eab736330b9692d0ae1fbb2c6a2c1c6a4364d0105591eaa957cf": {},
	"4c813331d2c1a806a81f7cbf599e686d0e1a68a2fb92d989e6e956c2e1cac490": {},
}

// skippedDirs are paths below the repository root that are not its own text.
var skippedDirs = map[string]struct{}{
	".git": {}, "vendor": {}, "node_modules": {}, ".cache": {}, "dist": {}, "bin": {}, "build": {}, "coverage": {},
}

var wordPattern = regexp.MustCompile(`\p{L}{3,}`)

func TestNoCustomerNames(t *testing.T) {
	root := filepath.Join("..", "..")

	var offenders []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if _, skip := skippedDirs[entry.Name()]; skip && path != root {
				return filepath.SkipDir
			}

			return nil
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		contents, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(contents, 0) >= 0 {
			// Binary files and anything unreadable have no words to check.
			return nil //nolint:nilerr // deliberate: unreadable files are skipped
		}

		for _, word := range wordPattern.FindAllString(string(contents), -1) {
			sum := sha256.Sum256([]byte(strings.ToLower(word)))
			if _, forbidden := forbiddenWordHashes[hex.EncodeToString(sum[:])]; forbidden {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}

				offenders = append(offenders, filepath.ToSlash(rel))

				break
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("these files name a customer or organisation; use a neutral placeholder such as \"example-org\":\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
