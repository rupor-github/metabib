package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	jsonv2 "encoding/json/v2"
)

const (
	importProvenanceSchema = "metabib.import_provenance/1"
	importProvenanceTable  = "metabib_import_provenance"
)

var ErrImportProvenanceNotFound = errors.New("database import provenance not found")

type ImportProvenance struct {
	Schema   string                 `json:"schema"`
	DumpDir  string                 `json:"dump_dir"`
	DumpDate string                 `json:"dump_date,omitempty"`
	Dumps    []ImportDumpProvenance `json:"dumps"`
}

type ImportDumpProvenance struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	DumpDate      string `json:"dump_date,omitempty"`
	DumpCompleted string `json:"dump_completed,omitempty"`
	Modified      string `json:"modified"`
	MD5           string `json:"md5"`
}

func (r *Repository) WriteImportProvenance(ctx context.Context, provenance ImportProvenance) error {
	provenance.Schema = importProvenanceSchema
	payload, err := jsonv2.Marshal(provenance)
	if err != nil {
		return fmt.Errorf("encode import provenance: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS `+quoteIdentifier(importProvenanceTable)+` (
  id TINYINT NOT NULL PRIMARY KEY,
  payload LONGTEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci`); err != nil {
		return fmt.Errorf("create import provenance table: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `REPLACE INTO `+quoteIdentifier(importProvenanceTable)+` (id, payload) VALUES (1, ?)`, string(payload)); err != nil {
		return fmt.Errorf("write import provenance: %w", err)
	}
	r.tmu.Lock()
	r.tables[importProvenanceTable] = true
	r.tmu.Unlock()
	return nil
}

func (r *Repository) ImportProvenance(ctx context.Context) (ImportProvenance, error) {
	exists, err := r.tableExists(ctx, importProvenanceTable)
	if err != nil {
		return ImportProvenance{}, err
	}
	if !exists {
		return ImportProvenance{}, ErrImportProvenanceNotFound
	}
	var payload string
	if err := r.db.QueryRowContext(ctx, `SELECT payload FROM `+quoteIdentifier(importProvenanceTable)+` WHERE id = 1`).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ImportProvenance{}, ErrImportProvenanceNotFound
		}
		return ImportProvenance{}, fmt.Errorf("read import provenance: %w", err)
	}
	var provenance ImportProvenance
	if err := jsonv2.Unmarshal([]byte(payload), &provenance); err != nil {
		return ImportProvenance{}, fmt.Errorf("decode import provenance: %w", err)
	}
	if provenance.Schema != importProvenanceSchema {
		return ImportProvenance{}, fmt.Errorf("import provenance has unexpected schema %q", provenance.Schema)
	}
	return provenance, nil
}
