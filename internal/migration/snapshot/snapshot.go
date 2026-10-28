package snapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Source struct{ DBName, ServerUUID, Version, Fingerprint string }
type File struct {
	Path, SHA256 string
	Size         int64
}
type Table struct {
	Name         string
	Columns      []string
	Rows         int64
	Schema, Data File
}
type Manifest struct {
	Format    int
	Source    Source
	CreatedAt time.Time
	Tables    []Table
	Objects   []File
}
type Bundle struct {
	Dir      string `json:"-"`
	ID       string
	Manifest Manifest
}

const receiptTable = "_marketplace_snapshot_receipts"

var identifier = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func fail(s string) error     { return errors.New("snapshot: " + s) }
func validName(s string) bool { return len(s) > 0 && len(s) <= 64 && identifier.MatchString(s) }
func quote(s string) string   { return "`" + s + "`" }
func validateDDL(s, name string) error {
	tokens, e := sqlTokens(s)
	if e != nil {
		return e
	}
	if !validName(name) || len(tokens) < 5 || strings.ToUpper(tokens[0]) != "CREATE" || strings.ToUpper(tokens[1]) != "TABLE" || tokens[2] != name || tokens[3] != "(" {
		return fail("invalid schema")
	}
	depth := 0
	for i, t := range tokens {
		u := strings.ToUpper(t)
		if t == ";" || t == "." || u == "SELECT" || u == "INSERT" || (u == "UPDATE" && !(depth > 0 && i > 0 && strings.EqualFold(tokens[i-1], "ON") && i+1 < len(tokens) && strings.EqualFold(tokens[i+1], "CURRENT_TIMESTAMP"))) || u == "DELETE" || u == "DROP" || u == "ALTER" || u == "DEFINER" || u == "CONNECTION" || u == "DIRECTORY" {
			return fail("unsafe schema")
		}
		if t == "(" {
			depth++
		}
		if t == ")" {
			depth--
			if depth < 0 {
				return fail("invalid schema")
			}
		}
		if u == "ENGINE" {
			j := i + 1
			if j < len(tokens) && tokens[j] == "=" {
				j++
			}
			if j >= len(tokens) || !strings.EqualFold(tokens[j], "InnoDB") {
				return fail("unsupported engine")
			}
		}
	}
	if depth != 0 {
		return fail("invalid schema")
	}
	return nil
}
func sqlTokens(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		if strings.ContainsRune(" \n\r\t", rune(c)) {
			i++
			continue
		}
		if c == '#' || c == '/' && i+1 < len(s) && s[i+1] == '*' || c == '-' && i+1 < len(s) && s[i+1] == '-' {
			return nil, fail("schema comments unsupported")
		}
		if c == '\'' || c == '"' || c == '`' {
			q := c
			i++
			var b strings.Builder
			closed := false
			for i < len(s) {
				if s[i] == '\\' && q != '`' {
					i += 2
					continue
				}
				if s[i] == q {
					if i+1 < len(s) && s[i+1] == q {
						b.WriteByte(q)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				b.WriteByte(s[i])
				i++
			}
			if !closed {
				return nil, fail("invalid quoting")
			}
			if q == '`' {
				out = append(out, b.String())
			} else {
				out = append(out, "<literal>")
			}
			continue
		}
		if strings.ContainsRune("(),.=;", rune(c)) {
			out = append(out, string(c))
			i++
			continue
		}
		start := i
		for i < len(s) && !strings.ContainsRune(" \n\r\t`'\"(),.=;", rune(s[i])) {
			i++
		}
		out = append(out, s[start:i])
	}
	return out, nil
}
func safeOpen(dir, name string) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == "" {
		return nil, fail("unsafe file")
	}
	p := filepath.Join(dir, name)
	st, e := os.Lstat(p)
	if e != nil || !st.Mode().IsRegular() {
		return nil, fail("unsafe file")
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, fail("cannot read file")
	}
	actual, e := f.Stat()
	if e != nil || !os.SameFile(st, actual) {
		f.Close()
		return nil, fail("file changed")
	}
	return f, nil
}
func checksum(dir, name string) (File, error) {
	f, e := safeOpen(dir, name)
	if e != nil {
		return File{}, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return File{}, fail("cannot checksum")
	}
	return File{name, hex.EncodeToString(h.Sum(nil)), n}, nil
}
func create(dir, name string) (*os.File, error) {
	f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, fail("cannot create private file")
	}
	return f, nil
}
func writeManifest(b *Bundle) error {
	data, e := json.Marshal(b.Manifest)
	if e != nil {
		return fail("cannot encode manifest")
	}
	h := sha256.Sum256(data)
	b.ID = hex.EncodeToString(h[:])
	for n, v := range map[string][]byte{"manifest.json": data, "manifest.sha256": []byte(b.ID)} {
		f, e := create(b.Dir, n)
		if e != nil {
			return e
		}
		_, e = f.Write(v)
		ce := f.Close()
		if e != nil || ce != nil {
			return fail("cannot write manifest")
		}
	}
	return nil
}
func verifyFile(dir string, f File) error {
	got, e := checksum(dir, f.Path)
	if e != nil {
		return e
	}
	if got != f {
		return fail("checksum mismatch")
	}
	return nil
}
func Load(dir string) (Bundle, error) {
	b := Bundle{Dir: dir}
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return b, fail("unsafe directory")
	}
	f, e := safeOpen(dir, "manifest.json")
	if e != nil {
		return b, e
	}
	data, e := io.ReadAll(io.LimitReader(f, 16<<20))
	f.Close()
	if e != nil {
		return b, fail("cannot read manifest")
	}
	f, e = safeOpen(dir, "manifest.sha256")
	if e != nil {
		return b, e
	}
	digest, e := io.ReadAll(io.LimitReader(f, 65))
	f.Close()
	h := sha256.Sum256(data)
	b.ID = hex.EncodeToString(h[:])
	if e != nil || string(digest) != b.ID {
		return b, fail("manifest checksum mismatch")
	}
	if json.Unmarshal(data, &b.Manifest) != nil || b.Manifest.Format != 1 || !validName(b.Manifest.Source.DBName) {
		return b, fail("invalid manifest")
	}
	seen := map[string]bool{}
	for _, t := range b.Manifest.Tables {
		if !validName(t.Name) || seen[t.Name] || t.Name == receiptTable || t.Rows < 0 || len(t.Columns) == 0 {
			return b, fail("invalid table")
		}
		seen[t.Name] = true
		for _, c := range t.Columns {
			if !validName(c) {
				return b, fail("invalid column")
			}
		}
		if t.Schema.Path != t.Name+".schema.sql" || t.Data.Path != t.Name+".rows.jsonl" {
			return b, fail("invalid paths")
		}
		for _, f := range []File{t.Schema, t.Data} {
			if e = verifyFile(dir, f); e != nil {
				return b, e
			}
		}
		ddl, e := readSchema(dir, t)
		if e != nil {
			return b, e
		}
		if e = validateDDL(ddl, t.Name); e != nil {
			return b, e
		}
		if e = readTable(b, t, func([]*[]byte) error { return nil }); e != nil {
			return b, e
		}
	}
	for _, f := range b.Manifest.Objects {
		if e = verifyFile(dir, f); e != nil {
			return b, e
		}
	}
	return b, nil
}
func readSchema(dir string, t Table) (string, error) {
	f, e := safeOpen(dir, t.Schema.Path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 16<<20))
	if e != nil {
		return "", fail("cannot read schema")
	}
	return string(b), nil
}
func readTable(b Bundle, t Table, visit func([]*[]byte) error) error {
	f, e := safeOpen(b.Dir, t.Data.Path)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	var count int64
	for {
		var cells []*[]byte
		e = d.Decode(&cells)
		if e == io.EOF {
			break
		}
		if e != nil || len(cells) != len(t.Columns) {
			return fail("invalid row file")
		}
		if e = visit(cells); e != nil {
			return e
		}
		count++
	}
	if count != t.Rows {
		return fail("row count mismatch")
	}
	return nil
}

