package domain

import "testing"

func TestJobStatusConstants(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want string
	}{
		{"pending", JobStatusPending, "pending"},
		{"processing", JobStatusProcessing, "processing"},
		{"done", JobStatusDone, "done"},
		{"failed", JobStatusFailed, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.val != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.val, tt.want)
			}
		})
	}
}

func TestSessionStatusConstants(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want string
	}{
		{"open", SessionStatusOpen, "open"},
		{"closed", SessionStatusClosed, "closed"},
		{"expired", SessionStatusExpired, "expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.val != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.val, tt.want)
			}
		})
	}
}
