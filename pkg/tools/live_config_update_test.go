package tools

import "testing"

func TestRejectProtectedDotPathUpdates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		protected []string
		updates   map[string]any
		wantErr   bool
	}{
		{
			name:      "direct protected path rejected",
			protected: []string{"customer_behavior.ai_control_message"},
			updates:   map[string]any{"customer_behavior.ai_control_message": "bad"},
			wantErr:   true,
		},
		{
			name:      "ancestor update rejected",
			protected: []string{"customer_behavior.ai_control_message"},
			updates:   map[string]any{"customer_behavior": "bad"},
			wantErr:   true,
		},
		{
			name:      "descendant update rejected",
			protected: []string{"customer_behavior"},
			updates:   map[string]any{"customer_behavior.tone": "warm"},
			wantErr:   true,
		},
		{
			name:      "sibling update allowed",
			protected: []string{"customer_behavior.ai_control_message"},
			updates:   map[string]any{"customer_behavior.tone": "warm"},
		},
		{
			name:      "unrelated update allowed",
			protected: []string{"customer_behavior.ai_control_message"},
			updates:   map[string]any{"school.name": "Trees"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := rejectProtectedDotPathUpdates(tt.updates, tt.protected)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}
