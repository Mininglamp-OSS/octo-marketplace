package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"testing"
)

func TestProductionWindowsArchivePathsAreNormalizedWithoutCollision(t *testing.T) {
	for _, collision := range []bool{false, true} {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		names := []string{"SKILL.md", `scripts\client.py`}
		if collision {
			names = append(names, "scripts/client.py")
		}
		for _, n := range names {
			f, _ := w.Create(n)
			f.Write([]byte("Original content"))
		}
		w.Close()
		ref, _ := json.Marshal(map[string]string{"zip_object_key": "skills/windows/package.zip"})
		p := model.Plugin{ID: "windows", Type: model.PluginTypeSkill, Visibility: model.PluginVisibilitySystem, Package: pkg(raw("SKILL.md", "Original content"), raw("skill/ref.json", string(ref)))}
		files, err := Resolve(context.Background(), &p, func(context.Context, string, int64) ([]byte, error) { return buf.Bytes(), nil }, DefaultLimits())
		if collision {
			if err == nil {
				t.Fatal("normalization collision accepted")
			}
			continue
		}
		if err != nil {
			t.Fatalf("Windows path blocked: %v", err)
		}
		found := false
		for _, f := range files {
			if f.Path == "scripts/client.py" {
				found = string(f.Data) == "Original content"
			}
		}
		if !found {
			t.Fatal("normalized file missing")
		}
	}
}
