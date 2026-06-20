//go:build whatsapp_native

package whatsapp

import (
	"errors"
	"fmt"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestParseServerErrorCode(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want int
	}{
		{"plain 463", "server returned error 463", 463},
		{"wrapped 463", "whatsapp send: server returned error 463: temporary", 463},
		{"code with trailing text", "server returned error 500 something", 500},
		{"no code", "server returned error", 0},
		{"unrelated", "some other error", 0},
		{"non-numeric tail", "server returned error abc", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseServerErrorCode(tt.msg); got != tt.want {
				t.Fatalf("parseServerErrorCode(%q) = %d, want %d", tt.msg, got, tt.want)
			}
		})
	}
}

func TestWhatsAppErrorCategory(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, ""},
		{400, "bad_request"},
		{401, "forbidden"},
		{403, "forbidden"},
		{404, "not_found"},
		{429, "rate_limited"},
		{463, "addressing"},
		{500, "server_error"},
		{503, "server_error"},
		{999, "other"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("code_%d", tt.code), func(t *testing.T) {
			if got := whatsAppErrorCategory(tt.code); got != tt.want {
				t.Fatalf("whatsAppErrorCategory(%d) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestClassifyWhatsAppSendError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantClass    string
		wantCode     int
		wantCategory string
	}{
		{
			name:      "nil error",
			err:       nil,
			wantClass: "",
		},
		{
			name:         "server returned 463",
			err:          fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 463),
			wantClass:    "server_returned_error",
			wantCode:     463,
			wantCategory: "addressing",
		},
		{
			name:         "server returned 463 wrapped",
			err:          fmt.Errorf("whatsapp send: %w", fmt.Errorf("%w %d", whatsmeow.ErrServerReturnedError, 463)),
			wantClass:    "server_returned_error",
			wantCode:     463,
			wantCategory: "addressing",
		},
		{
			name:         "iq error with code",
			err:          &whatsmeow.IQError{Code: 429, Text: "rate-overlimit"},
			wantClass:    "iq_error",
			wantCode:     429,
			wantCategory: "rate_limited",
		},
		{
			name:      "message timed out",
			err:       whatsmeow.ErrMessageTimedOut,
			wantClass: "timeout",
		},
		{
			name:      "not connected",
			err:       whatsmeow.ErrNotConnected,
			wantClass: "not_connected",
		},
		{
			name:      "unknown error",
			err:       errors.New("some random failure"),
			wantClass: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyWhatsAppSendError(tt.err)
			if got.Class != tt.wantClass {
				t.Errorf("Class = %q, want %q", got.Class, tt.wantClass)
			}
			if got.Code != tt.wantCode {
				t.Errorf("Code = %d, want %d", got.Code, tt.wantCode)
			}
			if got.Category != tt.wantCategory {
				t.Errorf("Category = %q, want %q", got.Category, tt.wantCategory)
			}
		})
	}
}

func TestSendErrorDiagnostic_CategoryOrUnknown(t *testing.T) {
	if got := (sendErrorDiagnostic{Category: "addressing"}).categoryOrUnknown(); got != "addressing" {
		t.Fatalf("categoryOrUnknown() = %q, want addressing", got)
	}
	if got := (sendErrorDiagnostic{}).categoryOrUnknown(); got != "unknown" {
		t.Fatalf("categoryOrUnknown() = %q, want unknown", got)
	}
}
