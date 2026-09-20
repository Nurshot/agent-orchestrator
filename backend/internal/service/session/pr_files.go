package session

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// PRFileSource identifies the immutable pull-request revision shown by the
// Files inspector. It is resolved from persisted SCM facts, never caller-
// supplied revisions.
type PRFileSource struct {
	Number       int
	URL          string
	Label        string
	SourceBranch string
	BaseSHA      string
	HeadSHA      string
}

// PRFiles is the exact base...head read model for one associated pull request.
type PRFiles struct {
	SessionID domain.SessionID
	Source    PRFileSource
	Files     []WorkspaceFileSummary
	Truncated bool
	Summary   WorkspaceSummary
}

// ListPRFiles returns the committed changed-file set for an associated PR.
// Git reads are revision-only and never inspect or mutate the worktree/index.
func (s *Service) ListPRFiles(ctx context.Context, id domain.SessionID, number int, sourceURL string) (PRFiles, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return PRFiles{}, err
	}
	root := rec.Metadata.WorkspacePath
	statuses, previous, err := workspaceDiffNameStatus(ctx, root, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return PRFiles{}, unavailablePRSource()
	}
	counts, err := workspaceDiffNumstat(ctx, root, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return PRFiles{}, unavailablePRSource()
	}
	paths := make([]string, 0, len(statuses))
	for rel := range statuses {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	truncated := len(paths) > maxWorkspaceFiles
	if truncated {
		paths = paths[:maxWorkspaceFiles]
	}
	files := make([]WorkspaceFileSummary, 0, len(paths))
	for _, rel := range paths {
		status := statuses[rel]
		additions, deletions := counts[rel][0], counts[rel][1]
		size := gitRevisionFileSize(ctx, root, pr.HeadSHA, rel, status)
		_, hasTextCounts := counts[rel]
		binary := status != WorkspaceFileDeleted && !hasTextCounts
		files = append(files, WorkspaceFileSummary{Path: rel, PreviousPath: previous[rel], Status: status, Additions: additions, Deletions: deletions, Size: size, Binary: binary})
	}
	return PRFiles{SessionID: id, Source: newPRFileSource(pr), Files: files, Truncated: truncated, Summary: workspaceSummaryFromFiles(files)}, nil
}

// GetPRFile returns one file and its exact base...head diff for an associated PR.
func (s *Service) GetPRFile(ctx context.Context, id domain.SessionID, number int, sourceURL, rawPath string) (WorkspaceFileDetail, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	rel, err := cleanWorkspaceRelativePath(rawPath)
	if err != nil {
		return WorkspaceFileDetail{}, err
	}
	root := rec.Metadata.WorkspacePath
	statuses, previous, err := workspaceDiffNameStatus(ctx, root, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return WorkspaceFileDetail{}, unavailablePRSource()
	}
	status, ok := statuses[rel]
	if !ok {
		return WorkspaceFileDetail{}, apierr.NotFound("PR_FILE_NOT_FOUND", "File is not part of the selected pull request")
	}
	counts, err := workspaceDiffNumstat(ctx, root, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return WorkspaceFileDetail{}, unavailablePRSource()
	}
	additions, deletions := counts[rel][0], counts[rel][1]
	detail := WorkspaceFileDetail{SessionID: id, Path: rel, PreviousPath: previous[rel], Status: status, Additions: additions, Deletions: deletions, Deleted: status == WorkspaceFileDeleted, CompareBaseSHA: pr.BaseSHA, CompareBaseRef: pr.TargetBranch, CompareMode: WorkspaceCompareBase}
	if !detail.Deleted {
		detail.Size = gitRevisionFileSize(ctx, root, pr.HeadSHA, rel, status)
		_, hasTextCounts := counts[rel]
		detail.Binary = !hasTextCounts
		if detail.Size > maxWorkspaceFileBytes {
			detail.ContentTruncated = true
		} else {
			content, contentErr := gitWorkspaceOutput(ctx, root, "show", pr.HeadSHA+":"+rel)
			if contentErr != nil {
				return WorkspaceFileDetail{}, apierr.NotFound("PR_FILE_NOT_FOUND", "File is unavailable at the selected pull request head")
			}
			if !detail.Binary && utf8.ValidString(content) && strings.IndexByte(content, 0) < 0 {
				detail.Content = content
			} else {
				detail.Binary = true
			}
		}
	}
	diffArgs := []string{"diff", "--no-ext-diff", "--find-renames", "--unified=3", pr.BaseSHA + "..." + pr.HeadSHA, "--"}
	if detail.PreviousPath != "" {
		diffArgs = append(diffArgs, detail.PreviousPath)
	}
	diffArgs = append(diffArgs, rel)
	diff, err := gitWorkspaceOutput(ctx, root, diffArgs...)
	if err != nil {
		return WorkspaceFileDetail{}, unavailablePRSource()
	}
	detail.Diff, detail.DiffTruncated = truncateUTF8(diff, maxWorkspaceDiffBytes)
	return detail, nil
}

