package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO), registered as "sqlite"
)

// SQLiteStore is a persistent VectorStore backed by a pure-Go SQLite database
// (modernc.org/sqlite — CGO-free, single binary), so long-term memory survives
// process restarts. Search is brute-force cosine over a namespace's rows (loaded
// into memory), which is fine at modest scale; pgvector/qdrant adapters can
// implement VectorStore later for large corpora.
type SQLiteStore struct {
	db *sql.DB
}

var _ VectorStore = (*SQLiteStore)(nil)

// OpenSQLiteStore opens (creating if needed) a memory store at path. Use
// ":memory:" for an ephemeral database.
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("memory: opening sqlite %q: %w", path, err)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS memory (
	id         TEXT PRIMARY KEY,
	namespace  TEXT NOT NULL,
	text       TEXT NOT NULL,
	metadata   BLOB,
	vector     BLOB NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_ns ON memory(namespace);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("memory: migrating schema: %w", err)
	}
	return nil
}

// Add upserts documents. Vectors are stored as little-endian float32 blobs;
// metadata as JSON.
func (s *SQLiteStore) Add(ctx context.Context, docs []Document) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO memory (id, namespace, text, metadata, vector, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   namespace=excluded.namespace, text=excluded.text,
		   metadata=excluded.metadata, vector=excluded.vector`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, d := range docs {
		meta, err := json.Marshal(d.Metadata)
		if err != nil {
			return fmt.Errorf("memory: encoding metadata: %w", err)
		}
		if _, err := stmt.ExecContext(ctx, d.ID, d.Namespace, d.Text, meta, encodeVector(d.Vector), createdAtSentinel); err != nil {
			return fmt.Errorf("memory: adding document: %w", err)
		}
	}
	return tx.Commit()
}

// Search loads a namespace's rows and returns the top-k by cosine similarity.
func (s *SQLiteStore) Search(ctx context.Context, namespace string, vector []float32, k int) ([]Scored, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, text, metadata, vector FROM memory WHERE namespace=?`, namespace)
	if err != nil {
		return nil, fmt.Errorf("memory: querying namespace: %w", err)
	}
	defer rows.Close()

	var scored []Scored
	for rows.Next() {
		var id, text string
		var meta, vec []byte
		if err := rows.Scan(&id, &text, &meta, &vec); err != nil {
			return nil, err
		}
		d := Document{ID: id, Text: text, Namespace: namespace, Vector: decodeVector(vec)}
		if len(meta) > 0 {
			_ = json.Unmarshal(meta, &d.Metadata)
		}
		scored = append(scored, Scored{Document: d, Score: cosine(vector, d.Vector)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return topK(scored, k), nil
}

// Delete removes documents by ID.
func (s *SQLiteStore) Delete(ctx context.Context, ids ...string) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM memory WHERE id=?`, id); err != nil {
			return fmt.Errorf("memory: deleting %q: %w", id, err)
		}
	}
	return nil
}

// createdAtSentinel is a fixed timestamp: the memory subsystem does not order by
// creation time (search is by similarity), and avoiding time.Now keeps writes
// deterministic. Callers who need real timestamps can carry one in Metadata.
const createdAtSentinel = 0

// encodeVector serializes a float32 slice as little-endian bytes.
func encodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector is the inverse of encodeVector.
func decodeVector(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
