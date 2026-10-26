package controller

import (
	"encoding/json"
	"io"
	"net/http"

	"browserd/internal/browser"
	"browserd/internal/types"
)

func (h *SessionController) ImportCookies(w http.ResponseWriter, r *http.Request, runtimeSessionID string) {
	if h.browser == nil {
		types.WriteErr(w, http.StatusServiceUnavailable, "BROWSER_UNAVAILABLE", "browser runtime unavailable; retry when browserd is healthy")
		return
	}
	var req browser.CookieConfig
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 262144))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "supply JSON with a cookies array containing name, value and url")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "supply exactly one JSON object")
		return
	}
	if (len(req.Cookies) == 0 && req.ProfilePath == "") || len(req.Cookies) > 1000 {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "supply 1 to 1000 cookies")
		return
	}
	if !h.touchSession(w, runtimeSessionID) {
		return
	}
	var count int
	var err error
	if req.ProfilePath != "" {
		count, err = h.browser.ImportCookieConfig(runtimeSessionID, req)
	} else {
		count, err = h.browser.ImportCookies(runtimeSessionID, req.Cookies)
	}
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, map[string]int{"importedCount": count})
}

func (h *SessionController) ImportProfileCookies(w http.ResponseWriter, r *http.Request) {
	if h.browser == nil {
		types.WriteErr(w, http.StatusServiceUnavailable, "BROWSER_UNAVAILABLE", "browser runtime unavailable; retry when browserd is healthy")
		return
	}
	var req browser.CookieConfig
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 262144))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "supply profilePath, revision and cookies")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		types.WriteErr(w, http.StatusBadRequest, "INVALID_REQUEST", "supply exactly one JSON object")
		return
	}
	count, err := h.browser.ImportProfileCookies(req)
	if err != nil {
		writeBrowserErr(w, err)
		return
	}
	types.WriteOK(w, http.StatusOK, map[string]int{"updatedSessions": count})
}
