package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
	"github.com/Mininglamp-OSS/octo-marketplace/internal/storage"
)

type migrationFileReader struct {
	storage.Storage
	gets      []string
	readBytes int
}
type migrationCountedBody struct {
	io.ReadCloser
	parent *migrationFileReader
}

func (r migrationCountedBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.parent.readBytes += n
	return n, err
}
func (r *migrationFileReader) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	r.gets = append(r.gets, key)
	body, err := r.Storage.GetObject(ctx, key)
	if err != nil {
		return nil, err
	}
	return migrationCountedBody{ReadCloser: body, parent: r}, nil
}
func migrationFileStore(t *testing.T, objects map[string]string) *migrationFileReader {
	t.Helper()
	store := storage.NewLocal(t.TempDir(), "http://unused.invalid")
	for key, data := range objects {
		if err := store.PutObject(context.Background(), key, strings.NewReader(data), int64(len(data)), ""); err != nil {
			t.Fatal(err)
		}
	}
	return &migrationFileReader{Storage: store}
}
func migrationSkill(pkg string) *model.Plugin {
	space := "space-a"
	return &model.Plugin{ID: "embedded-skill", Type: model.PluginTypeSkill, SpaceID: &space, Package: json.RawMessage(pkg)}
}
func migrationStoredAttachment(path, key, content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf(`{"path":%q,"content_type":"storage","storage_uri":%q,"content_size":%d,"content_hash":%q}`, path, key, len(content), "sha256:"+hex.EncodeToString(sum[:]))
}

func TestResolveMigrationSkillFilesReadsStoredTextWithoutChangingSource(t *testing.T) {
	key := "plugins/space-a/attachments/large.md"
	content := strings.Repeat("reference\n", 30000)
	store := migrationFileStore(t, map[string]string{key: content})
	p := migrationSkill(string(packageWith(rawAtt("SKILL.md", "---\nname: original-name\n---\nInstructions"), migrationStoredAttachment("references/large.md", key, content))))
	// Exercise the current sidecar format: public packages omit storage_uri.
	var pkg map[string]any
	if err := json.Unmarshal(p.Package, &pkg); err != nil {
		t.Fatal(err)
	}
	pkg["attachments"].([]any)[1].(map[string]any)["storage_uri"] = ""
	p.Package, _ = json.Marshal(pkg)
	p.AttachmentKeys = json.RawMessage(`{"references/large.md":"` + key + `"}`)
	beforePackage, beforeKeys := string(p.Package), string(p.AttachmentKeys)
	files, err := ResolveMigrationSkillFiles(context.Background(), store, p)
	if err != nil || files["references/large.md"] != content || !strings.Contains(files["SKILL.md"], "name: original-name") {
		t.Fatalf("files count=%d err=%v", len(files), err)
	}
	if string(p.Package) != beforePackage || string(p.AttachmentKeys) != beforeKeys || p.SpaceID == nil || *p.SpaceID != "space-a" {
		t.Fatal("helper mutated the source used by migration CAS")
	}
	if !reflect.DeepEqual(store.gets, []string{key}) {
		t.Fatalf("unexpected reads %+v", store.gets)
	}
}

func TestResolveMigrationSkillFilesReadsTrustedLegacyArchiveAndAuthoritativeMarkdown(t *testing.T) {
	zipKey := "experts/original/skills/archive.zip"
	mdKey := "experts/original/skills/SKILL.md"
	archive := zipWith(t, map[string][]byte{"wrapped/SKILL.md": []byte("archive markdown"), "wrapped/references/note.md": []byte("supporting")})
	store := migrationFileStore(t, map[string]string{zipKey: string(archive), mdKey: "authoritative original markdown"})
	p := migrationSkill(string(packageWith(rawAtt("SKILL.md", "snapshot stub"), rawAtt("skill/ref.json", fmt.Sprintf(`{"object_key":%q,"zip_object_key":%q}`, mdKey, zipKey)))))
	source := string(p.Package)
	files, err := ResolveMigrationSkillFiles(context.Background(), store, p)
	if err != nil || files["SKILL.md"] != "authoritative original markdown" || files["references/note.md"] != "supporting" || len(files) != 2 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	if string(p.Package) != source {
		t.Fatal("rewrote legacy source package")
	}
	for _, key := range store.gets {
		if strings.HasPrefix(key, "plugins/migration-preview/") {
			t.Fatal("virtual adapter key reached object storage")
		}
	}
	// The old backfill uses file_url for an object key, never an HTTP request.
	p.Package = packageWith(rawAtt("skill/ref.json", fmt.Sprintf(`{"file_url":%q}`, zipKey)))
	files, err = ResolveMigrationSkillFiles(context.Background(), store, p)
	if err != nil || files["SKILL.md"] != "archive markdown" {
		t.Fatalf("file_url legacy key failed: %v", err)
	}
}

