// Settings → Import: the HTTP surface over the importer the CLI already runs.
//
// Nothing new happens here. The upload is hashed and handed to
// importer.RunTracked, which owns the resume checkpoint and the run row; this
// file owns the two things a browser needs instead of a terminal: it starts the
// run on a context of its own so the request can end, and it answers "how far
// has it got?" by polling (plans/05, "Progress is polled, not streamed").
//
// One import at a time. Two runs would share a library and a checkpoint, which
// is the one way to make an import lose a row, so the second upload is refused
// rather than queued.
package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/importer"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const (
	// maxImportBodyBytes caps an upload. The real export is a couple of
	// megabytes and the whole body is held while the run reads it; a gigabyte
	// is a mistake, not an import (invariant 7).
	maxImportBodyBytes = 64 << 20
)

// importManager owns the one run the process is doing. The library is the
// durable record — a finished run is read back from import_runs — so this only
// has to answer "is something running right now, and how far has it read?".
type importManager struct {
	log *slog.Logger

	mu     sync.Mutex
	active *importJob
}

// importJob is the live half of a run: the cancel a human can ask for, and the
// bytes the reader has consumed, which is the only progress a poll can trust
// before the first checkpoint.
type importJob struct {
	read   *atomic.Int64
	total  int64
	cancel context.CancelFunc

	// started and stop are guarded by importManager.mu. A cancel can arrive
	// between the upload answering and the run opening its import_runs row —
	// the poll already says a run is going by then — so it is remembered here
	// and applied the moment the run is armed, instead of dying with a context
	// that had not started yet.
	started bool
	stop    bool
}

func newImportManager(log *slog.Logger) *importManager {
	return &importManager{log: log}
}

// begin claims the single slot. It returns false when a run is already going.
func (m *importManager) begin(job *importJob) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		return false
	}
	m.active = job
	return true
}

func (m *importManager) done(job *importJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == job {
		m.active = nil
	}
}

// snapshot is what a status poll reports about the live run.
func (m *importManager) snapshot() (read, total int64, running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return 0, 0, false
	}
	return m.active.read.Load(), m.active.total, true
}

func (m *importManager) running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active != nil
}

// armed says the run has opened its row and is about to read, so a cancel from
// here on reaches the importer.
func (m *importManager) armed(job *importJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != job {
		return
	}
	job.started = true
	if job.stop {
		job.cancel()
	}
}

// cancel asks the run to stop. The run stops at the next record, writes its
// checkpoint and closes as cancelled, so the same file resumes it.
func (m *importManager) cancel() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return false
	}
	m.active.stop = true
	if m.active.started {
		m.active.cancel()
	}
	return true
}

// countingReader reports how far into the file the run has read. The import
// reads the file once, front to back, so the bytes consumed are the progress
// bar — and they are honest before the first checkpoint row is written.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	read, err := c.r.Read(p)
	c.n.Add(int64(read))
	return read, err
}

type importRunDTO struct {
	ID         string           `json:"id"`
	State      string           `json:"state"`
	Checkpoint int              `json:"checkpoint"`
	StartedAt  time.Time        `json:"startedAt"`
	FinishedAt *time.Time       `json:"finishedAt"`
	Report     *importer.Report `json:"report,omitempty"`
}

// importStatusResponse is the one body both the start and the status endpoint
// answer, so the frontend decodes a poll and a start the same way.
type importStatusResponse struct {
	Running bool `json:"running"`
	// Resumable: the last run says "running" and nothing in this process is
	// doing it, which is what a restart mid-import leaves behind. Uploading the
	// same file continues it instead of starting over.
	Resumable  bool          `json:"resumable"`
	BytesRead  int64         `json:"bytesRead,omitempty"`
	BytesTotal int64         `json:"bytesTotal,omitempty"`
	Run        *importRunDTO `json:"run"`
}

type importDryRunResponse struct {
	DryRun     bool            `json:"dryRun"`
	BytesTotal int64           `json:"bytesTotal"`
	Report     importer.Report `json:"report"`
}

type importCancelResponse struct {
	Status string `json:"status"`
}

