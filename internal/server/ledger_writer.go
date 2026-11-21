package server

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"

	"github.com/ananthakumaran/paisa/internal/config"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Pass-through to ledger-writer's review API (the single writer of the journal).
// Paisa only renders the review UI; categorizing, rule learning and ledger rewrites
// stay in ledger-writer.
//
// Configured by environment, not paisa.yaml: /api/config serves the whole config to
// the browser, so a token stored there would leak.
//
//	LEDGER_WRITER_URL=http://192.168.1.101:8080
//	LEDGER_WRITER_TOKEN=<WRITER_TOKEN>
var ledgerWriterAllowed = regexp.MustCompile(`^(GET /review|POST /review(/[0-9]+)?|GET /accounts)$`)

func registerLedgerWriterProxy(router *gin.Engine) {
	base := os.Getenv("LEDGER_WRITER_URL")
	token := os.Getenv("LEDGER_WRITER_TOKEN")

	var proxy *httputil.ReverseProxy
	if base != "" {
		target, err := url.Parse(base)
		if err != nil || target.Host == "" {
			log.Errorf("invalid LEDGER_WRITER_URL %q, review UI disabled", base)
		} else {
			proxy = httputil.NewSingleHostReverseProxy(target)
			director := proxy.Director
			proxy.Director = func(r *http.Request) {
				director(r)
				r.Host = target.Host
			}
			proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
				log.Errorf("ledger-writer proxy: %v", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(gin.H{"error": "cannot reach ledger-writer at " + base + ": " + err.Error()})
			}
		}
	}

	router.Any("/api/writer/*path", func(c *gin.Context) {
		if proxy == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ledger-writer is not configured (set LEDGER_WRITER_URL and LEDGER_WRITER_TOKEN)"})
			return
		}
		path := c.Param("path")
		if !ledgerWriterAllowed.MatchString(c.Request.Method + " " + path) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not a review endpoint"})
			return
		}
		if c.Request.Method != http.MethodGet && config.GetConfig().Readonly {
			c.JSON(http.StatusForbidden, gin.H{"error": "paisa is in readonly mode"})
			return
		}
		c.Request.URL.Path = path
		c.Request.URL.RawPath = ""
		c.Request.Header.Del("X-Auth") // paisa's own login stays in paisa
		c.Request.Header.Set("Authorization", "Bearer "+token)
		proxy.ServeHTTP(c.Writer, c.Request)
	})
}
