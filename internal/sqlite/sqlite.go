package sqlite

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

func sqliteVecVersion(db *sql.DB) (string, error) {
	var vecVersion string
	err := db.QueryRow("select vec_version()").Scan(&vecVersion)
	if err != nil {
		return "", err
	}
	return vecVersion, nil
}

func SimilarPages(db *sql.DB, vector []float32) ([]string, error) {
	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize vector: %v", err)
	}

	rows, err := db.Query(`
		SELECT embeddings.path
		FROM embeddings
		ORDER BY vec_distance_cosine(embedding, ?) ASC
		LIMIT 5
	`, blob)
	if err != nil {
		return nil, fmt.Errorf("similarPages query error: %v", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("similarPages scan error: %v", err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func Insert(db *sql.DB, path string, stamp int64, vector []float32) error {
	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return fmt.Errorf("failed to serialize vector: %v", err)
	}

	// Insert into SQLite DB
	if _, err := db.Exec(`
			INSERT INTO embeddings(path, created_at, embedding)
			VALUES (?, ?, ?)
			ON CONFLICT(path) DO NOTHING
		    `, path, stamp, blob); err != nil {
		return fmt.Errorf("Failed to update database: %v", err)
	} else {
		log.Printf("updated database for page %s", path)
	}

	return nil
}

func Init(path string) *sql.DB {
	sqlite_vec.Auto()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Fatal("Failed to create database directory:", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS embeddings(
			path TEXT NOT NULL UNIQUE,
			embedding BLOB NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS embeddings_path ON embeddings(path);
	`)
	if err != nil {
		log.Fatalf("failed to create tables: %v", err)
	}

	vecVersion, err := sqliteVecVersion(db)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sqlite_vec version %s\n", vecVersion)
	return db
}
