package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/taskmcp"
)

// Pictures pasted into a task's description. The description refers to them as
// @image1, @image2...; agents fetch one with the get_task_image tool.

// maxCardImageBytes bounds a stored picture. The tool result travels as one NATS
// message (1 MB limit) in base64, so the picture has to stay well under that; the
// web UI shrinks pasted images to fit.
const maxCardImageBytes = 600 * 1024

var cardImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

type cardImageJSON struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"` // base64
}

func (h *Handler) listCardImages(w http.ResponseWriter, r *http.Request) {
	imgs, err := dbpkg.ListCardImages(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]cardImageJSON, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, cardImageJSON{Name: im.Name, MediaType: im.MediaType, Data: base64.StdEncoding.EncodeToString(im.Data)})
	}
	jsonResp(w, http.StatusOK, out)
}

func (h *Handler) addCardImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := dbpkg.GetKanbanCard(h.db, id); err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	var body struct {
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*maxCardImageBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid or too large body", http.StatusBadRequest)
		return
	}
	if !cardImageTypes[body.MediaType] {
		http.Error(w, "media_type must be image/png, image/jpeg, image/webp or image/gif", http.StatusBadRequest)
		return
	}
	data, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil || len(data) == 0 {
		http.Error(w, "data must be base64", http.StatusBadRequest)
		return
	}
	if len(data) > maxCardImageBytes {
		http.Error(w, fmt.Sprintf("image too large (%d KB, max %d KB)", len(data)/1024, maxCardImageBytes/1024), http.StatusRequestEntityTooLarge)
		return
	}
	im, err := dbpkg.InsertCardImage(h.db, id, body.MediaType, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, cardImageJSON{Name: im.Name, MediaType: im.MediaType, Data: body.Data})
}

func (h *Handler) deleteCardImage(w http.ResponseWriter, r *http.Request) {
	if err := dbpkg.DeleteCardImage(h.db, r.PathValue("id"), r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// taskImage is the get_task_image tool: a picture of a task of the caller's
// project (default: the caller's own), returned as an image result.
func (h *Handler) taskImage(caller dbpkg.Session, taskID, name string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		taskID = caller.CardID
	}
	card, err := dbpkg.GetKanbanCard(h.db, taskID)
	if err != nil || card.ProjectID != caller.ProjectID {
		return "", fmt.Errorf("task %q not found in this project", taskID)
	}
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if name == "" {
		return "", errors.New("name is required, e.g. \"image1\"")
	}
	im, err := dbpkg.GetCardImage(h.db, card.ID, name)
	if errors.Is(err, sql.ErrNoRows) {
		names, _ := dbpkg.ListCardImageNames(h.db, card.ID)
		return "", fmt.Errorf("task %s has no image %q (images: %s)", card.ID, name, orNone(strings.Join(names, ", ")))
	}
	if err != nil {
		return "", err
	}
	return taskmcp.ImageResult(im.MediaType, base64.StdEncoding.EncodeToString(im.Data)), nil
}
