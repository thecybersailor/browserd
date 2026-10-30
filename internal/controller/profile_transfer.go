package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"browserd/internal/browser"
	"browserd/internal/types"
)

func (h *SessionController) TransferProfile(w http.ResponseWriter, r *http.Request, importing bool) {
	var in browser.ProfileTransferInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		types.WriteErr(w, 400, "INVALID_PROFILE", "supply a valid profile transfer request, at most 32 MiB")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		types.WriteErr(w, 400, "INVALID_PROFILE", "supply exactly one JSON object")
		return
	}
	svc, ok := h.browser.(interface {
		TransferProfile(context.Context, browser.ProfileTransferInput, bool) (any, error)
	})
	if !ok {
		types.WriteErr(w, 503, "PROFILE_TRANSFER_UNAVAILABLE", "deploy browserd with profile transfer support")
		return
	}
	out, err := svc.TransferProfile(r.Context(), in, importing)
	if err != nil {
		types.WriteErr(w, http.StatusBadRequest, "PROFILE_TRANSFER_FAILED", err.Error())
		return
	}
	types.WriteOK(w, 200, out)
}