// GetPRFileRevision reads one immutable side of the selected PR comparison.
// It intentionally does not delegate to workspace revision readers: a PR view
// must never fall back to the mutable session worktree while expanding a hunk.
func (s *Service) GetPRFileRevision(ctx context.Context, id domain.SessionID, number int, sourceURL, rawPath string, side WorkspaceFileBlobSide) (WorkspaceFileRevision, error) {
	rec, pr, err := s.prFileSource(ctx, id, number, sourceURL)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	if side != WorkspaceBlobBefore && side != WorkspaceBlobAfter {
		return WorkspaceFileRevision{}, apierr.Invalid("INVALID_WORKSPACE_REVISION_SIDE", "side must be before or after", nil)
	}
	rel, err := cleanWorkspaceRelativePath(rawPath)
	if err != nil {
		return WorkspaceFileRevision{}, err
	}
	statuses, previous, err := workspaceDiffNameStatus(ctx, rec.Metadata.WorkspacePath, pr.BaseSHA+"..."+pr.HeadSHA)
	if err != nil {
		return WorkspaceFileRevision{}, unavailablePRSource()
	}
	status, ok := statuses[rel]
	if !ok {
		return WorkspaceFileRevision{}, apierr.NotFound("PR_FILE_NOT_FOUND", "File is not part of the selected pull request")
	}
	path, revision := rel, pr.HeadSHA
	if side == WorkspaceBlobBefore {
		if status == WorkspaceFileAdded {
			return WorkspaceFileRevision{SessionID: id, Path: rel, Side: side, Encoding: "utf-8"}, nil
		}
		if previous[rel] != "" {
			path = previous[rel]
		}
		revision = pr.BaseSHA
	}
	size := gitRevisionFileSize(ctx, rec.Metadata.WorkspacePath, revision, path, WorkspaceFileModified)
	result := WorkspaceFileRevision{SessionID: id, Path: rel, Side: side, Encoding: "utf-8", Exists: true, Size: size}
	if size > maxWorkspaceRevisionBytes {
		result.Truncated = true
		return result, nil
	}
	content, err := gitWorkspaceOutput(ctx, rec.Metadata.WorkspacePath, "show", revision+":"+path)
	if err != nil {
		return WorkspaceFileRevision{}, unavailablePRSource()
	}
	result.Revision = hashWorkspaceReviewValue(content)
	result.Binary = !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0
	if !result.Binary {
		result.MediaType = "text/plain"
		result.Content = content
	}
	return result, nil
}

func (s *Service) prFileSource(ctx context.Context, id domain.SessionID, number int, sourceURL string) (domain.SessionRecord, domain.PullRequest, error) {
	rec, err := s.sessionWorkspaceRecord(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, domain.PullRequest{}, err
	}
	prs, err := s.store.ListPRsBySession(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, domain.PullRequest{}, fmt.Errorf("list PRs for files: %w", err)
	}
	for _, pr := range prs {
		if pr.Number != number || (sourceURL != "" && !strings.EqualFold(strings.TrimSpace(pr.URL), strings.TrimSpace(sourceURL))) {
			continue
		}
		if strings.TrimSpace(pr.BaseSHA) == "" || strings.TrimSpace(pr.HeadSHA) == "" {
			return domain.SessionRecord{}, domain.PullRequest{}, unavailablePRSource()
		}
		root, err := s.prWorkspaceRoot(ctx, rec, pr)
		if err != nil {
			return domain.SessionRecord{}, domain.PullRequest{}, err
		}
		rec.Metadata.WorkspacePath = root
		if err := ensurePRRevisionObjects(ctx, root, pr); err != nil {
			return domain.SessionRecord{}, domain.PullRequest{}, err
		}
		return rec, pr, nil
	}
	return domain.SessionRecord{}, domain.PullRequest{}, apierr.NotFound("PR_NOT_FOUND", "Pull request is not associated with this session")
}

