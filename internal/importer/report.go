package importer

// maxImportRowErrors caps the errors carried in a Report; Failed always
// reports the true count (spec §4.2).
const maxImportRowErrors = 100

// RowError is one failed row: 1-based file row number plus a human message.
type RowError struct {
	Row     int
	Message string
}

// Report is the per-file import outcome (spec §4.2). Invariant:
// TotalRows = Created + Updated + Failed (each in-file duplicate occurrence
// counts individually).
type Report struct {
	TotalRows int
	Created   int
	Updated   int
	Failed    int
	Errors    []RowError
}

// addRowError records a failed row, honoring the maxImportRowErrors cap.
func (r *Report) addRowError(row int, msg string) {
	r.Failed++
	if len(r.Errors) < maxImportRowErrors {
		r.Errors = append(r.Errors, RowError{Row: row, Message: msg})
	}
}
