package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

var allowedBinaries = map[string]bool{
	"igloo-agent-linux-amd64": true,
}

// GET /downloads/{binary} — serves agent binaries to workspace pods at startup.
// No auth required: pods download the binary before the agent can authenticate.
func (h *Handler) DownloadAgent(w http.ResponseWriter, r *http.Request) {
	binary := chi.URLParam(r, "binary")
	if !allowedBinaries[binary] {
		h.err(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, "downloads/"+binary)
}
