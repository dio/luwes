package sink

import "net/http"

// handleUI is defined in ui.go -- placeholder here so sink.go compiles standalone.
// The real implementation serves the embedded HTML UI.
func handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(uiHTML) //nolint:errcheck
}
