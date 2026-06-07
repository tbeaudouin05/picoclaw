//go:build whatsapp_native

package whatsapp

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

type diagnosticLIDStore struct {
	pnResult  types.JID
	lidResult types.JID
	err       error
	panicNow  bool
}

func (s *diagnosticLIDStore) GetPNForLID(context.Context, types.JID) (types.JID, error) {
	if s.panicNow {
		panic("diagnostic panic")
	}
	return s.pnResult, s.err
}

func (s *diagnosticLIDStore) GetLIDForPN(context.Context, types.JID) (types.JID, error) {
	if s.panicNow {
		panic("diagnostic panic")
	}
	return s.lidResult, s.err
}

type diagnosticStoreWithLIDs struct {
	LIDs interface{}
}

type diagnosticStoreNoLIDs struct{}

func TestCallJIDStoreLookupOnStore(t *testing.T) {
	input := types.NewJID("lid-user", types.HiddenUserServer)
	phone := types.NewJID("33695651381", types.DefaultUserServer)

	tests := []struct {
		name       string
		store      any
		wantJID    string
		wantStatus string
		wantErr    bool
	}{
		{
			name:       "nil store",
			store:      nil,
			wantStatus: "store_unavailable",
		},
		{
			name:       "non pointer store",
			store:      diagnosticStoreNoLIDs{},
			wantStatus: "store_unavailable",
		},
		{
			name:       "missing LIDs field",
			store:      &diagnosticStoreNoLIDs{},
			wantStatus: "mapping_api_unavailable",
		},
		{
			name:       "nil LIDs interface",
			store:      &diagnosticStoreWithLIDs{},
			wantStatus: "mapping_store_unavailable",
		},
		{
			name:       "found phone mapping",
			store:      &diagnosticStoreWithLIDs{LIDs: &diagnosticLIDStore{pnResult: phone}},
			wantJID:    phone.String(),
			wantStatus: "found",
		},
		{
			name:       "not found",
			store:      &diagnosticStoreWithLIDs{LIDs: &diagnosticLIDStore{}},
			wantStatus: "not_found",
		},
		{
			name:       "lookup error",
			store:      &diagnosticStoreWithLIDs{LIDs: &diagnosticLIDStore{err: errors.New("boom")}},
			wantStatus: "lookup_error",
			wantErr:    true,
		},
		{
			name:       "lookup mismatch",
			store:      &diagnosticStoreWithLIDs{LIDs: &diagnosticLIDStore{pnResult: types.NewJID("wrong", types.HiddenUserServer)}},
			wantJID:    types.NewJID("wrong", types.HiddenUserServer).String(),
			wantStatus: "lookup_mismatch",
		},
		{
			name:       "panic recovered",
			store:      &diagnosticStoreWithLIDs{LIDs: &diagnosticLIDStore{panicNow: true}},
			wantStatus: "mapping_lookup_panic",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJID, gotStatus, gotErr := callJIDStoreLookupOnStore(tt.store, "LIDs", "GetPNForLID", input, types.DefaultUserServer)
			if gotJID != tt.wantJID {
				t.Fatalf("jid=%q, want %q", gotJID, tt.wantJID)
			}
			if gotStatus != tt.wantStatus {
				t.Fatalf("status=%q, want %q", gotStatus, tt.wantStatus)
			}
			if tt.wantErr && gotErr == "" {
				t.Fatal("expected diagnostic error text")
			}
			if !tt.wantErr && gotErr != "" {
				t.Fatalf("err=%q, want empty", gotErr)
			}
		})
	}
}