// prWorkspaceRoot chooses the registered repository whose origin matches the
// PR's persisted provider repository. Workspace projects can contain child
// repositories, so the session root is not necessarily the PR's git root.
func (s *Service) prWorkspaceRoot(ctx context.Context, rec domain.SessionRecord, pr domain.PullRequest) (string, error) {
	rows, err := s.store.ListSessionWorktrees(ctx, rec.ID)
	if err != nil || len(rows) == 0 {
		return rec.Metadata.WorkspacePath, err
	}
	want := strings.Trim(strings.ToLower(pr.Repo), "/")
	for _, row := range rows {
		if strings.TrimSpace(row.WorktreePath) == "" {
			continue
		}
		origin, originErr := gitWorkspaceOutput(ctx, row.WorktreePath, "remote", "get-url", "origin")
		if originErr != nil {
			continue
		}
		identity, parseErr := domain.ParseRepositoryIdentity(strings.TrimSpace(origin))
		if parseErr != nil {
			continue
		}
		got := strings.ToLower(identity.Namespace + "/" + identity.Name)
		if got == want {
			return row.WorktreePath, nil
		}
	}
	return "", apierr.NotFound("PR_SOURCE_REPOSITORY_NOT_FOUND", "No registered workspace repository matches the selected pull request")
}

// ensurePRRevisionObjects fetches provider-owned review refs only when the
// persisted immutable SHAs are absent. Fetch updates git object/ref storage but
// never HEAD, the index, or worktree files.
func ensurePRRevisionObjects(ctx context.Context, root string, pr domain.PullRequest) error {
	if gitCommitExists(ctx, root, pr.BaseSHA) && gitCommitExists(ctx, root, pr.HeadSHA) {
		return nil
	}
	providerRef := "refs/pull/" + strconv.Itoa(pr.Number) + "/head"
	if strings.EqualFold(pr.Provider, "gitlab") {
		providerRef = "refs/merge-requests/" + strconv.Itoa(pr.Number) + "/head"
	}
	localRef := "refs/ao/pr/" + strconv.Itoa(pr.Number) + "/head"
	_, _ = gitWorkspaceOutput(ctx, root, "fetch", "--no-tags", "origin", "+"+providerRef+":"+localRef)
	if !gitCommitExists(ctx, root, pr.BaseSHA) || !gitCommitExists(ctx, root, pr.HeadSHA) {
		return unavailablePRSource()
	}
	return nil
}

func newPRFileSource(pr domain.PullRequest) PRFileSource {
	label := "PR #" + strconv.Itoa(pr.Number)
	if branch := strings.TrimSpace(pr.SourceBranch); branch != "" {
		label += " · " + branch
	} else if title := strings.TrimSpace(pr.Title); title != "" {
		label += " · " + title
	}
	return PRFileSource{Number: pr.Number, URL: pr.URL, Label: label, SourceBranch: pr.SourceBranch, BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA}
}

func unavailablePRSource() error {
	return apierr.NotFound("PR_SOURCE_UNAVAILABLE", "The selected pull request revisions are unavailable locally")
}

func gitRevisionFileSize(ctx context.Context, root, rev, rel string, status WorkspaceFileStatus) int64 {
	if status == WorkspaceFileDeleted {
		return 0
	}
	out, err := gitWorkspaceOutput(ctx, root, "cat-file", "-s", rev+":"+rel)
	if err != nil {
		return 0
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	return size
}
