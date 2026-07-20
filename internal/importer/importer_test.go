package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/vlgrigoriev/coeus/internal/domain"
)

// --- fakes ---

type fakeUpserter struct {
	createdByHash map[string]bool
	questions     []*domain.Question
	err           error
}

func newFakeUpserter() *fakeUpserter {
	return &fakeUpserter{createdByHash: map[string]bool{}}
}

func (f *fakeUpserter) UpsertFromImport(_ context.Context, q *domain.Question) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.questions = append(f.questions, q)
	if f.createdByHash[q.TextHash] {
		return false, nil
	}
	f.createdByHash[q.TextHash] = true
	return true, nil
}

type fakeBatchEmbedder struct {
	calls int
	err   error
}

func (f *fakeBatchEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func csvReader(rows ...string) io.Reader {
	return strings.NewReader(strings.Join(rows, "\n") + "\n")
}

// --- tests ---

func TestService_ImportHappyPath(t *testing.T) {
	up := newFakeUpserter()
	emb := &fakeBatchEmbedder{}
	svc := New(up, emb, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		`What is 2+2?;"3;4";4;math;arith`,
		"Explain entropy.;;disorder increases;physics;",
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 2 || rep.Created != 2 || rep.Updated != 0 || rep.Failed != 0 {
		t.Errorf("report = %+v, want total=2 created=2", rep)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("errors = %v, want none", rep.Errors)
	}
	if emb.calls != 1 {
		t.Errorf("embed calls = %d, want 1 (one chunk)", emb.calls)
	}
	for i, q := range up.questions {
		if q.Embedding == nil {
			t.Errorf("question %d has nil embedding, want assigned", i)
		}
	}
}

func TestService_InFileDuplicatesLastWins(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		`What is 2+2?;"3;4";4;first;`,
		`What is 2+2?;"3;4";4;second;`,
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 2 || rep.Created != 1 || rep.Updated != 1 || rep.Failed != 0 {
		t.Errorf("report = %+v, want total=2 created=1 updated=1", rep)
	}
	// Last wins: the second occurrence's content is what was upserted last.
	last := up.questions[len(up.questions)-1]
	if last.Explanation != "second" {
		t.Errorf("last upserted explanation = %q, want %q (last-wins)", last.Explanation, "second")
	}
}

func TestService_RowFailureIsolation(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		`Good one?;"a;b";a;;`,
		"Bad one?;only;a;;", // 1 choice ⇒ row error
		`Good two?;"x;y";y;;`,
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 3 || rep.Created != 2 || rep.Failed != 1 {
		t.Errorf("report = %+v, want total=3 created=2 failed=1", rep)
	}
	if len(rep.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly 1", rep.Errors)
	}
	if rep.Errors[0].Row != 2 {
		t.Errorf("error row = %d, want 2 (1-based)", rep.Errors[0].Row)
	}
	if rep.Errors[0].Message != "multiple_choice requires at least 2 choices" {
		t.Errorf("error message = %q", rep.Errors[0].Message)
	}
}

func TestService_TooManyRows(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 2, quietLog())

	_, err := svc.Import(context.Background(), csvReader(
		`q1?;"a;b";a;;`,
		`q2?;"a;b";a;;`,
		`q3?;"a;b";a;;`,
	), KindCSV, "user-1")
	if err != ErrTooManyRows {
		t.Errorf("err = %v, want ErrTooManyRows", err)
	}
	if len(up.questions) != 0 {
		t.Errorf("upserted %d questions, want 0 (rejected before processing)", len(up.questions))
	}
}

func TestService_NilEmbedderSkipsEmbedding(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog()) // nil embedder

	rep, err := svc.Import(context.Background(), csvReader(`q?;"a;b";a;;`), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.Created != 1 {
		t.Errorf("created = %d, want 1", rep.Created)
	}
	if up.questions[0].Embedding != nil {
		t.Error("embedding assigned despite nil embedder")
	}
}

func TestService_EmbedChunkFailureSkipsRemaining(t *testing.T) {
	up := newFakeUpserter()
	emb := &fakeBatchEmbedder{err: errors.New("embedder down")}
	svc := New(up, emb, 1000, quietLog())

	// 150 rows ⇒ 2 chunks of (100, 50). First chunk fails ⇒ only 1 call.
	rows := make([]string, 150)
	for i := range rows {
		rows[i] = fmt.Sprintf(`Question number %d?;"a;b";a;;`, i)
	}
	rep, err := svc.Import(context.Background(), csvReader(rows...), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if emb.calls != 1 {
		t.Errorf("embed calls = %d, want 1 (fail-fast skips remaining chunks)", emb.calls)
	}
	if rep.Created != 150 || rep.Failed != 0 {
		t.Errorf("report = %+v, want 150 created, 0 failed (embedding is best-effort)", rep)
	}
	for i, q := range up.questions {
		if q.Embedding != nil {
			t.Errorf("question %d got embedding despite chunk failure", i)
		}
	}
}

func TestService_RowErrorCap(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 1000, quietLog())

	// 101 invalid rows (1 choice each) ⇒ Failed=101 but Errors capped at 100.
	rows := make([]string, 101)
	for i := range rows {
		rows[i] = fmt.Sprintf("Bad %d?;only;a;;", i)
	}
	rep, err := svc.Import(context.Background(), csvReader(rows...), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != 101 || rep.Failed != 101 {
		t.Errorf("report = %+v, want total=101 failed=101", rep)
	}
	if len(rep.Errors) != maxImportRowErrors {
		t.Errorf("len(Errors) = %d, want capped at %d", len(rep.Errors), maxImportRowErrors)
	}
}

func TestService_UpsertFailureRecorded(t *testing.T) {
	up := newFakeUpserter()
	up.err = errors.New("db exploded")
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(`q?;"a;b";a;;`), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.Failed != 1 || rep.Created != 0 {
		t.Errorf("report = %+v, want failed=1 created=0", rep)
	}
	if !strings.Contains(rep.Errors[0].Message, "db exploded") {
		t.Errorf("error message = %q, want upsert error surfaced", rep.Errors[0].Message)
	}
}

func TestService_ReportArithmeticAndRowNumbers(t *testing.T) {
	up := newFakeUpserter()
	svc := New(up, nil, 100, quietLog())

	rep, err := svc.Import(context.Background(), csvReader(
		`New question?;"a;b";a;;`,         // created (row 1)
		"Bad row?;solo;a;;",              // failed  (row 2)
		`New question?;"a;b";a;updated;`, // updated (row 3, in-file dup)
	), KindCSV, "user-1")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rep.TotalRows != rep.Created+rep.Updated+rep.Failed {
		t.Errorf("arithmetic broken: %+v", rep)
	}
	if rep.Created != 1 || rep.Updated != 1 || rep.Failed != 1 || rep.TotalRows != 3 {
		t.Errorf("report = %+v", rep)
	}
	if rep.Errors[0].Row != 2 {
		t.Errorf("failed row reported as %d, want 2 (1-based file row)", rep.Errors[0].Row)
	}
}
