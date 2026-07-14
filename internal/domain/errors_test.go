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

func TestHTTPStatus_NewConflictCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"self_forbidden sentinel", ErrSelfForbidden, 409},
		{"last_admin sentinel", ErrLastAdmin, 409},
		{"question_in_use dynamic", NewError("question_in_use", "linked to 3 session(s)"), 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HTTPStatus(tc.err); got != tc.want {
				t.Errorf("HTTPStatus(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

func TestNewErrorSentinelMessages(t *testing.T) {
	if ErrSelfForbidden.Code != "self_forbidden" {
		t.Errorf("ErrSelfForbidden.Code = %q", ErrSelfForbidden.Code)
	}
	if ErrLastAdmin.Code != "last_admin" {
		t.Errorf("ErrLastAdmin.Code = %q", ErrLastAdmin.Code)
	}
}

func TestErrorsIs(t *testing.T) {
	wrapped := errors.Join(ErrNotFound, errors.New("question 123"))
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is should match wrapped sentinel")
	}
}
