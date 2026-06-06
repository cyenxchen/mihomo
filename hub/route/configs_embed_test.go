package route

import (
	"strings"
	"testing"

	"github.com/metacubex/http"
	"github.com/metacubex/http/httptest"
	"github.com/metacubex/mihomo/tunnel"
)

func TestDecodeEmbedModeConfigPatch(t *testing.T) {
	patch, err := decodeEmbedModeConfigPatch(strings.NewReader(`{"mode":"direct"}`))
	if err != nil {
		t.Fatalf("expected mode-only config patch to decode: %v", err)
	}

	if patch.Mode == nil || *patch.Mode != tunnel.Direct {
		t.Fatalf("mode = %v, want %s", patch.Mode, tunnel.Direct)
	}
}

func TestDecodeEmbedModeConfigPatchRejectsUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"mode":"direct","mixed-port":7890}`,
		`{"mode":"direct","tun":{"enable":true}}`,
	} {
		if _, err := decodeEmbedModeConfigPatch(strings.NewReader(body)); err == nil {
			t.Fatalf("expected unknown field to be rejected for body %s", body)
		}
	}
}

func TestPatchEmbedModeConfigsRejectsMissingMode(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("PATCH", "/", strings.NewReader(`{}`))

	patchEmbedModeConfigs(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
