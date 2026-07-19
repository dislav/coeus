// Package importer synchronously imports exam questions from CSV/XLSX
// uploads into the canonical questions table: parse → validate → embed
// (best effort) → per-row upsert (spec §6).
package importer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// embedChunkSize bounds each EmbedBatch call (spec §8).
const embedChunkSize = 100

// QuestionUpserter is the consumer-side narrow port for storage.
type QuestionUpserter interface {
	UpsertFromImport(ctx context.Context, q *domain.Question) (created bool, err error)
}

// BatchEmbedder is the consumer-side narrow port for embeddings. It is
// nil-able: a nil embedder skips the embedding step silently (spec §8).
type BatchEmbedder interface {
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

type Service struct {
	questions QuestionUpserter
	embedder  BatchEmbedder
	maxRows   int
	log       *slog.Logger
}

func New(questions QuestionUpserter, embedder BatchEmbedder, maxRows int, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{questions: questions, embedder: embedder, maxRows: maxRows, log: log}
}

// validRow pairs a validated question with its 1-based file row number.
type validRow struct {
	row int
	q   *domain.Question
}

// Import runs the full synchronous pipeline (spec §6). File-level failures
// (parse errors, row cap) return an error; row-level failures are recorded
// in the Report and never abort the import.
func (s *Service) Import(ctx context.Context, r io.Reader, kind FileKind, userID string) (Report, error) {
	var rows [][]string
	var err error
	switch kind {
	case KindCSV:
		rows, err = parseCSV(r)
	case KindXLSX:
		rows, err = parseXLSX(r)
	default:
		return Report{}, ErrUnsupportedFormat
	}
	if err != nil {
		return Report{}, err
	}
	if len(rows) > s.maxRows {
		return Report{}, ErrTooManyRows
	}

	rep := Report{TotalRows: len(rows), Errors: []RowError{}}

	now := time.Now().UTC()
	valid := make([]validRow, 0, len(rows))
	for i, raw := range rows {
		rowNum := i + 1 // no header row: file row = data row (spec §4.2)
		cols, shapeErr := normalizeRow(raw)
		if shapeErr != nil {
			rep.addRowError(rowNum, shapeErr.Error())
			continue
		}
		q, valErr := buildQuestion(cols, userID, now)
		if valErr != nil {
			rep.addRowError(rowNum, valErr.Error())
			continue
		}
		valid = append(valid, validRow{row: rowNum, q: q})
	}

	s.embedAll(ctx, valid)

	// Per-row upsert, each in its own transaction (atomic per row). A failed
	// row is recorded; later rows proceed — bad rows never block good rows.
	for _, vr := range valid {
		created, err := s.questions.UpsertFromImport(ctx, vr.q)
		if err != nil {
			s.log.Warn("import upsert failed", "row", vr.row, "err", err)
			rep.addRowError(vr.row, fmt.Sprintf("upsert failed: %v", err))
			continue
		}
		if created {
			rep.Created++
		} else {
			rep.Updated++
		}
	}
	return rep, nil
}

// embedAll fetches embeddings in sequential chunks of embedChunkSize. On the
// FIRST failed chunk it logs a warning and skips embedding for all remaining
// chunks (fail-fast, spec §8) — affected rows import with nil embedding and
// the report is unaffected. A nil embedder skips the step entirely.
func (s *Service) embedAll(ctx context.Context, valid []validRow) {
	if s.embedder == nil || len(valid) == 0 {
		return
	}
	for start := 0; start < len(valid); start += embedChunkSize {
		end := start + embedChunkSize
		if end > len(valid) {
			end = len(valid)
		}
		chunk := valid[start:end]
		texts := make([]string, len(chunk))
		for i, vr := range chunk {
			texts[i] = vr.q.Text
		}
		vecs, err := s.embedder.EmbedBatch(ctx, texts)
		if err != nil {
			s.log.Warn("import embedding chunk failed — skipping remaining chunks",
				"chunk_start", start, "err", err)
			return
		}
		if len(vecs) != len(chunk) {
			s.log.Warn("import embedding chunk size mismatch — skipping remaining chunks",
				"chunk_start", start, "got", len(vecs), "want", len(chunk))
			return
		}
		for i, vec := range vecs {
			chunk[i].q.Embedding = vec
		}
	}
}
