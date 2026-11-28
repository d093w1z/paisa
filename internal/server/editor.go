package server

import (
	"path/filepath"
	"sort"
	"time"

	"os"

	"github.com/ananthakumaran/paisa/internal/config"
	"github.com/ananthakumaran/paisa/internal/ledger"
	"github.com/ananthakumaran/paisa/internal/model/posting"
	"github.com/ananthakumaran/paisa/internal/utils"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type LedgerFile struct {
	Name       string   `json:"name"`
	Content    string   `json:"content"`
	Versions   []string `json:"versions"`
	Operation  string   `json:"operation"`
	GitEnabled bool     `json:"git_enabled"`
	// Message is an optional custom git commit message for this save. Only
	// meaningful when git history is enabled; ignored otherwise. When
	// empty, a default "paisa: update <file>" message is used.
	Message string `json:"message"`
}

func GetFiles(db *gorm.DB) gin.H {
	var accounts []string
	var payees []string
	var commodities []string
	db.Model(&posting.Posting{}).Distinct().Pluck("Account", &accounts)
	db.Model(&posting.Posting{}).Distinct().Pluck("Payee", &payees)
	db.Model(&posting.Posting{}).Distinct().Pluck("Commodity", &commodities)

	path := config.GetJournalPath()

	files := []*LedgerFile{}
	dir := filepath.Dir(path)
	paths, _ := doublestar.FilepathGlob(dir + "/**/*" + filepath.Ext(path))

	gitEnabled := isGitHistoryAvailable()

	for _, path = range paths {
		files = append(files, readLedgerFileWithVersions(dir, path, gitEnabled))
	}

	return gin.H{"files": files, "accounts": accounts, "payees": payees, "commodities": commodities}
}

func GetFile(file LedgerFile) gin.H {
	path := config.GetJournalPath()
	dir := filepath.Dir(path)
	filePath, err := utils.BuildSubPath(dir, file.Name)
	if err != nil {
		return gin.H{"message": "Invalid file name"}
	}
	return gin.H{"file": readLedgerFile(dir, filePath, isGitHistoryAvailable())}
}

func DeleteBackups(file LedgerFile) gin.H {
	path := config.GetJournalPath()
	dir := filepath.Dir(path)
	filePath, err := utils.BuildSubPath(dir, file.Name)
	if err != nil {
		return gin.H{"message": "Invalid file name"}
	}

	if !config.GetConfig().Readonly {
		versions := utils.Glob(filePath, ".backup.*")
		for _, version := range versions {
			err := os.Remove(version)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	return gin.H{"file": readLedgerFileWithVersions(dir, filePath, isGitHistoryAvailable())}
}

// isGitHistoryAvailable reports whether git-based history is enabled and a
// repository is currently reachable for the journal directory, so the
// editor UI knows whether to show the legacy backup-file version list or
// point the user at the git history page instead.
func isGitHistoryAvailable() bool {
	return resolveActiveRepo(false) != nil
}

func SaveFile(db *gorm.DB, file LedgerFile) gin.H {
	path := config.GetJournalPath()
	if extension := filepath.Ext(path); extension != "" && filepath.Ext(file.Name) != extension {
		return gin.H{"errors": []ledger.LedgerFileError{}, "saved": false, "message": "File name must end with " + extension}
	}
	errors, _, err := validateFile(file)
	if err != nil {
		return gin.H{"errors": errors, "saved": false, "message": "Validation failed"}
	}

	dir := filepath.Dir(path)

	filePath, err := utils.BuildSubPath(dir, file.Name)
	if err != nil {
		log.Warn(err)
		return gin.H{"errors": errors, "saved": false, "message": "Invalid file name"}
	}

	backupPath := filePath + ".backup." + time.Now().Format("2006-01-02-15-04-05.000")

	err = os.MkdirAll(filepath.Dir(filePath), 0700)
	if err != nil {
		log.Warn(err)
		return gin.H{"errors": errors, "saved": false, "message": "Failed to create directory"}
	}

	fileStat, err := os.Stat(filePath)
	if err != nil && file.Operation != "overwrite" && file.Operation != "create" {
		log.Warn(err)
		return gin.H{"errors": errors, "saved": false, "message": "File does not exist"}
	}

	// When git history is enabled and a repo is available, git commits
	// supersede the filesystem backup mechanism; fall back to writing a
	// backup file whenever git isn't usable this run, so history is never
	// silently lost.
	gitRepo := openGitRepo()

	var perm os.FileMode = 0644
	if err == nil {
		if file.Operation == "create" {
			return gin.H{"errors": errors, "saved": false, "message": "File already exists"}
		}

		perm = fileStat.Mode().Perm()

		if gitRepo == nil {
			existingContent, err := os.ReadFile(filePath)
			if err != nil {
				log.Warn(err)
				return gin.H{"errors": errors, "saved": false, "message": "Failed to read file"}
			}

			err = os.WriteFile(backupPath, existingContent, perm)
			if err != nil {
				log.Warn(err)
				return gin.H{"errors": errors, "saved": false, "message": "Failed to create backup"}
			}
		}
	}

	err = os.WriteFile(filePath, []byte(file.Content), perm)
	if err != nil {
		log.Warn(err)
		return gin.H{"errors": errors, "saved": false, "message": "Failed to write file"}
	}

	if gitRepo != nil {
		commitMessage := file.Message
		if commitMessage == "" {
			commitMessage = "paisa: update " + file.Name
		}

		_, changed, commitErr := gitRepo.StageAndCommitAll(dir, commitMessage, gitAuthorFromConfig())
		if commitErr != nil {
			log.Warn("git history: commit failed: ", commitErr)
		} else if changed && config.GetConfig().GitHistory.AutoPush == config.Yes {
			go attemptAutoPush(gitRepo)
		}
	}

	Sync(db, SyncRequest{Journal: true})

	return gin.H{"errors": errors, "saved": true, "file": readLedgerFileWithVersions(dir, filePath, gitRepo != nil)}
}

func ValidateFile(file LedgerFile) gin.H {
	errors, output, _ := validateFile(file)
	return gin.H{"errors": errors, "output": output}
}

func validateFile(file LedgerFile) ([]ledger.LedgerFileError, string, error) {
	path := config.GetJournalPath()
	if file.Name != "" {
		filePath, err := utils.BuildSubPath(filepath.Dir(path), file.Name)
		if err != nil || filePath == filepath.Clean(filepath.Dir(path)) {
			return []ledger.LedgerFileError{{LineFrom: 1, LineTo: 1, Message: "Invalid file name"}}, "", os.ErrInvalid
		}
		path = filePath
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return []ledger.LedgerFileError{{LineFrom: 1, LineTo: 1, Message: err.Error()}}, "", err
	}

	tmpfile, err := os.CreateTemp(filepath.Dir(path), "paisa-tmp-")
	if err != nil {
		return []ledger.LedgerFileError{{LineFrom: 1, LineTo: 1, Message: err.Error()}}, "", err
	}

	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(file.Content)); err != nil {
		log.Fatal(err)
	}

	if err := tmpfile.Close(); err != nil {
		log.Fatal(err)
	}

	return ledger.Cli().ValidateFile(tmpfile.Name())
}

func readLedgerFile(dir string, path string, gitEnabled bool) *LedgerFile {
	content, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}

	name, err := filepath.Rel(dir, path)

	return &LedgerFile{
		Name:       name,
		Content:    string(content),
		GitEnabled: gitEnabled,
	}
}

func readLedgerFileWithVersions(dir string, path string, gitEnabled bool) *LedgerFile {
	content, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}

	versions := utils.Glob(path, ".backup.*")
	versionPaths := lo.Map(versions, func(path string, _ int) string {
		name, err := filepath.Rel(dir, path)
		if err != nil {
			log.Fatal(err)
		}

		return name
	})
	sort.Sort(sort.Reverse(sort.StringSlice(versionPaths)))

	name, err := filepath.Rel(dir, path)
	if err != nil {
		log.Fatal(err)
	}

	return &LedgerFile{
		Name:       name,
		Content:    string(content),
		Versions:   versionPaths,
		GitEnabled: gitEnabled,
	}
}
