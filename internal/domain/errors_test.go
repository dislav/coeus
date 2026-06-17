package domain

import (
	"errors"
	"net/http"
	"testing"
)

func TestErrorHTTPStatusMapping(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{ErrNotFound, http.StatusNotFound},
		{ErrSessionExpired, http.StatusGone},
		{ErrDuplicate, http.StatusConflict},
		{ErrValidation, http.StatusBadRequest},
		{ErrUnauthorized, http.StatusUnauthorized},
		{ErrForbidden, http.StatusForbidden},
		{ErrAIUnavailable, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			got := HTTPStatus(tt.err)
			if got != tt.want {
				t.Errorf("HTTPStatus(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrorsIs(t *testing.T) {
	wrapped := errors.Join(ErrNotFound, errors.New("question 123"))
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is should match wrapped sentinel")
	}
}
