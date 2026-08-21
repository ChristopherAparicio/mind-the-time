package ui

import (
	"io"
	"strings"
	"testing"
)

func TestOfflineHTMLWording(t *testing.T) {
	file, err := Assets.Open("static/offline.html")
	if err != nil {
		t.Fatalf("failed to open static/offline.html: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("failed to close file: %v", err)
		}
	}()

	contentBytes, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("failed to read static/offline.html: %v", err)
	}
	content := string(contentBytes)

	if !strings.Contains(content, "connexion au service est interrompue") {
		t.Errorf("offline.html does not state connection interruption: %s", content)
	}

	if !strings.Contains(content, "Veuillez vous reconnecter pour gérer vos trajets enregistrés") {
		t.Errorf("offline.html does not state saved trips can be managed after reconnection: %s", content)
	}

	if !strings.Contains(content, "L'évaluation PRIM reste indisponible dans cette version car son format de réponse n'est pas pris en charge") {
		t.Errorf("offline.html does not state PRIM is unavailable due to unsupported response format: %s", content)
	}

	if strings.Contains(content, "car l'évaluation PRIM est indisponible") && !strings.Contains(content, "car son format de réponse n'est pas pris en charge") {
		t.Errorf("offline.html conflates offline state with PRIM unavailability: %s", content)
	}

	if strings.Contains(content, "pouvez toujours") && strings.Contains(content, "trajets") && !strings.Contains(content, "reconnecter") {
		t.Errorf("offline.html claims saved trips are available while offline: %s", content)
	}
}
