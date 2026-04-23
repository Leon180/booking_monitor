package log

import (
	"encoding/json"
	"net/http"
	"strings"
)

// LevelHandler returns an http.Handler that reads or changes the
// logger's level at runtime.
//
//	GET  /admin/loglevel       → {"level":"info"}
//	POST /admin/loglevel ...   → sets the level
//
// The POST body can be either "level=debug" (form encoding) or
// {"level":"debug"} (JSON). Unknown levels return 400.
//
// Mount this on an operator-only listener (we use the pprof port :6060
// gated by ENABLE_PPROF). Never expose it on the public HTTP server —
// flipping the level to debug on a busy host is a cheap way to DoS
// yourself.
func (l *Logger) LevelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]string{"level": l.level.Level().String()})

		case http.MethodPut, http.MethodPost:
			lvlStr, err := readLevelFromRequest(r)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			lvl, err := ParseLevel(lvlStr)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			l.level.SetLevel(lvl)
			writeJSON(w, http.StatusOK, map[string]string{"level": lvl.String()})

		default:
			w.Header().Set("Allow", "GET, POST, PUT")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
}

// readLevelFromRequest pulls the target level from either JSON body
// ({"level":"debug"}), form body (level=debug) or query string
// (?level=debug). First non-empty wins.
func readLevelFromRequest(r *http.Request) (string, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var body struct {
			Level string `json:"level"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", err
		}
		if body.Level != "" {
			return body.Level, nil
		}
	}
	if err := r.ParseForm(); err == nil {
		if v := r.PostForm.Get("level"); v != "" {
			return v, nil
		}
		if v := r.Form.Get("level"); v != "" {
			return v, nil
		}
	}
	return "", errMissingLevel
}

// errMissingLevel is returned when the POST body has no `level` field.
var errMissingLevel = &badRequestError{msg: "missing 'level' in request body or query"}

type badRequestError struct{ msg string }

func (e *badRequestError) Error() string { return e.msg }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
