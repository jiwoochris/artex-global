package db

// Cache of machine translations shown read-only in the UI (for example the English
// view of an edited agent prompt). Rows are keyed by the hash of one source paragraph
// and the target language, so editing a prompt only re-translates the paragraphs that
// changed. Nothing stored here is ever sent to an agent.

// CachedTranslations returns the cached translations into lang for the given source
// hashes, keyed by hash. Hashes without a cached row are absent from the map.
func (d *DB) CachedTranslations(lang string, hashes []string) (map[string]string, error) {
	out := make(map[string]string, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := d.Query(`SELECT src_hash, text FROM text_translations WHERE lang=$1 AND src_hash = ANY($2)`, lang, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h, text string
		if err := rows.Scan(&h, &text); err != nil {
			return nil, err
		}
		out[h] = text
	}
	return out, rows.Err()
}

// SaveTranslation stores (or replaces) the translation of one source paragraph.
func (d *DB) SaveTranslation(srcHash, lang, text, model string) error {
	_, err := d.Exec(`
INSERT INTO text_translations (src_hash, lang, text, model) VALUES ($1, $2, $3, $4)
ON CONFLICT (src_hash, lang) DO UPDATE SET text=EXCLUDED.text, model=EXCLUDED.model, created_at=now()`,
		srcHash, lang, text, model)
	return err
}
