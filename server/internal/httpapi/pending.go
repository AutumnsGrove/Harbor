// The pending window: saves the Worker has accepted but the potato has not
// ingested yet. It is the answer to "I saved it and it vanished" (design.md §5),
// and it is read-only — opening the page never claims anything.
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/clip"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

// pendingJobDTO is deliberately smaller than a job envelope: the card needs a
// title, where it came from, and when. Blob keys stay on the potato.
type pendingJobDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	URL       *string   `json:"url"`
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type pendingResponse struct {
	Jobs []pendingJobDTO `json:"jobs"`
}

func (s *Server) pendingJobs(w http.ResponseWriter, r *http.Request) {
	if s.pending == nil {
		// No Worker configured: an empty list is the honest answer, not an
		// error. A laptop running `make dev` has nothing in flight.
		writeJSON(w, http.StatusOK, pendingResponse{Jobs: []pendingJobDTO{}})
		return
	}

	jobs, err := s.pending.Pending(r.Context())
	if err != nil {
		// A revoked token or a Worker outage is worth showing, but it must not
		// take the library down with it: the home feed still renders.
		s.log.Warn("the pending list is unavailable", "error", err)
		writeError(w, http.StatusBadGateway, "the Worker's pending list is unavailable")
		return
	}

	response := pendingResponse{Jobs: make([]pendingJobDTO, 0, len(jobs))}
	for _, job := range jobs {
		response.Jobs = append(response.Jobs, pendingJobDTO{
			ID:        job.ID,
			Title:     pendingTitle(job),
			URL:       job.Link,
			Source:    job.Source,
			Kind:      job.Kind,
			Status:    job.Status,
			CreatedAt: time.Unix(job.CreatedAt, 0).UTC(),
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// pendingTitle is what a greyed card can say before the potato has read the
// page: the title a door sent, the link, or the first line of the note.
func pendingTitle(job workerclient.Job) string {
	if job.Title != nil && strings.TrimSpace(*job.Title) != "" {
		return strings.TrimSpace(*job.Title)
	}
	if job.Link != nil && strings.TrimSpace(*job.Link) != "" {
		return strings.TrimSpace(*job.Link)
	}
	if job.Note != nil {
		line := strings.TrimSpace(strings.SplitN(*job.Note, "\n", 2)[0])
		if line != "" {
			// A note can be any language, so the cut has to land on a rune: a byte
			// cut turned a Japanese note's last word into U+FFFD (issue #90).
			return clip.Text(line, 80)
		}
	}
	return "A save on its way"
}
