package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

func TestHistoricalLegacyFileUsesOriginalBytesAndValidatesPendingReview(t *testing.T) {
	ctx := context.Background()
	blobs := storage.NewLocal(t.TempDir(), "http://localhost")
	s := New(&fakeStore{}, blobs)
	key := "plugins/space-a/attachments/old-markdown"
	text := "actual historical markdown"
	sum := sha256.Sum256([]byte(text))
	if err := blobs.PutObject(ctx, key, strings.NewReader(text), int64(len(text)), ""); err != nil {
		t.Fatal(err)
	}
	ref, _ := json.Marshal(map[string]any{"object_key": key, "object_key_hash": "sha256:" + hex.EncodeToString(sum[:]), "object_key_size": len(text)})
	pkg, _ := json.Marshal(map[string]any{"attachments": []any{map[string]any{"path": "SKILL.md", "content_type": "raw", "raw_content": "inline stub"}, map[string]any{"path": "skill/ref.json", "content_type": "raw", "raw_content": string(ref)}}})
	v := &model.PluginVersion{Package: pkg} // pending review has no artifacts_json yet
	space := "space-a"
	p := &model.Plugin{SpaceID: &space, Type: model.PluginTypeSkill}
	got, err := s.versionFileBytes(ctx, p, v, "SKILL.md")
	if err != nil || string(got) != text {
		t.Fatalf("returned stub/incorrect history: %q %v", got, err)
	}
	if err = s.verifyVersionFiles(ctx, p, v); err != nil {
		t.Fatal(err)
	}
	if err = blobs.DeleteObject(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err = s.verifyVersionFiles(ctx, p, v); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("missing review object approved: %v", err)
	}
}
