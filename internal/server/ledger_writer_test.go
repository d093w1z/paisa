package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLedgerWriterProxy(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	t.Setenv("LEDGER_WRITER_URL", upstream.URL)
	t.Setenv("LEDGER_WRITER_TOKEN", "s3cret")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerLedgerWriterProxy(router)

	paisa := httptest.NewServer(router) // real server: gin's CloseNotify panics on a ResponseRecorder
	defer paisa.Close()
	do := func(method, path string) *http.Response {
		got = nil
		req, err := http.NewRequest(method, paisa.URL+path, strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Header.Set("X-Auth", "user:hash")
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		res.Body.Close()
		return res
	}

	require.Equal(t, 200, do("GET", "/api/writer/review?group=payee").StatusCode)
	require.Equal(t, "/review", got.URL.Path)
	require.Equal(t, "group=payee", got.URL.RawQuery)
	require.Equal(t, "Bearer s3cret", got.Header.Get("Authorization"))
	require.Empty(t, got.Header.Get("X-Auth"))

	require.Equal(t, 200, do("POST", "/api/writer/review").StatusCode)
	require.Equal(t, 200, do("POST", "/api/writer/review/42").StatusCode)
	require.Equal(t, 200, do("GET", "/api/writer/accounts").StatusCode)

	for _, c := range [][2]string{{"POST", "/api/writer/txn"}, {"POST", "/api/writer/statement"},
		{"GET", "/api/writer/health"}, {"DELETE", "/api/writer/review/1"}, {"POST", "/api/writer/review/1/../../txn"}} {
		require.Equal(t, 404, do(c[0], c[1]).StatusCode, c)
		require.Nil(t, got, c)
	}
}

func TestLedgerWriterProxyUnconfigured(t *testing.T) {
	t.Setenv("LEDGER_WRITER_URL", "")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerLedgerWriterProxy(router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/writer/review", nil))
	require.Equal(t, 503, w.Code)
}

func TestLedgerWriterProxyDown(t *testing.T) {
	t.Setenv("LEDGER_WRITER_URL", "http://127.0.0.1:1")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerLedgerWriterProxy(router)
	paisa := httptest.NewServer(router)
	defer paisa.Close()
	res, err := http.Get(paisa.URL + "/api/writer/review")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 502, res.StatusCode)
	var body map[string]string
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	require.Contains(t, body["error"], "cannot reach ledger-writer")
}
