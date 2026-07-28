package server

import (
	"github.com/ananthakumaran/paisa/internal/cache"
	"github.com/ananthakumaran/paisa/internal/config"
	"github.com/ananthakumaran/paisa/internal/model"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type SyncRequest struct {
	Journal    bool `json:"journal"`
	Prices     bool `json:"prices"`
	Portfolios bool `json:"portfolios"`
}

func Sync(db *gorm.DB, request SyncRequest) gin.H {
	cache.Clear()

	if request.Journal {
		if config.GetConfig().GitHistory.Enabled == config.Yes {
			if err := ensureGitCommitted("paisa: auto-commit external changes"); err != nil {
				log.Warn("git history: auto-commit failed: ", err)
			}
		}

		message, err := model.SyncJournal(db)
		if err != nil {
			return gin.H{"success": false, "message": message}
		}
	}

	if request.Prices {
		err := model.SyncCommodities(db)
		if err != nil {
			return gin.H{"success": false, "message": err.Error()}
		}
		err = model.SyncCII(db)
		if err != nil {
			return gin.H{"success": false, "message": err.Error()}
		}
	}

	if request.Portfolios {
		err := model.SyncPortfolios(db)
		if err != nil {
			return gin.H{"success": false, "message": err.Error()}
		}
	}

	return gin.H{"success": true}
}