// ReadTable verifies the complete bundle before streaming SQL cells; nil is SQL NULL.
func ReadTable(b Bundle, name string, visit func([]string, []*[]byte) error) error {
	v, e := Load(b.Dir)
	if e != nil {
		return e
	}
	if b.ID != v.ID {
		return fail("snapshot changed")
	}
	for _, t := range v.Manifest.Tables {
		if t.Name == name {
			return readTable(v, t, func(c []*[]byte) error { return visit(t.Columns, c) })
		}
	}
	return fail("table absent")
}
func Export(ctx context.Context, db *sql.DB, dir, fp string) (Bundle, error) {
	b := Bundle{Dir: dir, Manifest: Manifest{Format: 1, CreatedAt: time.Now().UTC()}}
	if fp == "" {
		return b, fail("fingerprint required")
	}
	if os.Mkdir(dir, 0700) != nil {
		return b, fail("directory must be new")
	}
	// Reserve and discard this session so callers' pooled connections never
	// inherit the migration time zone. SET SESSION does not modify source data.
	c, e := db.Conn(ctx)
	if e != nil {
		return b, fail("cannot connect source")
	}
	defer c.Close()
	defer c.Raw(func(any) error { return driver.ErrBadConn })
	if _, e = c.ExecContext(ctx, "SET SESSION time_zone = ?", "+00:00"); e != nil {
		return b, fail("cannot set source UTC session")
	}
	tx, e := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return b, fail("cannot begin read-only snapshot")
	}
	defer tx.Rollback()
	if tx.QueryRowContext(ctx, "SELECT DATABASE(), @@server_uuid, VERSION()").Scan(&b.Manifest.Source.DBName, &b.Manifest.Source.ServerUUID, &b.Manifest.Source.Version) != nil || !validName(b.Manifest.Source.DBName) {
		return b, fail("cannot identify source")
	}
	b.Manifest.Source.Fingerprint = fp
	rows, e := tx.QueryContext(ctx, "SELECT TABLE_NAME, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' ORDER BY BINARY TABLE_NAME")
	if e != nil {
		return b, fail("cannot list tables")
	}
	var names []string
	for rows.Next() {
		var n, engine string
		if rows.Scan(&n, &engine) != nil || !validName(n) || engine != "InnoDB" || n == receiptTable {
			rows.Close()
			return b, fail("unsupported table")
		}
		names = append(names, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return b, fail("cannot list tables")
	}
	for _, n := range names {
		t := Table{Name: n}
		var actual, ddl string
		if tx.QueryRowContext(ctx, "SHOW CREATE TABLE "+quote(n)).Scan(&actual, &ddl) != nil || actual != n {
			return b, fail("cannot read schema")
		}
		if e = validateDDL(ddl, n); e != nil {
			return b, e
		}
		f, e := create(dir, n+".schema.sql")
		if e != nil {
			return b, e
		}
		_, e = f.WriteString(ddl)
		ce := f.Close()
		if e != nil || ce != nil {
			return b, fail("cannot write schema")
		}
		t.Schema, e = checksum(dir, n+".schema.sql")
		if e != nil {
			return b, e
		}
		cols, e := tx.QueryContext(ctx, "SELECT COLUMN_NAME, EXTRA FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION", n)
		if e != nil {
			return b, fail("cannot read columns")
		}
		for cols.Next() {
			var c, extra string
			if cols.Scan(&c, &extra) != nil || !validName(c) {
				cols.Close()
				return b, fail("invalid column")
			}
			if !strings.Contains(strings.ToUpper(extra), "VIRTUAL GENERATED") && !strings.Contains(strings.ToUpper(extra), "STORED GENERATED") {
				t.Columns = append(t.Columns, c)
			}
		}
		e = cols.Err()
		cols.Close()
		if e != nil || len(t.Columns) == 0 {
			return b, fail("cannot read columns")
		}
		q := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			q[i] = quote(c)
		}
		rows, e = tx.QueryContext(ctx, "SELECT "+strings.Join(q, ",")+" FROM "+quote(n))
		if e != nil {
			return b, fail("cannot read rows")
		}
		f, e = create(dir, n+".rows.jsonl")
		if e != nil {
			rows.Close()
			return b, e
		}
		enc := json.NewEncoder(f)
		for rows.Next() {
			cells := make([]sql.RawBytes, len(q))
			dest := make([]any, len(q))
			for i := range cells {
				dest[i] = &cells[i]
			}
			if rows.Scan(dest...) != nil || enc.Encode(cells) != nil {
				rows.Close()
				f.Close()
				return b, fail("cannot encode row")
			}
			t.Rows++
		}
		e = rows.Err()
		rows.Close()
		ce = f.Close()
		if e != nil || ce != nil {
			return b, fail("cannot export rows")
		}
		t.Data, e = checksum(dir, n+".rows.jsonl")
		if e != nil {
			return b, e
		}
		b.Manifest.Tables = append(b.Manifest.Tables, t)
	}
	if tx.Commit() != nil {
		return b, fail("cannot commit source snapshot")
	}
	e = writeManifest(&b)
	return b, e
}

