package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

var ErrFileNotFoundAtCommit = errors.New("file did not exist at this commit")
var ErrSSHNotSupported = errors.New("SSH remotes are not supported yet, please use an HTTPS remote URL (e.g. https://github.com/user/repo.git) with a personal access token")

type GitAuthor struct {
	Name  string
	Email string
}

type CommitInfo struct {
	Hash    string    `json:"hash"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	When    time.Time `json:"when"`
	Message string    `json:"message"`
}

type RemoteStatus struct {
	HasRemote     bool        `json:"has_remote"`
	RemoteURL     string      `json:"remote_url"`
	Branch        string      `json:"branch"`
	Ahead         int         `json:"ahead"`
	Behind        int         `json:"behind"`
	LastCommit    *CommitInfo `json:"last_commit"`
	AuthConnected bool        `json:"auth_connected"`
}

// Repo wraps a go-git repository handle. root is the absolute path to the
// repository's working tree root, which may be an ancestor of the journal
// directory when an existing user-managed repo is detected.
type Repo struct {
	repo     *git.Repository
	worktree *git.Worktree
	root     string
}

// Open detects an existing repository at journalDir or any ancestor
// directory (mirroring plain `git` CLI behaviour), so a user who already
// manages their journal under their own git repo is respected rather than
// overridden.
func Open(journalDir string) (*Repo, error) {
	repo, err := git.PlainOpenWithOptions(journalDir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	root := worktree.Filesystem.Root()

	return &Repo{repo: repo, worktree: worktree, root: root}, nil
}

// Init creates a brand new non-bare repository at journalDir. Only call this
// when Open reports the repo doesn't exist and the user opted into
// auto-initializing one.
func Init(journalDir string) (*Repo, error) {
	repo, err := git.PlainInit(journalDir, false)
	if err != nil {
		return nil, err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	return &Repo{repo: repo, worktree: worktree, root: journalDir}, nil
}

// OpenAt opens a repository only if one exists exactly at journalDir,
// without walking up to ancestor directories the way Open does.
func OpenAt(journalDir string) (*Repo, error) {
	repo, err := git.PlainOpenWithOptions(journalDir, &git.PlainOpenOptions{DetectDotGit: false})
	if err != nil {
		return nil, err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	return &Repo{repo: repo, worktree: worktree, root: journalDir}, nil
}

// EnsureDedicated opens (or creates) a repository scoped exactly to
// journalDir, ignoring any ancestor repository. This is the "create a new
// repository just for the ledger files" choice, used even when an ancestor
// repository is present but the user doesn't want to piggyback on it.
func EnsureDedicated(journalDir string) (*Repo, error) {
	repo, err := OpenAt(journalDir)
	if err == nil {
		return repo, nil
	}

	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, err
	}

	return Init(journalDir)
}

// RepoDetection reports what git repository (if any) can be found for
// journalDir without opening or creating anything persistent. It's used to
// offer the user a choice between reusing an already-detected ancestor
// repository and creating a new one scoped to just the journal directory,
// rather than silently picking one on their behalf.
type RepoDetection struct {
	Found          bool
	Root           string
	IsAtJournalDir bool
}

func Detect(journalDir string) RepoDetection {
	repo, err := Open(journalDir)
	if err != nil {
		return RepoDetection{}
	}

	return RepoDetection{Found: true, Root: repo.Root(), IsAtJournalDir: repo.Root() == journalDir}
}

func (r *Repo) Root() string {
	return r.root
}

func (r *Repo) IsClean() (bool, error) {
	status, err := r.worktree.Status()
	if err != nil {
		return false, err
	}

	return status.IsClean(), nil
}

// StageAndCommitAll stages every changed path under journalDir (scoping to
// that subtree when the detected repo root is an ancestor, so a pre-existing
// user-managed repo with unrelated content elsewhere isn't touched) and
// commits. Returns changed=false and creates no commit when the working tree
// (within scope) is already clean.
func (r *Repo) StageAndCommitAll(journalDir string, message string, author GitAuthor) (hash string, changed bool, err error) {
	status, err := r.worktree.Status()
	if err != nil {
		return "", false, err
	}

	relScope, err := filepath.Rel(r.root, journalDir)
	if err != nil {
		return "", false, err
	}
	relScope = filepath.ToSlash(relScope)
	scoped := relScope != "."

	anyStaged := false
	for path := range status {
		if scoped && !isUnderScope(path, relScope) {
			continue
		}

		if _, err := r.worktree.Add(path); err != nil {
			return "", false, err
		}
		anyStaged = true
	}

	if !anyStaged {
		return "", false, nil
	}

	commitHash, err := r.worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  author.Name,
			Email: author.Email,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", false, err
	}

	return commitHash.String(), true, nil
}

func isUnderScope(path string, relScope string) bool {
	path = filepath.ToSlash(path)
	return path == relScope || strings.HasPrefix(path, relScope+"/")
}

// LogForPath returns the commit history touching relPath, newest first,
// capped at limit entries. An empty relPath returns the repository's full
// commit log (no path filter).
func (r *Repo) LogForPath(relPath string, limit int) ([]CommitInfo, error) {
	opts := &git.LogOptions{}
	if relPath != "" {
		p := filepath.ToSlash(relPath)
		opts.FileName = &p
	}

	iter, err := r.repo.Log(opts)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	commits := []CommitInfo{}
	err = iter.ForEach(func(c *object.Commit) error {
		if len(commits) >= limit {
			return storerStop
		}

		commits = append(commits, CommitInfo{
			Hash:    c.Hash.String(),
			Author:  c.Author.Name,
			Email:   c.Author.Email,
			When:    c.Author.When,
			Message: strings.TrimRight(c.Message, "\n"),
		})
		return nil
	})
	if err != nil && !errors.Is(err, storerStop) {
		return nil, err
	}

	return commits, nil
}

// storerStop is a sentinel used to break out of the commit iterator early
// once the requested limit is reached.
var storerStop = errors.New("stop")

// ContentAt returns the file content of relPath as of commitHash.
func (r *Repo) ContentAt(commitHash string, relPath string) (string, error) {
	relPath = filepath.ToSlash(relPath)

	commit, err := r.repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return "", err
	}

	file, err := commit.File(relPath)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return "", ErrFileNotFoundAtCommit
		}
		return "", err
	}

	return file.Contents()
}

// EnsureGitignore idempotently appends any missing Paisa-managed ignore
// entries to <repoRoot>/.gitignore. Returns true if the file was modified.
func EnsureGitignore(repoRoot string) (bool, error) {
	entries := []string{
		"*.backup.*",
		"paisa.db",
		"paisa.db-journal",
		"paisa.db-shm",
		"paisa.db-wal",
		"git-credentials.enc",
		".git-credentials-key",
	}

	path := filepath.Join(repoRoot, ".gitignore")

	existing := []byte{}
	if content, err := os.ReadFile(path); err == nil {
		existing = content
	} else if !os.IsNotExist(err) {
		return false, err
	}

	lines := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		lines[strings.TrimSpace(line)] = true
	}

	missing := []string{}
	for _, entry := range entries {
		if !lines[entry] {
			missing = append(missing, entry)
		}
	}

	if len(missing) == 0 {
		return false, nil
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if buf.Len() > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		buf.WriteString("\n")
	}
	for _, entry := range missing {
		buf.WriteString(entry)
		buf.WriteString("\n")
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return false, err
	}

	return true, nil
}

// Status reports the remote configuration and, best-effort, ahead/behind
// counts. Network errors never fail the call outright, they only degrade
// Ahead/Behind to -1 (unknown).
func (r *Repo) Status(remoteName string, branch string, auth transport.AuthMethod) (RemoteStatus, error) {
	status := RemoteStatus{Ahead: -1, Behind: -1}

	remote, err := r.repo.Remote(remoteName)
	if err != nil {
		if errors.Is(err, git.ErrRemoteNotFound) {
			return status, nil
		}
		return status, err
	}

	status.HasRemote = true
	if len(remote.Config().URLs) > 0 {
		status.RemoteURL = remote.Config().URLs[0]
	}
	status.Branch = branch

	if lastCommits, err := r.LogForPath("", 1); err == nil && len(lastCommits) > 0 {
		status.LastCommit = &lastCommits[0]
	}

	if auth != nil {
		if isSSHURL(status.RemoteURL) {
			return status, nil
		}

		ahead, behind, err := r.computeAheadBehind(remoteName, branch, auth)
		if err == nil {
			status.Ahead = ahead
			status.Behind = behind
		}
	}

	return status, nil
}

func (r *Repo) computeAheadBehind(remoteName string, branch string, auth transport.AuthMethod) (ahead int, behind int, err error) {
	err = r.repo.Fetch(&git.FetchOptions{RemoteName: remoteName, Auth: auth, RefSpecs: []config.RefSpec{
		config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, remoteName, branch)),
	}})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return 0, 0, err
	}

	localHashes, err := r.hashSetForRef(plumbing.HEAD.String())
	if err != nil {
		return 0, 0, err
	}

	remoteRef := fmt.Sprintf("refs/remotes/%s/%s", remoteName, branch)
	remoteHashes, err := r.hashSetForRef(remoteRef)
	if err != nil {
		return 0, 0, err
	}

	for h := range localHashes {
		if !remoteHashes[h] {
			ahead++
		}
	}
	for h := range remoteHashes {
		if !localHashes[h] {
			behind++
		}
	}

	return ahead, behind, nil
}

func (r *Repo) hashSetForRef(ref string) (map[string]bool, error) {
	hash, err := r.repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return nil, err
	}

	iter, err := r.repo.Log(&git.LogOptions{From: *hash})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	set := map[string]bool{}
	count := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if count >= 500 {
			return storerStop
		}
		set[c.Hash.String()] = true
		count++
		return nil
	})
	if err != nil && !errors.Is(err, storerStop) {
		return nil, err
	}

	return set, nil
}

// HTTPAuth builds a transport.AuthMethod for a personal access token,
// suitable for passing to Status/Push. Works uniformly across GitHub,
// GitLab, and self-hosted PAT auth over HTTPS.
func HTTPAuth(token string) transport.AuthMethod {
	return &githttp.BasicAuth{Username: "x-access-token", Password: token}
}

func isSSHURL(url string) bool {
	return strings.HasPrefix(url, "git@") || strings.HasPrefix(url, "ssh://")
}

// Push pushes branch to remoteName using the given token (HTTPS basic-auth
// style, works uniformly across GitHub/GitLab/self-hosted PAT auth).
func (r *Repo) Push(remoteName string, branch string, token string) error {
	remote, err := r.repo.Remote(remoteName)
	if err != nil {
		return err
	}

	if len(remote.Config().URLs) > 0 && isSSHURL(remote.Config().URLs[0]) {
		return ErrSSHNotSupported
	}

	auth := &githttp.BasicAuth{Username: "x-access-token", Password: token}

	err = r.repo.Push(&git.PushOptions{
		RemoteName: remoteName,
		Auth:       auth,
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch)),
		},
	})
	if err != nil && errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}

	return err
}

// SetRemote configures (creating or replacing) the named remote to point at
// url.
func (r *Repo) SetRemote(name string, url string) error {
	_ = r.repo.DeleteRemote(name)

	_, err := r.repo.CreateRemote(&config.RemoteConfig{
		Name: name,
		URLs: []string{url},
	})
	return err
}