// importStart takes the file and either reports what the run would do (a dry
// run, answered once it has read the file) or starts it in the background.
//
// enrich is off unless it is asked for: the documented choice for a bulk import
// is to land the items usable and let enrichment be a separate, watched run
// (plans/05, "Volume vs the enrichment queue").
func (s *Server) importStart(w http.ResponseWriter, r *http.Request) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, "the library is not open")
		return
	}

	body, err := readImportBody(w, r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "that file is larger than 64 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "the upload could not be read")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "the upload was empty")
		return
	}

	dryRun := queryFlag(r, "dryRun")
	enrich := queryFlag(r, "enrich")

	sum := sha256.Sum256(body)
	fileSHA256 := hex.EncodeToString(sum[:])
	total := int64(len(body))

	if dryRun {
		source, err := importer.NewSource(bytes.NewReader(body))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		report, err := importer.Run(r.Context(), s.library, source, importer.Options{DryRun: true})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "the dry run could not read the file")
			s.log.Warn("import dry run failed", "error", err)
			return
		}
		writeJSON(w, http.StatusOK, importDryRunResponse{DryRun: true, BytesTotal: total, Report: report})
		return
	}

	// The source is built before the slot is claimed, so a file that is not a
	// bookmark export is refused without ever marking the app busy.
	read := &atomic.Int64{}
	source, err := importer.NewSource(&countingReader{r: bytes.NewReader(body), n: read})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// The run outlives the request that started it: the browser is going to
	// poll, and closing the tab must not stop an import.
	ctx, cancel := context.WithCancel(context.Background())
	job := &importJob{cancel: cancel, read: read, total: total}
	if !s.imports.begin(job) {
		cancel()
		writeError(w, http.StatusConflict, "an import is already running; cancel it or wait for it to finish")
		return
	}

	go func() {
		defer s.imports.done(job)
		defer cancel()

		// OnStart is the arm: the run row exists before a cancel can bite.
		opts := importer.Options{
			NoEnrich: !enrich,
			OnStart:  func(bool, int) { s.imports.armed(job) },
		}
		report, tracked, err := importer.RunTracked(ctx, s.library, fileSHA256, source, opts)
		switch {
		case err != nil:
			s.log.Error("import failed", "run", tracked.RunID, "error", err)
		case tracked.Stopped:
			s.log.Info("import stopped", "run", tracked.RunID, "records", tracked.From+report.Total)
		default:
			s.log.Info("import finished",
				"run", tracked.RunID,
				"records", report.Total,
				"imported", report.Imported,
				"merged", report.Merged,
				"failed", report.Failed,
			)
		}
	}()

	writeJSON(w, http.StatusAccepted, importStatusResponse{Running: true, BytesTotal: total})
}

// importStatus is the poll. The durable answer comes from import_runs; the live
// numbers come from the run in this process.
func (s *Server) importStatus(w http.ResponseWriter, r *http.Request) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, "the library is not open")
		return
	}

	response := importStatusResponse{}
	response.BytesRead, response.BytesTotal, response.Running = s.imports.snapshot()

	run, err := s.library.LatestImportRun(r.Context())
	switch {
	case errors.Is(err, store.ErrNotFound):
		// No import has ever run here. An empty answer, not an error: the page
		// asks this before the first upload.
	case err != nil:
		s.log.Warn("the import status is unavailable", "error", err)
		writeError(w, http.StatusInternalServerError, "the import status could not be read")
		return
	default:
		dto := toImportRunDTO(run)
		response.Run = &dto
		response.Resumable = !response.Running && run.State == store.ImportRunning
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) importCancel(w http.ResponseWriter, r *http.Request) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, "the library is not open")
		return
	}
	if !s.imports.cancel() {
		writeError(w, http.StatusConflict, "no import is running")
		return
	}
	writeJSON(w, http.StatusAccepted, importCancelResponse{Status: "cancelling"})
}

func toImportRunDTO(run model.ImportRun) importRunDTO {
	dto := importRunDTO{
		ID:         run.ID,
		State:      run.State,
		Checkpoint: run.Checkpoint,
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,
	}
	// counts_json is the report the run last wrote. A row written by an older
	// build, or a run that never got past its first row, can hold nothing
	// readable; the page then shows the checkpoint and no counts.
	if strings.TrimSpace(run.CountsJSON) != "" && run.CountsJSON != "{}" {
		var report importer.Report
		if err := json.Unmarshal([]byte(run.CountsJSON), &report); err == nil {
			dto.Report = &report
		}
	}
	return dto
}

// readImportBody reads the upload, bounded. The body is the file: no multipart,
// no field name, nothing a browser has to get right for the CLI's format.
func readImportBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.ContentLength > maxImportBodyBytes {
		return nil, &http.MaxBytesError{Limit: maxImportBodyBytes}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBodyBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// queryFlag reads a boolean query parameter. Absent is false, which is the
// conservative default for both: a dry run writes nothing, and enrichment costs
// money.
func queryFlag(r *http.Request, name string) bool {
	switch r.URL.Query().Get(name) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
