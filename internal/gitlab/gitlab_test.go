// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: MIT

package gitlab_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.ole-hartwig.eu/yasrt/cli/internal/gitlab"
)

func newServer(t *testing.T, h http.HandlerFunc) (*gitlab.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL+"/api/v4", "42", "job-token-value")
	c.Backoff = time.Millisecond
	return c, srv
}

func TestRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, `[]`)
	})
	if _, err := c.ListProtectedTags(context.Background()); err != nil {
		t.Fatalf("should have recovered: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"403 Forbidden"}`)
	})
	_, err := c.ListProtectedTags(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *gitlab.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("a 403 will not fix itself; calls = %d", got)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("the error should name the status: %v", err)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	c.MaxAttempts = 2
	if _, err := c.ListProtectedTags(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestContextCancellation(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListProtectedTags(ctx); err == nil {
		t.Fatal("expected the cancelled context to surface")
	}
}

func TestProtectedBranchAndTagReads(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/protected_branches"):
			io.WriteString(w, `[{"name":"main","merge_access_levels":[{"access_level":40}],"push_access_levels":[{"access_level":40}]}]`)
		case strings.HasSuffix(r.URL.Path, "/protected_tags"):
			io.WriteString(w, `[{"name":"v*","create_access_levels":[{"access_level":40}]},{"name":"*","create_access_levels":[{"access_level":30}]}]`)
		}
	})

	bs, err := c.ListProtectedBranches(context.Background())
	if err != nil || len(bs) != 1 {
		t.Fatalf("bs=%+v err=%v", bs, err)
	}
	b := bs[0]
	if got := b.LowestMergeLevel(); got != gitlab.AccessMaintainer {
		t.Errorf("merge level = %v", got)
	}
	if got := b.LowestMergeLevel().String(); got != "Maintainer" {
		t.Errorf("name = %q", got)
	}

	tags, err := c.ListProtectedTags(context.Background())
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags=%+v err=%v", tags, err)
	}
	if got := tags[1].LowestCreateLevel(); got != gitlab.AccessDeveloper {
		t.Errorf("create level = %v", got)
	}
}

func TestAccessLevelNames(t *testing.T) {
	for lvl, want := range map[gitlab.AccessLevel]string{
		gitlab.AccessNoOne: "no one", gitlab.AccessDeveloper: "Developer",
		gitlab.AccessMaintainer: "Maintainer", gitlab.AccessOwner: "Owner",
	} {
		if got := lvl.String(); got != want {
			t.Errorf("%d = %q, want %q", int(lvl), got, want)
		}
	}
}
