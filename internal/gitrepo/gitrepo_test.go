package gitrepo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
)

func testAuthor() GitAuthor {
	return GitAuthor{Name: "Test", Email: "test@example.com"}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	assert.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func TestInitAndOpen(t *testing.T) {
	dir := t.TempDir()

	_, err := Init(dir)
	assert.NoError(t, err)
	assert.DirExists(t, filepath.Join(dir, ".git"))

	repo, err := Open(dir)
	assert.NoError(t, err)
	assert.Equal(t, dir, repo.Root())
}

func TestStageAndCommitAll_NoChanges(t *testing.T) {
	dir := t.TempDir()
	repo, err := Init(dir)
	assert.NoError(t, err)

	hash, changed, err := repo.StageAndCommitAll(dir, "empty commit attempt", testAuthor())
	assert.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, hash)

	_, err = repo.repo.Head()
	assert.Error(t, err, "no commit should have been created")
}

func TestStageAndCommitAll_WithChanges(t *testing.T) {
	dir := t.TempDir()
	repo, err := Init(dir)
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "main.ledger"), "2023/01/01 Test\n")

	hash, changed, err := repo.StageAndCommitAll(dir, "paisa: update main.ledger", testAuthor())
	assert.NoError(t, err)
	assert.True(t, changed)
	assert.NotEmpty(t, hash)

	commits, err := repo.LogForPath("main.ledger", 10)
	assert.NoError(t, err)
	assert.Len(t, commits, 1)
	assert.Equal(t, hash, commits[0].Hash)
	assert.Equal(t, "paisa: update main.ledger", commits[0].Message)
	assert.Equal(t, "Test", commits[0].Author)
}

func TestLogForPath_MultipleCommits(t *testing.T) {
	dir := t.TempDir()
	repo, err := Init(dir)
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "a.ledger"), "a v1\n")
	_, _, err = repo.StageAndCommitAll(dir, "add a", testAuthor())
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "b.ledger"), "b v1\n")
	_, _, err = repo.StageAndCommitAll(dir, "add b", testAuthor())
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "a.ledger"), "a v2\n")
	_, _, err = repo.StageAndCommitAll(dir, "update a", testAuthor())
	assert.NoError(t, err)

	commitsA, err := repo.LogForPath("a.ledger", 10)
	assert.NoError(t, err)
	assert.Len(t, commitsA, 2)
	assert.Equal(t, "update a", commitsA[0].Message)
	assert.Equal(t, "add a", commitsA[1].Message)

	commitsB, err := repo.LogForPath("b.ledger", 10)
	assert.NoError(t, err)
	assert.Len(t, commitsB, 1)
	assert.Equal(t, "add b", commitsB[0].Message)
}

func TestContentAt(t *testing.T) {
	dir := t.TempDir()
	repo, err := Init(dir)
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "main.ledger"), "v1\n")
	hashV1, _, err := repo.StageAndCommitAll(dir, "v1", testAuthor())
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "main.ledger"), "v2\n")
	hashV2, _, err := repo.StageAndCommitAll(dir, "v2", testAuthor())
	assert.NoError(t, err)

	contentV1, err := repo.ContentAt(hashV1, "main.ledger")
	assert.NoError(t, err)
	assert.Equal(t, "v1\n", contentV1)

	contentV2, err := repo.ContentAt(hashV2, "main.ledger")
	assert.NoError(t, err)
	assert.Equal(t, "v2\n", contentV2)
}

func TestContentAt_FileNotYetAdded(t *testing.T) {
	dir := t.TempDir()
	repo, err := Init(dir)
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "a.ledger"), "a\n")
	hashBeforeB, _, err := repo.StageAndCommitAll(dir, "add a", testAuthor())
	assert.NoError(t, err)

	_, err = repo.ContentAt(hashBeforeB, "b.ledger")
	assert.ErrorIs(t, err, ErrFileNotFoundAtCommit)
}

func TestEnsureGitignore_Idempotent(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir)
	assert.NoError(t, err)

	changed, err := EnsureGitignore(dir)
	assert.NoError(t, err)
	assert.True(t, changed)

	content1, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NoError(t, err)

	changed, err = EnsureGitignore(dir)
	assert.NoError(t, err)
	assert.False(t, changed)

	content2, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	assert.NoError(t, err)
	assert.Equal(t, string(content1), string(content2))
}

func TestDetectExistingRepo(t *testing.T) {
	parent := t.TempDir()
	_, err := Init(parent)
	assert.NoError(t, err)

	journalDir := filepath.Join(parent, "finance")
	assert.NoError(t, os.MkdirAll(journalDir, 0755))

	repo, err := Open(journalDir)
	assert.NoError(t, err)
	assert.Equal(t, parent, repo.Root())
}

