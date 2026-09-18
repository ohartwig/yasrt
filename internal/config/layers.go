// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// Layered configuration exists because an organisation usually keeps release
// configuration central: most repositories carry none of their own and rely
// on what the shared CI template provides. What semantic-release's shareable
// configs get wrong is not central defaults but their transport — a package
// resolved at run time, which is where the unpinned dependencies come from.
//
// Defaults therefore arrive as plain files, written by the component that the
// consuming repository already includes and pins. No registry, no network, no
// resolution step: the same YAML, from a source that is reviewed and versioned
// like any other part of the pipeline.

// DefaultsEnv names an environment variable holding one or more default files,
// separated by the OS list separator. The CI template sets it.
const DefaultsEnv = "YASRT_DEFAULTS"

// Layer is one configuration source, named for error messages.
type Layer struct {
	Name string
	Data []byte
}

// LoadLayered reads default layers in order, then the repository's own file,
// and merges them so that later layers win.
//
// The repository file is optional when defaults were given: a repository whose
// component already says what it builds needs no file of its own. With no
// defaults and no file there is nothing to work from, and that is an error.
func LoadLayered(defaultPaths []string, repoPath string) (*Config, error) {
	var layers []Layer
	for _, p := range defaultPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("defaults %s: %w", p, err)
		}
		layers = append(layers, Layer{Name: p, Data: b})
	}

	b, err := os.ReadFile(repoPath)
	switch {
	case err == nil:
		layers = append(layers, Layer{Name: repoPath, Data: b})
	case errors.Is(err, os.ErrNotExist):
		if len(layers) == 0 {
			return nil, fmt.Errorf(
				"%s not found and no defaults given: a repository needs either its own configuration or a component that supplies one", repoPath)
		}
	default:
		return nil, err
	}
	return Merge(layers)
}

// Merge combines layers and validates the result. Each layer is checked for
// unknown fields on its own first, so a typo is reported against the file it is
// in rather than against the merged whole.
func Merge(layers []Layer) (*Config, error) {
	merged := map[string]any{}
	for _, l := range layers {
		if err := checkKnownFields(l); err != nil {
			return nil, err
		}
		var m map[string]any
		if err := yaml.Unmarshal(l.Data, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", l.Name, err)
		}
		merged = deepMerge(merged, m)
	}

	out, err := yaml.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(out))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", describe(layers), err)
	}
	return &c, nil
}

func checkKnownFields(l Layer) error {
	var probe Config
	dec := yaml.NewDecoder(bytes.NewReader(l.Data))
	dec.KnownFields(true)
	if err := dec.Decode(&probe); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", l.Name, err)
	}
	return nil
}

func describe(layers []Layer) string {
	if len(layers) == 0 {
		return "configuration"
	}
	if len(layers) == 1 {
		return layers[0].Name
	}
	return layers[len(layers)-1].Name + " (merged with " + layers[0].Name + " and others)"
}

// deepMerge merges over onto base. Maps are merged key by key; scalars and
// lists are replaced wholesale.
//
// Lists replace rather than append on purpose. Appending reads well for
// ignore.authors and terribly for rules, where order decides the outcome and a
// silently extended list would change which rule matches first. Where extending
// is what you want, the schema says so explicitly — that is what
// extra_non_release_paths is for.
func deepMerge(base, over map[string]any) map[string]any {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range over {
		if existing, ok := out[k]; ok {
			em, eok := existing.(map[string]any)
			vm, vok := v.(map[string]any)
			if eok && vok {
				out[k] = deepMerge(em, vm)
				continue
			}
		}
		out[k] = v
	}
	return out
}