func TestResolveMigrationSkillFilesManagedArchiveChecksStoredDigest(t *testing.T) {
	key := "plugins/space-a/attachments/skill.zip"
	archive := string(zipWith(t, map[string][]byte{"SKILL.md": []byte("real"), "ref.txt": []byte("text")}))
	store := migrationFileStore(t, map[string]string{key: archive})
	p := migrationSkill(string(packageWith(rawAtt("SKILL.md", "stub"), migrationStoredAttachment("skill/package.zip", key, archive))))
	files, err := ResolveMigrationSkillFiles(context.Background(), store, p)
	if err != nil || files["SKILL.md"] != "real" {
		t.Fatal(err)
	}
	p.Package = json.RawMessage(strings.Replace(string(p.Package), "sha256:", "sha256:0", 1))
	if _, err := ResolveMigrationSkillFiles(context.Background(), store, p); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("corrupt digest=%v", err)
	}
}

func TestResolveMigrationSkillFilesRejectsUntrustedSourcesBeforeReading(t *testing.T) {
	for _, key := range []string{"plugins/space-b/attachments/foreign.md", "https://example.invalid/skill.zip", "../skills/escape.zip", "file:///tmp/secret", "private/arbitrary.zip"} {
		t.Run(key, func(t *testing.T) {
			store := migrationFileStore(t, nil)
			p := migrationSkill(string(packageWith(rawAtt("SKILL.md", "stub"), rawAtt("skill/ref.json", fmt.Sprintf(`{"zip_object_key":%q}`, key)))))
			if _, err := ResolveMigrationSkillFiles(context.Background(), store, p); err == nil {
				t.Fatal("untrusted source accepted")
			}
			if len(store.gets) != 0 {
				t.Fatalf("unauthorized object read: %+v", store.gets)
			}
		})
	}
}

func TestResolveMigrationSkillFilesFailsClosedOnCorruptionAndBounds(t *testing.T) {
	key := "plugins/space-a/attachments/file.txt"
	for _, tt := range []struct {
		name, content, pkg string
		want               error
	}{
		{"missing object", "", string(packageWith(rawAtt("SKILL.md", "stub"), `{"path":"ref.txt","content_type":"storage","storage_uri":"`+key+`"}`)), ErrIntegrity},
		{"oversized storage", strings.Repeat("x", 1<<20+200), string(packageWith(rawAtt("SKILL.md", "stub"), `{"path":"ref.txt","content_type":"storage","storage_uri":"`+key+`"}`)), ErrTooLarge},
		{"binary text", "bad\x00text", string(packageWith(rawAtt("SKILL.md", "stub"), `{"path":"ref.txt","content_type":"storage","storage_uri":"`+key+`"}`)), nil},
		{"unsafe path", "", string(packageWith(rawAtt("SKILL.md", "stub"), rawAtt("../outside.txt", "bad"))), nil},
		{"duplicate path", "", string(packageWith(rawAtt("SKILL.md", "one"), rawAtt("SKILL.md", "two"))), nil},
		{"missing document", "", string(packageWith(rawAtt("note.txt", "supporting"))), nil},
		{"unresolved legacy reference", "", string(packageWith(rawAtt("SKILL.md", "stub"), rawAtt("skill/ref.json", `{}`))), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objects := map[string]string{}
			if tt.content != "" {
				objects[key] = tt.content
			}
			store := migrationFileStore(t, objects)
			p := migrationSkill(tt.pkg)
			_, err := ResolveMigrationSkillFiles(context.Background(), store, p)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("unexpected error=%v want=%v", err, tt.want)
			}
			if tt.name == "oversized storage" && store.readBytes > 1<<20+1 {
				t.Fatalf("unbounded object fetch: %d bytes", store.readBytes)
			}
		})
	}
}
