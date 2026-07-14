//go:build whatsapp_native

package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/store"
)

func TestWhatsmeowDefaultClientVersionIsNotKnownOutdated(t *testing.T) {
	knownOutdated := store.WAVersionContainer{2, 3000, 1033703022}
	if got := store.GetWAVersion(); got == knownOutdated {
		t.Fatalf("whatsmeow default client version is the known-outdated %s", got.String())
	}
}
