// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

// Package logging configures the logger and guarantees that credentials never
// reach it. Masking lives in the handler rather than at each call site, because
// a call site can be forgotten and a handler cannot.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"git.ole-hartwig.eu/devops/yasrt/internal/git"
)

// Format selects the output shape.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Options configure the logger.
type Options struct {
	Format  Format
	Verbose bool
	Secrets []string
}

// New builds a logger whose every record passes through secret masking.
func New(w io.Writer, opts Options) *slog.Logger {
	level := slog.LevelInfo
	if opts.Verbose {
		level = slog.LevelDebug
	}
	var h slog.Handler
	switch opts.Format {
	case FormatJSON:
		h = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	default:
		h = slog.NewTextHandler(w, &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				// Timestamps are noise in a CI log, which already timestamps.
				if a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			},
		})
	}
	return slog.New(&masking{inner: h, secrets: nonEmpty(opts.Secrets)})
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// masking wraps a handler and rewrites every string it can see.
type masking struct {
	inner   slog.Handler
	secrets []string
}

func (m *masking) Enabled(ctx context.Context, l slog.Level) bool { return m.inner.Enabled(ctx, l) }

func (m *masking) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, m.clean(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(m.cleanAttr(a))
		return true
	})
	return m.inner.Handle(ctx, out)
}

func (m *masking) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		cleaned = append(cleaned, m.cleanAttr(a))
	}
	return &masking{inner: m.inner.WithAttrs(cleaned), secrets: m.secrets}
}

func (m *masking) WithGroup(name string) slog.Handler {
	return &masking{inner: m.inner.WithGroup(name), secrets: m.secrets}
}

func (m *masking) cleanAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, m.clean(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			return slog.String(a.Key, m.clean(err.Error()))
		}
		if s, ok := a.Value.Any().(string); ok {
			return slog.String(a.Key, m.clean(s))
		}
	case slog.KindGroup:
		grp := a.Value.Group()
		cleaned := make([]any, 0, len(grp))
		for _, g := range grp {
			cleaned = append(cleaned, m.cleanAttr(g))
		}
		return slog.Group(a.Key, cleaned...)
	}
	return a
}

// Clean masks a string with the configured secrets. Exported so that error
// paths outside the logger can use the same rule.
func (m *masking) clean(s string) string {
	for _, sec := range m.secrets {
		s = strings.ReplaceAll(s, sec, "***")
	}
	return git.MaskURLCredentials(s)
}

// Mask applies the same masking outside a logger, for text written straight to
// stdout or to a report file.
func Mask(s string, secrets []string) string {
	for _, sec := range nonEmpty(secrets) {
		s = strings.ReplaceAll(s, sec, "***")
	}
	return git.MaskURLCredentials(s)
}
