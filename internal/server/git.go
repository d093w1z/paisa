package server

import (
	"net/http"
	"path/filepath"
	"regexp"

	"github.com/ananthakumaran/paisa/internal/config"
	"github.com/ananthakumaran/paisa/internal/gitrepo"
	"github.com/ananthakumaran/paisa/internal/utils"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

var commitHashPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func gitAuthorFromConfig() gitrepo.GitAuthor {
	c := config.GetConfig()
	return gitrepo.GitAuthor{Name: c.GitHistory.AuthorName, Email: c.GitHistory.AuthorEmail}
}

// resolveActiveRepo returns the repository that should be used for git
// history, honoring GitHistory.RepoMode. Git history works entirely
// locally — no remote is ever required — but *which* local repository to
// use is only ever picked automatically when there's truly nothing to
// choose between (nothing found anywhere, or a repository already sits
// exactly at the journal directory). If an ancestor repository exists and
// no explicit choice has been recorded yet, this deliberately returns nil
// rather than silently adopting it, so a user's unrelated personal repo
// (e.g. a dotfiles repo) is never assumed to be the right place to commit
// financial data without them saying so via POST /api/git/init.
//
// write=false is used by read-only callers (status/history/diff) and never
// creates a repository or persists an auto-resolved mode. write=true is
// used by the save/sync paths and may create a repository when AutoInit is
// enabled and there's nothing ambiguous about doing so.
func resolveActiveRepo(write bool) *gitrepo.Repo {
	c := config.GetConfig()
	if c.GitHistory.Enabled != config.Yes {
		return nil
	}

	dir := filepath.Dir(config.GetJournalPath())

	switch c.GitHistory.RepoMode {
	case "new":
		if !write {
			repo, err := gitrepo.OpenAt(dir)
			if err != nil {
				return nil
			}
			return repo
		}

		repo, err := gitrepo.EnsureDedicated(dir)
		if err != nil {
			log.Warn("git history: dedicated repo unavailable, falling back to file backups: ", err)
			return nil
		}
		ensureGitignoreQuiet(repo)
		return repo

	case "existing":
		repo, err := gitrepo.Open(dir)
		if err != nil {
			log.Warn("git history: configured to use an existing repository, but none could be found: ", err)
			return nil
		}
		ensureGitignoreQuiet(repo)
		return repo

	default:
		detection := gitrepo.Detect(dir)

		if detection.Found && detection.IsAtJournalDir {
			// Unambiguous: a repository already lives exactly at the
			// journal directory (e.g. the user ran `git init` there
			// themselves), so there's nothing to ask about.
			repo, err := gitrepo.OpenAt(dir)
			if err != nil {
				return nil
			}
			if write {
				persistRepoMode("new")
			}
			ensureGitignoreQuiet(repo)
			return repo
		}

		if !detection.Found {
			// Nothing found anywhere: the only possible choice is to
			// create one, so it's safe to auto-resolve when the user
			// opted into zero-friction setup via AutoInit. Otherwise wait
			// for an explicit choice (there's still a choice being made,
			// it's just that "new" is the only option).
			if !write || c.GitHistory.AutoInit != config.Yes {
				return nil
			}

			repo, err := gitrepo.Init(dir)
			if err != nil {
				log.Warn("git history: auto-init failed: ", err)
				return nil
			}
			persistRepoMode("new")
			ensureGitignoreQuiet(repo)
			return repo
		}

		// An ancestor repository exists but isn't exactly the journal
		// directory: genuinely ambiguous, requires an explicit choice via
		// POST /api/git/init before any commit happens.
		return nil
	}
}

func ensureGitignoreQuiet(repo *gitrepo.Repo) {
	if _, err := gitrepo.EnsureGitignore(repo.Root()); err != nil {
		log.Warn("git history: failed to update .gitignore: ", err)
	}
}

func persistRepoMode(mode string) {
	c := config.GetConfig()
	if c.GitHistory.RepoMode == mode {
		return
	}

	c.GitHistory.RepoMode = mode
	if err := config.SaveConfigObject(c); err != nil {
		log.Warn("git history: failed to persist repository mode: ", err)
	}
}

// openGitRepo is the write-path entry point used by SaveFile/sync: it may
// create/initialize a repository when doing so is unambiguous (see
// resolveActiveRepo). Returns nil when git history is disabled, needs an
// explicit setup choice, or couldn't be resolved (callers should fall back
// to the filesystem backup mechanism rather than treating this as fatal).
func openGitRepo() *gitrepo.Repo {
	return resolveActiveRepo(true)
}

// ensureGitCommitted stages and commits any outstanding changes under the
// journal directory. Used both on general sync (to capture edits made
// outside the web editor) and, ahead of time, by SaveFile.
func ensureGitCommitted(message string) error {
	repo := openGitRepo()
	if repo == nil {
		return nil
	}

	dir := filepath.Dir(config.GetJournalPath())
	_, changed, err := repo.StageAndCommitAll(dir, message, gitAuthorFromConfig())
	if err != nil {
		return err
	}

	if changed && config.GetConfig().GitHistory.AutoPush == config.Yes {
		go attemptAutoPush(repo)
	}

	return nil
}

func attemptAutoPush(repo *gitrepo.Repo) {
	remote := config.GetConfig().GitHistory.Remote
	if remote.URL == "" {
		return
	}

	token, err := gitrepo.LoadToken(config.GetConfigDir(), remote.URL)
	if err != nil {
		log.Warn("git history: auto-push skipped, no stored credential: ", err)
		return
	}

	if err := repo.Push(remote.Name, remote.Branch, token); err != nil {
		log.Warn("git history: auto-push failed: ", err)
	}
}

func GitStatus() gin.H {
	c := config.GetConfig()
	if c.GitHistory.Enabled != config.Yes {
		return gin.H{"enabled": false}
	}

	dir := filepath.Dir(config.GetJournalPath())
	repo := resolveActiveRepo(false)
	if repo == nil {
		detection := gitrepo.Detect(dir)
		return gin.H{
			"enabled":               true,
			"repo_detected":         false,
			"needs_setup":           true,
			"parent_repo_available": detection.Found && !detection.IsAtJournalDir,
			"parent_repo_root":      detection.Root,
			"auto_init":             c.GitHistory.AutoInit == config.Yes,
		}
	}

	remote := c.GitHistory.Remote

	var token string
	authConnected := false
	if remote.URL != "" {
		if t, err := gitrepo.LoadToken(config.GetConfigDir(), remote.URL); err == nil {
			token = t
			authConnected = true
		}
	}

	var status gitrepo.RemoteStatus
	var err error
	if token != "" {
		status, err = repo.Status(remote.Name, remote.Branch, gitrepo.HTTPAuth(token))
	} else {
		status, err = repo.Status(remote.Name, remote.Branch, nil)
	}
	if err != nil {
		return gin.H{"enabled": true, "repo_detected": true, "repo_mode": c.GitHistory.RepoMode, "error": err.Error()}
	}

	return gin.H{
		"enabled":         true,
		"repo_detected":   true,
		"repo_mode":       c.GitHistory.RepoMode,
		"repo_root":       repo.Root(),
		"remote_url":      status.RemoteURL,
		"branch":          status.Branch,
		"ahead":           status.Ahead,
		"behind":          status.Behind,
		"last_commit":     status.LastCommit,
		"auth_connected":  authConnected,
		"keyring_backend": gitrepo.ActiveBackend(),
	}
}

// GitInit records the user's explicit choice between reusing an already
// detected ancestor repository ("existing") and creating one scoped just to
// the ledger files ("new"). This is the only way an ambiguous choice (an
// ancestor repository exists but isn't exactly the journal directory) gets
// resolved — nothing is committed until it is.
func GitInit(mode string) gin.H {
	if mode != "existing" && mode != "new" {
		return gin.H{"success": false, "message": "Invalid mode"}
	}

	dir := filepath.Dir(config.GetJournalPath())

	var repo *gitrepo.Repo
	var err error
	if mode == "existing" {
		repo, err = gitrepo.Open(dir)
		if err != nil {
			return gin.H{"success": false, "message": "No existing git repository was found for this directory or any parent directory."}
		}
	} else {
		repo, err = gitrepo.EnsureDedicated(dir)
		if err != nil {
			return gin.H{"success": false, "message": "Failed to create repository: " + err.Error()}
		}
	}

	ensureGitignoreQuiet(repo)

	c := config.GetConfig()
	c.GitHistory.RepoMode = mode
	if err := config.SaveConfigObject(c); err != nil {
		return gin.H{"success": false, "message": "Failed to save configuration: " + err.Error()}
	}

	// If a remote was already connected before local setup was completed
	// (e.g. the user connected GitHub first), attach it to the now-resolved
	// repository.
	if c.GitHistory.Remote.URL != "" {
		if err := repo.SetRemote("origin", c.GitHistory.Remote.URL); err != nil {
			log.Warn("git history: failed to attach configured remote: ", err)
		}
	}

	return gin.H{"success": true, "repo_root": repo.Root()}
}

func GitHistoryForFile(file string) gin.H {
	dir := filepath.Dir(config.GetJournalPath())
	fullPath, err := utils.BuildSubPath(dir, file)
	if err != nil {
		return gin.H{"commits": []gitrepo.CommitInfo{}, "error": "Invalid file name"}
	}

	repo := resolveActiveRepo(false)
	if repo == nil {
		return gin.H{"commits": []gitrepo.CommitInfo{}}
	}

	relPath, err := filepath.Rel(repo.Root(), fullPath)
	if err != nil {
		return gin.H{"commits": []gitrepo.CommitInfo{}}
	}

	commits, err := repo.LogForPath(relPath, 200)
	if err != nil {
		return gin.H{"commits": []gitrepo.CommitInfo{}}
	}

	return gin.H{"commits": commits}
}

func GitContentAt(file string, hash string) gin.H {
	if !commitHashPattern.MatchString(hash) {
		return gin.H{"error": "Invalid commit hash"}
	}

	dir := filepath.Dir(config.GetJournalPath())
	fullPath, err := utils.BuildSubPath(dir, file)
	if err != nil {
		return gin.H{"error": "Invalid file name"}
	}

	repo := resolveActiveRepo(false)
	if repo == nil {
		return gin.H{"error": "Repository not available"}
	}

	relPath, err := filepath.Rel(repo.Root(), fullPath)
	if err != nil {
		return gin.H{"error": "Invalid file name"}
	}

	content, err := repo.ContentAt(hash, relPath)
	if err != nil {
		if err == gitrepo.ErrFileNotFoundAtCommit {
			return gin.H{"error": "This file did not exist at this commit"}
		}
		return gin.H{"error": err.Error()}
	}

	return gin.H{"name": file, "content": content}
}

func GitPush() gin.H {
	c := config.GetConfig()
	remote := c.GitHistory.Remote
	if remote.URL == "" {
		return gin.H{"success": false, "message": "No remote configured"}
	}

	repo := resolveActiveRepo(false)
	if repo == nil {
		return gin.H{"success": false, "message": "Repository not available — finish setting up local history first"}
	}

	token, err := gitrepo.LoadToken(config.GetConfigDir(), remote.URL)
	if err != nil {
		return gin.H{"success": false, "message": "Not connected — please connect an account or token first"}
	}

	if err := repo.Push(remote.Name, remote.Branch, token); err != nil {
		return gin.H{"success": false, "message": "Push failed: " + err.Error()}
	}

	return gin.H{"success": true}
}

func GitOAuthStart(providerStr string) gin.H {
	provider := gitrepo.Provider(providerStr)
	if provider != gitrepo.ProviderGitHub && provider != gitrepo.ProviderGitLab {
		return gin.H{"error": "Unsupported provider"}
	}

	session, sessionID, err := gitrepo.StartDeviceFlow(provider)
	if err != nil {
		return gin.H{"error": err.Error()}
	}

	return gin.H{
		"session_id":       sessionID,
		"user_code":        session.UserCode,
		"verification_uri": session.VerificationURI,
		"interval":         session.Interval,
	}
}

func GitOAuthPoll(sessionID string) gin.H {
	status, err := gitrepo.PollDeviceFlow(sessionID)
	if err != nil {
		return gin.H{"status": "error", "message": err.Error()}
	}

	return gin.H{"status": status}
}

func GitStartTokenSession(providerStr string, token string) gin.H {
	provider := gitrepo.Provider(providerStr)
	if provider != gitrepo.ProviderGitHub && provider != gitrepo.ProviderGitLab {
		return gin.H{"error": "Unsupported provider"}
	}

	if err := gitrepo.ValidateToken(provider, token); err != nil {
		return gin.H{"error": "Could not validate token: " + err.Error()}
	}

	sessionID := gitrepo.PutTokenSession(provider, token)
	return gin.H{"session_id": sessionID}
}

func GitListRepos(sessionID string) gin.H {
	provider, token, ok := gitrepo.GetSessionToken(sessionID)
	if !ok {
		return gin.H{"error": "Session expired, please reconnect"}
	}

	repos, err := gitrepo.ListRepos(provider, token)
	if err != nil {
		return gin.H{"error": err.Error()}
	}

	return gin.H{"repos": repos}
}

func GitSelectRepo(sessionID string, cloneURL string, branch string) gin.H {
	provider, token, ok := gitrepo.GetSessionToken(sessionID)
	if !ok {
		return gin.H{"success": false, "message": "Session expired, please reconnect"}
	}

	if err := gitrepo.StoreToken(config.GetConfigDir(), cloneURL, token); err != nil {
		return gin.H{"success": false, "message": "Failed to store credential: " + err.Error()}
	}

	if err := updateGitRemote(string(provider), cloneURL, branch); err != nil {
		return gin.H{"success": false, "message": err.Error()}
	}

	if err := ensureRemoteConfigured(cloneURL); err != nil {
		log.Warn("git history: failed to configure git remote: ", err)
	}

	gitrepo.ConsumeSession(sessionID)

	return gin.H{"success": true}
}

func GitSaveToken(url string, branch string, token string) gin.H {
	if err := gitrepo.StoreToken(config.GetConfigDir(), url, token); err != nil {
		return gin.H{"success": false, "message": "Failed to store credential: " + err.Error()}
	}

	if err := updateGitRemote("other", url, branch); err != nil {
		return gin.H{"success": false, "message": err.Error()}
	}

	if err := ensureRemoteConfigured(url); err != nil {
		log.Warn("git history: failed to configure git remote: ", err)
	}

	return gin.H{"success": true}
}

func GitDisconnect() gin.H {
	c := config.GetConfig()
	remote := c.GitHistory.Remote
	if remote.URL != "" {
		if err := gitrepo.DeleteToken(config.GetConfigDir(), remote.URL); err != nil {
			log.Warn("git history: failed to delete stored credential: ", err)
		}
	}

	if err := updateGitRemote("", "", ""); err != nil {
		return gin.H{"success": false, "message": err.Error()}
	}

	return gin.H{"success": true}
}

func updateGitRemote(provider string, url string, branch string) error {
	c := config.GetConfig()
	if branch == "" {
		branch = "main"
	}

	c.GitHistory.Remote = config.GitRemote{
		Provider: provider,
		URL:      url,
		Branch:   branch,
		Name:     "origin",
	}

	return config.SaveConfigObject(c)
}

func ensureRemoteConfigured(url string) error {
	repo := resolveActiveRepo(true)
	if repo == nil {
		// Local repository setup hasn't been completed yet (existing vs
		// new choice still pending). The remote metadata is already saved
		// in config at this point, so GitInit attaches it once the local
		// setup choice is made.
		return nil
	}

	return repo.SetRemote("origin", url)
}

func gitReadonlyResponse(c *gin.Context) bool {
	if config.GetConfig().Readonly {
		c.JSON(http.StatusOK, gin.H{"success": true})
		return true
	}
	return false
}
