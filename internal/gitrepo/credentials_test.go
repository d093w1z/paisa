package gitrepo

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCredentials_FileFallback_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	backend := newFileBackend(dir)

	err := backend.Set("https://github.com/user/repo.git", "sekret-token")
	assert.NoError(t, err)

	token, err := backend.Get("https://github.com/user/repo.git")
	assert.NoError(t, err)
	assert.Equal(t, "sekret-token", token)

	raw, err := os.ReadFile(filepath.Join(dir, "git-credentials.enc"))
	assert.NoError(t, err)
	assert.False(t, strings.Contains(string(raw), "sekret-token"), "token must not appear in plaintext on disk")

	if runtime.GOOS != "windows" {
		keyInfo, err := os.Stat(filepath.Join(dir, ".git-credentials-key"))
		assert.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), keyInfo.Mode().Perm())

		storeInfo, err := os.Stat(filepath.Join(dir, "git-credentials.enc"))
		assert.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), storeInfo.Mode().Perm())
	}
}

func TestCredentials_Delete(t *testing.T) {
	dir := t.TempDir()
	backend := newFileBackend(dir)

	assert.NoError(t, backend.Set("remote-key", "token"))

	_, err := backend.Get("remote-key")
	assert.NoError(t, err)

	assert.NoError(t, backend.Delete("remote-key"))

	_, err = backend.Get("remote-key")
	assert.ErrorIs(t, err, ErrTokenNotFound)
}

func TestCredentials_GetMissing(t *testing.T) {
	dir := t.TempDir()
	backend := newFileBackend(dir)

	_, err := backend.Get("does-not-exist")
	assert.ErrorIs(t, err, ErrTokenNotFound)
}
