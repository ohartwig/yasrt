// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/ohartwig/yasrt/internal/analyze"
	"github.com/ohartwig/yasrt/internal/config"
	"github.com/ohartwig/yasrt/internal/forge"
)

func publishRelease(ctx context.Context, o Options, cfg *config.Config, res *analyze.Result,
	notes string, log *slog.Logger) (string, StepStatus, string, error) {

	if existing, ok, err := o.Forge.GetRelease(ctx, res.Tag); err != nil {
		return "", StepFailed, err.Error(), err
	} else if ok {
		log.Info("release already exists, skipping", "tag", res.Tag, "forge", string(o.Forge.Kind()))
		return existing.URL, StepSkipped, "already exists", nil
	}

	vars := config.Vars{Version: res.Version, Tag: res.Tag}
	var links []forge.Link
	var uploads []forge.Upload
	for _, a := range cfg.GitLabRelease.Assets {
		if a.IsUpload() {
			ups, err := expandUploads(o.Repo.Dir(), a, vars)
			if err != nil {
				return "", StepFailed, err.Error(), err
			}
			uploads = append(uploads, ups...)
			continue
		}
		links = append(links, forge.Link{
			Name:     vars.Expand(a.Name),
			URL:      vars.Expand(a.URL),
			LinkType: a.LinkType,
		})
	}

	// GitLab stores uploads independently of the release, so they go first
	// and become links on it. A failed upload therefore aborts before the
	// release exists, and a re-run starts clean.
	if o.Forge.SupportsLinks() {
		for _, up := range uploads {
			l, err := o.Forge.UploadAsset(ctx, nil, up)
			if err != nil {
				return "", StepFailed, err.Error(), err
			}
			log.Info("asset uploaded", "name", up.Name, "url", l.URL)
			links = append(links, l)
		}
	}

	body := notes
	if !o.Forge.SupportsLinks() {
		// Nowhere to attach them, so they go into the body rather than being
		// lost. Stated in the log, not silently.
		if md := forge.LinksAsMarkdown(links); md != "" {
			body += md
			log.Info("this forge has no release-link endpoint; links appended to the body",
				"forge", string(o.Forge.Kind()), "links", len(links))
		}
	}

	rel, err := o.Forge.CreateRelease(ctx, res.Tag, vars.Expand(cfg.GitLabRelease.Name), body)
	if err != nil {
		return "", StepFailed, err.Error(), err
	}
	log.Info("release published", "tag", res.Tag, "url", rel.URL, "forge", string(o.Forge.Kind()))

	if o.Forge.SupportsLinks() && len(links) > 0 {
		if err := o.Forge.AddLinks(ctx, res.Tag, links); err != nil {
			// A missing link is not worth discarding a published release over.
			log.Warn("release links failed", "err", err)
		}
	}

	// GitHub and Forgejo attach files to the release object, so these can
	// only happen now. A failure here is fatal so the pipeline goes red, but
	// the release stays: a re-run finds it and skips, so attach the missing
	// file by hand rather than expecting a retry to do it.
	if !o.Forge.SupportsLinks() {
		for _, up := range uploads {
			l, err := o.Forge.UploadAsset(ctx, rel, up)
			if err != nil {
				return rel.URL, StepFailed, err.Error(), err
			}
			log.Info("asset attached", "name", up.Name, "url", l.URL)
		}
	}
	return rel.URL, StepDone, rel.URL, nil
}

// expandUploads resolves one path asset to the files it names. A glob that
// matches nothing is an error: an asset that was configured and is missing is
// a broken build, not an empty set.
func expandUploads(dir string, a config.ReleaseLink, vars config.Vars) ([]forge.Upload, error) {
	pattern := vars.Expand(a.Path)
	matches, err := doublestar.Glob(os.DirFS(dir), pattern, doublestar.WithFilesOnly())
	if err != nil {
		return nil, fmt.Errorf("gitlab_release.assets: path %q: %w", a.Path, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("gitlab_release.assets: path %q matches no file", a.Path)
	}
	slices.Sort(matches)
	out := make([]forge.Upload, 0, len(matches))
	for _, m := range matches {
		name := filepath.Base(m)
		if len(matches) == 1 && a.Name != "" {
			name = vars.Expand(a.Name)
		}
		out = append(out, forge.Upload{
			Name:     name,
			Path:     filepath.Join(dir, filepath.FromSlash(m)),
			Package:  vars.Expand(a.Package),
			Version:  vars.Version.String(),
			LinkType: a.LinkType,
		})
	}
	return out, nil
}

func runTriggers(ctx context.Context, o Options, cfg *config.Config, res *analyze.Result,
	log *slog.Logger) []TriggerResult {

	if len(cfg.AfterRelease.Triggers) == 0 {
		return nil
	}
	var out []TriggerResult
	triggerer, canTrigger := o.Forge.(pipelineTriggerer)
	for _, t := range cfg.AfterRelease.Triggers {
		r := TriggerResult{Project: t.Project, Ref: t.Ref}
		switch {
		case o.Forge == nil:
			r.Error = "no forge client"
		case !canTrigger:
			// Only GitLab has the endpoint yasrt posts to. Dispatching a
			// workflow elsewhere is a different call with different
			// permissions, and guessing at it would be worse than saying so.
			r.Error = fmt.Sprintf("%s has no pipeline-trigger endpoint", o.Forge.Kind())
			log.Warn("follow-up trigger not delivered", "forge", string(o.Forge.Kind()), "project", t.Project)
		default:
			// Project and ref expand like the variables do, so a repository
			// whose build runs on the tag pipeline can start that pipeline
			// itself: project ${CI_PROJECT_PATH}, ref ${tag}. A push with the
			// job token starts none, and this is the sanctioned way to get one.
			expand := func(s string) string {
				return expandEnv(config.Vars{Version: res.Version, Tag: res.Tag}.Expand(s))
			}
			vars := make(map[string]string, len(t.Variables))
			for k, v := range t.Variables {
				vars[k] = expand(v)
			}
			r.Project, r.Ref = expand(t.Project), expand(t.Ref)
			token := ""
			if t.TokenVar != "" {
				if token = os.Getenv(t.TokenVar); token == "" {
					log.Info("trigger token variable unset; the job token is sent instead", "project", t.Project, "var", t.TokenVar)
				}
			}
			url, err := triggerer.TriggerPipeline(ctx, r.Project, r.Ref, vars, token)
			if err != nil {
				// Non-fatal by design: the release is already published.
				r.Error = err.Error()
				log.Warn("follow-up trigger failed", "project", t.Project, "err", err)
			} else {
				r.Pipeline = url
				log.Info("follow-up pipeline triggered", "project", t.Project, "pipeline", url)
			}
		}
		out = append(out, r)
	}
	return out
}

// pipelineTriggerer is implemented only where the platform has an endpoint for
// starting a pipeline in another project.
// token, when set, is a pipeline trigger token of the target project and
// replaces the job token in the request.
type pipelineTriggerer interface {
	TriggerPipeline(ctx context.Context, project, ref string, vars map[string]string, token string) (string, error)
}

// expandEnv resolves ${CI_PROJECT_PATH} style references in trigger variables.
func expandEnv(s string) string {
	return os.Expand(s, func(k string) string { return os.Getenv(k) })
}
