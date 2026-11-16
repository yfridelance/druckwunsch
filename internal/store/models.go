package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Material struct {
	Type  string  `json:"type"`
	Color string  `json:"color"`
	Grams float64 `json:"grams"`
}

type Profile struct {
	ID              int64
	ModelID         int64
	SourceProfileID string
	Name            string
	Printer         string
	Nozzle          string
	PrintTimeSec    int64
	FilamentG       float64
	PlateCount      int
	Materials       []Material
	CoverImage      string
	URL             string
}

// Model is a printable model from some platform, deduplicated by URL.
type Model struct {
	ID             int64
	Platform       string
	SourceID       string
	URL            string
	Title          string
	Designer       string
	DesignerURL    string
	Description    string
	License        string
	CoverImage     string
	Images         []string
	RawJSON        string
	MetadataStatus string
	FetchedAt      *time.Time
	Profiles       []Profile
}

// HasMetadata reports whether proper metadata was captured.
func (m Model) HasMetadata() bool { return m.MetadataStatus == "ok" }

// UpsertModel inserts the model or, if it exists, updates the metadata
// (only when the new data has metadata, so a bare URL never wipes data).
// Profiles are replaced when the new data carries any.
func (s *Store) UpsertModel(ctx context.Context, m Model) (int64, error) {
	images, _ := json.Marshal(nonNil(m.Images))
	status := "fehlt"
	var fetched any
	if m.Title != "" && m.MetadataStatus == "ok" {
		status = "ok"
		fetched = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM models WHERE url = ?`, m.URL).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `
			INSERT INTO models (platform, source_id, url, title, designer, designer_url, description, license,
			                    cover_image, images, raw_json, metadata_status, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.Platform, m.SourceID, m.URL, m.Title, m.Designer, m.DesignerURL, m.Description, m.License,
			m.CoverImage, string(images), m.RawJSON, status, fetched)
		if err != nil {
			return 0, err
		}
		id, _ = res.LastInsertId()
	case err != nil:
		return 0, err
	default:
		if status == "ok" {
			_, err = tx.ExecContext(ctx, `
				UPDATE models SET title=?, designer=?, designer_url=?, description=?, license=?, cover_image=?,
				       images=?, raw_json=?, metadata_status='ok', fetched_at=? WHERE id=?`,
				m.Title, m.Designer, m.DesignerURL, m.Description, m.License, m.CoverImage,
				string(images), m.RawJSON, fetched, id)
		} else if m.Title != "" {
			// e.g. manual title for a model without metadata
			_, err = tx.ExecContext(ctx, `UPDATE models SET title=? WHERE id=? AND metadata_status != 'ok'`, m.Title, id)
		}
		if err != nil {
			return 0, err
		}
	}

	if len(m.Profiles) > 0 {
		keep := make([]any, 0, len(m.Profiles))
		for _, p := range m.Profiles {
			mats, _ := json.Marshal(nonNil(p.Materials))
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO profiles (model_id, source_profile_id, name, printer, nozzle, print_time_sec,
				                      filament_g, plate_count, materials, cover_image, url)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (model_id, source_profile_id) DO UPDATE SET
				  name=excluded.name, printer=excluded.printer, nozzle=excluded.nozzle,
				  print_time_sec=excluded.print_time_sec, filament_g=excluded.filament_g,
				  plate_count=excluded.plate_count, materials=excluded.materials,
				  cover_image=excluded.cover_image, url=excluded.url`,
				id, p.SourceProfileID, p.Name, p.Printer, p.Nozzle, p.PrintTimeSec, p.FilamentG,
				p.PlateCount, string(mats), p.CoverImage, p.URL); err != nil {
				return 0, err
			}
			keep = append(keep, p.SourceProfileID)
		}
		args := append([]any{id}, keep...)
		if _, err := tx.ExecContext(ctx, `DELETE FROM profiles WHERE model_id = ? AND source_profile_id NOT IN (`+
			placeholders(len(keep))+`)`, args...); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (s *Store) ModelByURL(ctx context.Context, url string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM models WHERE url = ?`, url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) Model(ctx context.Context, id int64) (Model, error) {
	var m Model
	var images string
	var fetched sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, platform, source_id, url, title, designer, designer_url, description, license,
		       cover_image, images, metadata_status, fetched_at
		FROM models WHERE id = ?`, id).
		Scan(&m.ID, &m.Platform, &m.SourceID, &m.URL, &m.Title, &m.Designer, &m.DesignerURL, &m.Description,
			&m.License, &m.CoverImage, &images, &m.MetadataStatus, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if fetched.Valid {
		m.FetchedAt = &fetched.Time
	}
	_ = json.Unmarshal([]byte(images), &m.Images)
	m.Profiles, err = s.profiles(ctx, id)
	return m, err
}

func (s *Store) profiles(ctx context.Context, modelID int64) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, model_id, source_profile_id, name, printer, nozzle, print_time_sec, filament_g,
		       plate_count, materials, cover_image, url
		FROM profiles WHERE model_id = ? ORDER BY id`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		var p Profile
		var mats string
		if err := rows.Scan(&p.ID, &p.ModelID, &p.SourceProfileID, &p.Name, &p.Printer, &p.Nozzle,
			&p.PrintTimeSec, &p.FilamentG, &p.PlateCount, &mats, &p.CoverImage, &p.URL); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(mats), &p.Materials)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetImageURLs rewrites image references after caching them locally.
func (s *Store) SetImageURLs(ctx context.Context, modelID int64, cover string, images []string) error {
	b, _ := json.Marshal(nonNil(images))
	_, err := s.db.ExecContext(ctx, `UPDATE models SET cover_image = ?, images = ? WHERE id = ?`, cover, string(b), modelID)
	return err
}

func (s *Store) SetProfileCover(ctx context.Context, profileID int64, cover string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE profiles SET cover_image = ? WHERE id = ?`, cover, profileID)
	return err
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
