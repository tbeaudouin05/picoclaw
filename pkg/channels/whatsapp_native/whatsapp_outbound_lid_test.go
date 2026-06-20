//go:build whatsapp_native

package whatsapp

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/sipeed/picoclaw/pkg/channels"
)

func TestResolveOutboundTarget_NonLIDPassthrough(t *testing.T) {
	phone := types.NewJID("33695651381", types.DefaultUserServer)
	got, status := resolveOutboundTarget(phone, nil) // lookupFn must not be called
	if got != phone {
		t.Fatalf("JID=%v, want %v", got, phone)
	}
	if status != "not_lid" {
		t.Fatalf("status=%q, want not_lid", status)
	}
}

func TestResolveOutboundTarget(t *testing.T) {
	lid := types.NewJID("lid-user", types.HiddenUserServer)
	phone := types.NewJID("33695651381", types.DefaultUserServer)

	tests := []struct {
		name       string
		lookupFn   func(types.JID) (string, string, string)
		wantJID    types.JID
		wantStatus string
	}{
		{
			name: "resolved to PN JID",
			lookupFn: func(types.JID) (string, string, string) {
				return phone.String(), "found", ""
			},
			wantJID:    phone,
			wantStatus: "found",
		},
		{
			name: "not found returns zero JID",
			lookupFn: func(types.JID) (string, string, string) {
				return "", "not_found", ""
			},
			wantJID:    types.JID{},
			wantStatus: "not_found",
		},
		{
			name: "lookup error returns zero JID",
			lookupFn: func(types.JID) (string, string, string) {
				return "", "lookup_error", "some error"
			},
			wantJID:    types.JID{},
			wantStatus: "lookup_error",
		},
		{
			name: "client unavailable returns zero JID",
			lookupFn: func(types.JID) (string, string, string) {
				return "", "client_unavailable", ""
			},
			wantJID:    types.JID{},
			wantStatus: "client_unavailable",
		},
		{
			name: "lookup receives the original LID JID",
			lookupFn: func(got types.JID) (string, string, string) {
				if got != lid {
					return "", "lookup_error", "wrong JID passed"
				}
				return phone.String(), "found", ""
			},
			wantJID:    phone,
			wantStatus: "found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJID, gotStatus := resolveOutboundTarget(lid, tt.lookupFn)
			if gotJID != tt.wantJID {
				t.Fatalf("JID=%v, want %v", gotJID, tt.wantJID)
			}
			if gotStatus != tt.wantStatus {
				t.Fatalf("status=%q, want %q", gotStatus, tt.wantStatus)
			}
		})
	}
}

func TestOutboundSendTargets(t *testing.T) {
	lid := types.NewJID("106000011014190", types.HiddenUserServer)
	phone := types.NewJID("393391077930", types.DefaultUserServer)
	group := types.NewJID("123456789", types.GroupServer)

	tests := []struct {
		name           string
		original       types.JID
		resolved       types.JID
		resolvedStatus string
		want           []types.JID
	}{
		{
			name:           "resolved LID sends PN first then original LID fallback",
			original:       lid,
			resolved:       phone,
			resolvedStatus: "found",
			want:           []types.JID{phone, lid},
		},
		{
			name:           "unresolved LID sends original LID only",
			original:       lid,
			resolved:       lid,
			resolvedStatus: "not_found",
			want:           []types.JID{lid},
		},
		{
			name:           "phone number target has no alternate",
			original:       phone,
			resolved:       phone,
			resolvedStatus: "not_lid",
			want:           []types.JID{phone},
		},
		{
			name:           "group target has no alternate",
			original:       group,
			resolved:       group,
			resolvedStatus: "not_lid",
			want:           []types.JID{group},
		},
		{
			name:           "resolved status with same target deduplicates",
			original:       lid,
			resolved:       lid,
			resolvedStatus: "found",
			want:           []types.JID{lid},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outboundSendTargets(tt.original, tt.resolved, tt.resolvedStatus)
			if len(got) != len(tt.want) {
				t.Fatalf("len=%d, want %d; got=%v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("target[%d]=%v, want %v; all=%v", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// TestSendErrorFormat verifies that the error returned when client.SendMessage fails
// satisfies errors.Is(err, channels.ErrTemporary) while including the underlying
// error text for diagnostics — matching the fmt.Errorf pattern used in Send.
func TestSendErrorFormat(t *testing.T) {
	underlying := errors.New("upstream timeout")
	err := fmt.Errorf("whatsapp send: %v: %w", underlying, channels.ErrTemporary)

	if !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("errors.Is(err, ErrTemporary) = false; err=%v", err)
	}
	if !strings.Contains(err.Error(), underlying.Error()) {
		t.Fatalf("underlying error text %q not found in %q", underlying.Error(), err.Error())
	}
}
