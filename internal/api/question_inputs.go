package api

import "net/http"

func (s *Server) questionInputs(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	value, err := s.app.QuestionInputs(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, notFound(err))
		return
	}
	writeJSON(w, http.StatusOK, value)
}
