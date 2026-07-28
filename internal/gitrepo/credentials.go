package gitrepo

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/zalando/go-keyring"
)

var ErrTokenNotFound = errors.New("token not found")

const keyringService = "paisa-git-history"

// credentialBackend abstracts the storage mechanism so the file-based
// fallback can be unit tested without a real OS keyring/Secret Service.
type credentialBackend interface {
	Set(key, token string) error
	Get(key string) (string, error)
	Delete(key string) error
}

var (
	backendMu      sync.Mutex
	activeBackend  = "os"
	fallbackWarned = false
)

type osKeyringBackend struct{}

func (osKeyringBackend) Set(key, token string) error {
	return keyring.Set(keyringService, key, token)
}

func (osKeyringBackend) Get(key string) (string, error) {
	token, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrTokenNotFound
	}
	return token, err
}

func (osKeyringBackend) Delete(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

type fileBackend struct {
	dir string
}

func newFileBackend(dir string) *fileBackend {
	return &fileBackend{dir: dir}
}

func (b *fileBackend) keyPath() string {
	return filepath.Join(b.dir, ".git-credentials-key")
}

func (b *fileBackend) storePath() string {
	return filepath.Join(b.dir, "git-credentials.enc")
}

func (b *fileBackend) loadOrCreateKey() ([]byte, error) {
	key, err := os.ReadFile(b.keyPath())
	if err == nil && len(key) == 32 {
		return key, nil
	}

	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}

	if err := os.WriteFile(b.keyPath(), key, 0600); err != nil {
		return nil, err
	}

	return key, nil
}

func (b *fileBackend) readStore() (map[string]string, error) {
	store := map[string]string{}

	content, err := os.ReadFile(b.storePath())
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(content, &store); err != nil {
		return nil, err
	}

	return store, nil
}

func (b *fileBackend) writeStore(store map[string]string) error {
	content, err := json.Marshal(store)
	if err != nil {
		return err
	}

	return os.WriteFile(b.storePath(), content, 0600)
}

func (b *fileBackend) Set(key, token string) error {
	gcmKey, err := b.loadOrCreateKey()
	if err != nil {
		return err
	}

	block, err := aes.NewCipher(gcmKey)
	if err != nil {
		return err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(token), nil)

	store, err := b.readStore()
	if err != nil {
		return err
	}
	store[key] = base64.StdEncoding.EncodeToString(ciphertext)

	return b.writeStore(store)
}

func (b *fileBackend) Get(key string) (string, error) {
	store, err := b.readStore()
	if err != nil {
		return "", err
	}

	encoded, ok := store[key]
	if !ok {
		return "", ErrTokenNotFound
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}

	gcmKey, err := b.loadOrCreateKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(gcmKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("malformed credential entry")
	}

	nonce, data := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

func (b *fileBackend) Delete(key string) error {
	store, err := b.readStore()
	if err != nil {
		return err
	}

	delete(store, key)

	return b.writeStore(store)
}

// backendFor returns the OS keyring backend, falling back to the encrypted
// file backend (rooted at dir) when the OS backend is unusable (e.g. no
// Secret Service running, common on headless Linux deployments). "Not found"
// is not treated as a fallback trigger, it's the normal outcome for a key
// that's genuinely absent.
func backendFor(dir string, probe func(credentialBackend) error) credentialBackend {
	backendMu.Lock()
	defer backendMu.Unlock()

	primary := osKeyringBackend{}
	if err := probe(primary); err == nil || errors.Is(err, ErrTokenNotFound) {
		activeBackend = "os"
		return primary
	}

	if !fallbackWarned {
		log.Warn("git history: OS keychain unavailable, falling back to encrypted file storage in ", dir, " -- for stronger protection, install a Secret Service provider (e.g. gnome-keyring, kwallet) on this system")
		fallbackWarned = true
	}
	activeBackend = "file"
	return newFileBackend(dir)
}

// ActiveBackend reports which credential backend was used by the most
// recent Store/Load/Delete call: "os" or "file".
func ActiveBackend() string {
	backendMu.Lock()
	defer backendMu.Unlock()
	return activeBackend
}

func StoreToken(dir string, remoteKey string, token string) error {
	backend := backendFor(dir, func(b credentialBackend) error {
		_, err := b.Get(remoteKey)
		if errors.Is(err, ErrTokenNotFound) {
			return nil
		}
		return err
	})
	return backend.Set(remoteKey, token)
}

func LoadToken(dir string, remoteKey string) (string, error) {
	backend := backendFor(dir, func(b credentialBackend) error {
		_, err := b.Get(remoteKey)
		return err
	})
	return backend.Get(remoteKey)
}

func DeleteToken(dir string, remoteKey string) error {
	backend := backendFor(dir, func(b credentialBackend) error {
		_, err := b.Get(remoteKey)
		if errors.Is(err, ErrTokenNotFound) {
			return nil
		}
		return err
	})
	return backend.Delete(remoteKey)
}
