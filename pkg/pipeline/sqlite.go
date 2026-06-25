package pipeline

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"errors"
	"fmt"
	"time"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO), registered as "sqlite"
)

// SQLiteStore is a CheckpointStore backed by a pure-Go SQLite database
// (modernc.org/sqlite — CGO-free, single binary). It persists pipeline state,
// the durable conversation log, and the audit trail across process restarts.
type SQLiteStore struct {
	db *sql.DB
}

var _ CheckpointStore = (*SQLiteStore)(nil)

// OpenSQLite opens (creating if needed) a SQLite-backed store at path. Use
// ":memory:" for an ephemeral database (note: a :memory: DB lives only as long
// as its single connection).
func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite %q: %w", path, err)
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
CREATE TABLE IF NOT EXISTS checkpoints (
	pipeline_id   TEXT PRIMARY KEY,
	current_stage TEXT NOT NULL,
	status        INTEGER NOT NULL,
	state         BLOB NOT NULL,
	updated_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS history (
	pipeline_id TEXT NOT NULL,
	seq         INTEGER NOT NULL,
	msg         BLOB NOT NULL,
	PRIMARY KEY (pipeline_id, seq)
);
CREATE TABLE IF NOT EXISTS audit (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	pipeline_id TEXT NOT NULL,
	ts          INTEGER NOT NULL,
	stage       TEXT NOT NULL,
	action      TEXT NOT NULL,
	detail      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_pipeline ON audit(pipeline_id, id);
CREATE TABLE IF NOT EXISTS checkpoint_steps (
	pipeline_id TEXT NOT NULL,
	seq         INTEGER NOT NULL,
	stage       TEXT NOT NULL,
	status      INTEGER NOT NULL,
	state       BLOB NOT NULL,
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (pipeline_id, seq)
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrating sqlite schema: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Save(ctx context.Context, st *PipelineState) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(st); err != nil {
		return fmt.Errorf("encoding checkpoint: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checkpoints (pipeline_id, current_stage, status, state, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(pipeline_id) DO UPDATE SET
		   current_stage=excluded.current_stage, status=excluded.status,
		   state=excluded.state, updated_at=excluded.updated_at`,
		st.PipelineID, st.CurrentStage, int(st.Status), buf.Bytes(), st.UpdatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("saving checkpoint: %w", err)
	}
	return nil
}

func (s *SQLiteStore) SaveStep(ctx context.Context, st *PipelineState) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(st); err != nil {
		return fmt.Errorf("encoding checkpoint step: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checkpoint_steps (pipeline_id, seq, stage, status, state, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(pipeline_id, seq) DO UPDATE SET
		   stage=excluded.stage, status=excluded.status, state=excluded.state, updated_at=excluded.updated_at`,
		st.PipelineID, st.Seq, st.CurrentStage, int(st.Status), buf.Bytes(), st.UpdatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("saving checkpoint step: %w", err)
	}
	return nil
}

func (s *SQLiteStore) LoadAt(ctx context.Context, pipelineID string, seq int) (*PipelineState, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT state FROM checkpoint_steps WHERE pipeline_id=? AND seq=?`, pipelineID, seq).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading checkpoint step: %w", err)
	}
	var st PipelineState
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&st); err != nil {
		return nil, fmt.Errorf("decoding checkpoint step: %w", err)
	}
	return &st, nil
}

func (s *SQLiteStore) ListCheckpoints(ctx context.Context, pipelineID string) ([]CheckpointInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, stage, status, updated_at FROM checkpoint_steps WHERE pipeline_id=? ORDER BY seq`, pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckpointInfo
	for rows.Next() {
		var ci CheckpointInfo
		var status int
		var ts int64
		if err := rows.Scan(&ci.Seq, &ci.Stage, &status, &ts); err != nil {
			return nil, err
		}
		ci.Status = Status(status)
		ci.UpdatedAt = time.Unix(0, ts)
		out = append(out, ci)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Load(ctx context.Context, pipelineID string) (*PipelineState, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT state FROM checkpoints WHERE pipeline_id=?`, pipelineID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading checkpoint: %w", err)
	}
	var st PipelineState
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&st); err != nil {
		return nil, fmt.Errorf("decoding checkpoint: %w", err)
	}
	return &st, nil
}

func (s *SQLiteStore) List(ctx context.Context) ([]PipelineInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pipeline_id, current_stage, status, updated_at FROM checkpoints ORDER BY pipeline_id`)
	if err != nil {
		return nil, fmt.Errorf("listing checkpoints: %w", err)
	}
	defer rows.Close()
	var out []PipelineInfo
	for rows.Next() {
		var info PipelineInfo
		var status int
		var ts int64
		if err := rows.Scan(&info.PipelineID, &info.CurrentStage, &status, &ts); err != nil {
			return nil, err
		}
		info.Status = Status(status)
		info.UpdatedAt = time.Unix(0, ts)
		out = append(out, info)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) AppendHistory(ctx context.Context, pipelineID string, msgs []llm.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq)+1, 0) FROM history WHERE pipeline_id=?`, pipelineID).Scan(&next); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO history (pipeline_id, seq, msg) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, m := range msgs {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(m); err != nil {
			return fmt.Errorf("encoding history msg: %w", err)
		}
		if _, err := stmt.ExecContext(ctx, pipelineID, next+i, buf.Bytes()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// History returns the durable log for a pipeline in order (recovery/test helper;
// not part of CheckpointStore).
func (s *SQLiteStore) History(ctx context.Context, pipelineID string) ([]llm.Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT msg FROM history WHERE pipeline_id=? ORDER BY seq`, pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []llm.Message
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		var m llm.Message
		if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (pipeline_id, ts, stage, action, detail) VALUES (?, ?, ?, ?, ?)`,
		e.PipelineID, e.Timestamp.UnixNano(), e.Stage, e.Action, e.Detail)
	if err != nil {
		return fmt.Errorf("appending audit: %w", err)
	}
	return nil
}

func (s *SQLiteStore) AuditLog(ctx context.Context, pipelineID string) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, stage, action, detail FROM audit WHERE pipeline_id=? ORDER BY id`, pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var ts int64
		e := AuditEntry{PipelineID: pipelineID}
		if err := rows.Scan(&ts, &e.Stage, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.Timestamp = time.Unix(0, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}
