package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/notes"
)

// Notes (PROJECT_PLAN.md §4.14): the user's markdown context for tasks.

// SetNotes wires the notes store.
func (s *Server) SetNotes(n *notes.Store) { s.notes = n }

func (s *Server) notesOK(c *gin.Context) bool {
	if s.notes == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "notes are not available")
		return false
	}
	return true
}

// noteSlug is the note's name from the route: gin hands a wildcard back with
// its leading slash, and the name never has one.
func noteSlug(c *gin.Context) string {
	return strings.Trim(c.Param("slug"), "/")
}

func (s *Server) listNotes(c *gin.Context) {
	if !s.notesOK(c) {
		return
	}
	list, err := s.notes.List(c.MustGet(userIDKey).(string))
	if err != nil {
		s.log.Error("list notes failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to list notes")
		return
	}
	c.JSON(http.StatusOK, list)
}

func (s *Server) getNote(c *gin.Context) {
	if !s.notesOK(c) {
		return
	}
	n, err := s.notes.Get(c.MustGet(userIDKey).(string), noteSlug(c))
	if err != nil {
		s.respondNoteError(c, err)
		return
	}
	c.JSON(http.StatusOK, n)
}

type noteBody struct {
	Context notes.Context `json:"context"`
	Body    string        `json:"body"`
}

func (s *Server) putNote(c *gin.Context) {
	if !s.notesOK(c) {
		return
	}
	var body noteBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body (a note is at most 30 KiB)")
		return
	}
	n, err := s.notes.Put(c.MustGet(userIDKey).(string), noteSlug(c), body.Context, body.Body)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	c.JSON(http.StatusOK, n)
}

func (s *Server) deleteNote(c *gin.Context) {
	if !s.notesOK(c) {
		return
	}
	if err := s.notes.Delete(c.MustGet(userIDKey).(string), noteSlug(c)); err != nil {
		s.respondNoteError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) respondNoteError(c *gin.Context, err error) {
	if errors.Is(err, notes.ErrNotFound) {
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	s.log.Error("note request failed", "err", err)
	respondError(c, http.StatusInternalServerError, CodeInternal, "note request failed")
}
