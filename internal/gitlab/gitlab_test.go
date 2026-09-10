// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package gitlab_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.ole-hartwig.eu/devops/yasrt/internal/gitlab"
)

func newServer(t *testing.T, h http.HandlerFunc) (*gitlab.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := gitlab.New(srv.URL+"/api/v4", "42", "job-token-value")
	c.Backoff = time.Millisecond
	return c, srv
}

func TestCreateRelease(t *testing.T) {
	var gotPath, gotToken, gotBody string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("JOB-TOKEN")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"tag_name":"3.4.0","name":"3.4.0","_links":{"self":"https://git/x/-/releases/3.4.0"}}`)
	})

	rel, err := c.CreateRelease(context.Background(), gitlab.CreateReleaseRequest{
		TagName: "3.4.0", Name: "3.4.0", Description: "### Features\n\n- thing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v4/projects/42/releases" {
		t.Errorf("path = %s", gotPath)
	}
	if gotToken != "job-token-value" {
		t.Errorf("the job token must travel in the JOB-TOKEN header, got %q", gotToken)
	}
	if !strings.Contains(gotBody, `"tag_name":"3.4.0"`) {
		t.Errorf("body = %s", gotBody)
	}
	if rel.URL() != "https://git/x/-/releases/3.4.0" {
		t.Errorf("URL = %q", rel.URL())
	}
}

func TestGetReleaseMissingIsNotAnError(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"404 Not found"}`)
	})
	rel, ok, err := c.GetRelease(context.Background(), "3.4.0")
	if err != nil {
		t.Fatalf("a missing release is the normal case, not an error: %v", err)
	}
	if ok || rel != nil {
		t.Errorf("ok=%v rel=%+v", ok, rel)
	}
}

func TestGetReleaseFound(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tag_name":"3.4.0"}`)
	})
	rel, ok, err := c.GetRelease(context.Background(), "3.4.0")
	if err != nil || !ok || rel.TagName != "3.4.0" {
		t.Fatalf("rel=%+v ok=%v err=%v", rel, ok, err)
	}
}

func TestReleaseTagIsPathEscaped(t *testing.T) {
	var gotPath string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		io.WriteString(w, `{"tag_name":"x"}`)
	})
	// A tag format can legitimately contain a slash, e.g. release/1.2.3.
	if _, _, err := c.GetRelease(context.Background(), "release/1.2.3"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPath, "release%2F1.2.3") {
		t.Errorf("tag must be escaped in the path, got %s", gotPath)
	}
}

func TestAddReleaseLink(t *testing.T) {
	var body map[string]any
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
	})
	err := c.AddReleaseLink(context.Background(), "3.4.0",
		gitlab.Link{Name: "binary", URL: "https://example/x", LinkType: "package"})
	if err != nil {
		t.Fatal(err)
	}
	if body["name"] != "binary" || body["link_type"] != "package" {
		t.Errorf("body = %+v", body)
	}
}

func TestTriggerPipeline(t *testing.T) {
	var gotPath string
	var body map[string]any
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		io.WriteString(w, `{"id":7,"web_url":"https://git/p/-/pipelines/7","status":"created"}`)
	})

	p, err := c.TriggerPipeline(context.Background(), "devops/renovate-runner", "main",
		map[string]string{"FAST_LANE": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPath, "devops%2Frenovate-runner") {
		t.Errorf("the target project path must be escaped, got %s", gotPath)
	}
	if body["ref"] != "main" {
		t.Errorf("body = %+v", body)
	}
	if p.ID != 7 {
		t.Errorf("pipeline = %+v", p)
	}
}

func TestRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"tag_name":"3.4.0"}`)
	})
	if _, err := c.CreateRelease(context.Background(), gitlab.CreateReleaseRequest{TagName: "3.4.0"}); err != nil {
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
	_, err := c.CreateRelease(context.Background(), gitlab.CreateReleaseRequest{TagName: "3.4.0"})
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
	if _, err := c.CreateRelease(context.Background(), gitlab.CreateReleaseRequest{TagName: "x"}); err == nil {
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
	if _, err := c.CreateRelease(ctx, gitlab.CreateReleaseRequest{TagName: "x"}); err == nil {
		t.Fatal("expected the cancelled context to surface")
	}
}