func TestStageAndCommitAll_ScopedToJournalSubtree(t *testing.T) {
	parent := t.TempDir()
	repo, err := Init(parent)
	assert.NoError(t, err)

	journalDir := filepath.Join(parent, "finance")
	assert.NoError(t, os.MkdirAll(journalDir, 0755))

	writeFile(t, filepath.Join(parent, "unrelated.txt"), "unrelated\n")
	writeFile(t, filepath.Join(journalDir, "main.ledger"), "2023/01/01 Test\n")

	hash, changed, err := repo.StageAndCommitAll(journalDir, "paisa: scoped commit", testAuthor())
	assert.NoError(t, err)
	assert.True(t, changed)

	// The committed tree should contain only the in-scope file, proving the
	// unrelated change outside the journal subtree was left untouched.
	commitObj, err := repo.repo.CommitObject(plumbing.NewHash(hash))
	assert.NoError(t, err)
	tree, err := commitObj.Tree()
	assert.NoError(t, err)

	committedFiles := []string{}
	assert.NoError(t, tree.Files().ForEach(func(f *object.File) error {
		committedFiles = append(committedFiles, f.Name)
		return nil
	}))
	assert.Equal(t, []string{"finance/main.ledger"}, committedFiles)

	// The unrelated file should still be untracked in the working tree.
	status, err := repo.worktree.Status()
	assert.NoError(t, err)
	unrelatedStatus := status.File("unrelated.txt")
	assert.Equal(t, git.Untracked, unrelatedStatus.Worktree)
}

func TestOpenAt_ExactDirOnly(t *testing.T) {
	parent := t.TempDir()
	_, err := Init(parent)
	assert.NoError(t, err)

	journalDir := filepath.Join(parent, "finance")
	assert.NoError(t, os.MkdirAll(journalDir, 0755))

	// A parent repo exists, but OpenAt must not walk up to find it.
	_, err = OpenAt(journalDir)
	assert.ErrorIs(t, err, git.ErrRepositoryNotExists)

	// Once a repo exists exactly at journalDir, OpenAt finds it.
	_, err = Init(journalDir)
	assert.NoError(t, err)

	repo, err := OpenAt(journalDir)
	assert.NoError(t, err)
	assert.Equal(t, journalDir, repo.Root())
}

func TestEnsureDedicated_CreatesWhenAbsent(t *testing.T) {
	parent := t.TempDir()
	_, err := Init(parent)
	assert.NoError(t, err)

	journalDir := filepath.Join(parent, "finance")
	assert.NoError(t, os.MkdirAll(journalDir, 0755))

	repo, err := EnsureDedicated(journalDir)
	assert.NoError(t, err)
	assert.Equal(t, journalDir, repo.Root())
	assert.DirExists(t, filepath.Join(journalDir, ".git"))
}

func TestEnsureDedicated_ReusesExistingDedicatedRepo(t *testing.T) {
	dir := t.TempDir()
	created, err := Init(dir)
	assert.NoError(t, err)

	writeFile(t, filepath.Join(dir, "main.ledger"), "v1\n")
	hash, _, err := created.StageAndCommitAll(dir, "v1", testAuthor())
	assert.NoError(t, err)

	repo, err := EnsureDedicated(dir)
	assert.NoError(t, err)
	assert.Equal(t, dir, repo.Root())

	commits, err := repo.LogForPath("main.ledger", 10)
	assert.NoError(t, err)
	assert.Len(t, commits, 1)
	assert.Equal(t, hash, commits[0].Hash)
}

func TestDetect_NothingFound(t *testing.T) {
	dir := t.TempDir()
	detection := Detect(dir)
	assert.False(t, detection.Found)
}

func TestDetect_FoundAtJournalDir(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(dir)
	assert.NoError(t, err)

	detection := Detect(dir)
	assert.True(t, detection.Found)
	assert.True(t, detection.IsAtJournalDir)
	assert.Equal(t, dir, detection.Root)
}

func TestDetect_FoundAtAncestor(t *testing.T) {
	parent := t.TempDir()
	_, err := Init(parent)
	assert.NoError(t, err)

	journalDir := filepath.Join(parent, "finance")
	assert.NoError(t, os.MkdirAll(journalDir, 0755))

	detection := Detect(journalDir)
	assert.True(t, detection.Found)
	assert.False(t, detection.IsAtJournalDir)
	assert.Equal(t, parent, detection.Root)
}