// AddObject copies one blob with bounded memory; Seal binds returned entries to ID.
func AddObject(dir string, r io.Reader) (File, error) {
	f, e := os.CreateTemp(dir, "object-pending-")
	if e != nil {
		return File{}, fail("cannot create object")
	}
	name := f.Name()
	defer os.Remove(name)
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), r)
	ce := f.Close()
	if e != nil || ce != nil {
		return File{}, fail("cannot copy object")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	dest := "object-" + digest
	if old, e := checksum(dir, dest); e == nil {
		if old.SHA256 != digest || old.Size != n {
			return File{}, fail("object mismatch")
		}
		return old, nil
	}
	if os.Link(name, filepath.Join(dir, dest)) != nil {
		return File{}, fail("cannot finalize object")
	}
	return File{dest, digest, n}, nil
}
func Seal(b Bundle) (Bundle, error) {
	v, e := Load(b.Dir)
	if e != nil || v.ID != b.ID {
		return b, fail("snapshot changed")
	}
	for _, f := range b.Manifest.Objects {
		if e = verifyFile(b.Dir, f); e != nil {
			return b, e
		}
	}
	if os.Remove(filepath.Join(b.Dir, "manifest.json")) != nil || os.Remove(filepath.Join(b.Dir, "manifest.sha256")) != nil {
		return b, fail("cannot seal")
	}
	e = writeManifest(&b)
	return b, e
}
