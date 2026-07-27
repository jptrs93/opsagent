package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jptrs93/opsagent/testexamples/libraryapp/backend/apigen"
)

//go:generate sh -c "cd ../frontend && pnpm install && pnpm run build"
//go:embed web/dist
var embeddedWeb embed.FS

type config struct {
	HTTPAddr string
	PGHost   string
	PGPort   string
	PGUser   string
	PGPass   string
	PGDB     string
	PGSSL    string
}

type handler struct {
	db         *pgxpool.Pool
	web        fs.FS
	fileServer http.Handler
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, configFromEnv()); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("library app stopped", "err", err)
		os.Exit(1)
	}
}

func configFromEnv() config {
	return config{
		HTTPAddr: envOr("HTTP_ADDR", ":8080"),
		PGHost:   envOr("PGHOST", "127.0.0.1"),
		PGPort:   envOr("PGPORT", "5432"),
		PGUser:   envOr("PGUSER", "postgres"),
		PGPass:   os.Getenv("PGPASSWORD"),
		PGDB:     envOr("PGDATABASE", "postgres"),
		PGSSL:    envOr("PGSSLMODE", "prefer"),
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func run(ctx context.Context, cfg config) error {
	web, err := fs.Sub(embeddedWeb, "web/dist")
	if err != nil {
		return fmt.Errorf("open embedded frontend: %w", err)
	}

	db, err := connectPostgres(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	h := &handler{db: db, web: web, fileServer: http.FileServer(http.FS(web))}
	apiMux := apigen.CreateMux(h, &apigen.MuxConfig{
		MaxRequestBodySize: 64 * 1024,
		VerifyAuth: func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, _ apigen.AccessPolicy) (apigen.Context, error) {
			return apigen.Context{Context: ctx}, nil
		},
	})

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           apiMux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("library app listening", "address", cfg.HTTPAddr, "postgres_host", cfg.PGHost, "postgres_database", cfg.PGDB)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func connectPostgres(ctx context.Context, cfg config) (*pgxpool.Pool, error) {
	port, err := strconv.ParseUint(cfg.PGPort, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid PGPORT %q: %w", cfg.PGPort, err)
	}
	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.PGUser, cfg.PGPass),
		Host:   net.JoinHostPort(cfg.PGHost, strconv.FormatUint(port, 10)),
		Path:   cfg.PGDB,
	}
	query := dsn.Query()
	query.Set("sslmode", cfg.PGSSL)
	dsn.RawQuery = query.Encode()

	for attempt := 1; ; attempt++ {
		db, openErr := pgxpool.New(ctx, dsn.String())
		if openErr == nil {
			openErr = db.Ping(ctx)
		}
		if openErr == nil {
			openErr = createSchema(ctx, db)
		}
		if openErr == nil {
			return db, nil
		}
		if db != nil {
			db.Close()
		}
		slog.Warn("waiting for postgres", "attempt", attempt, "err", openErr)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func createSchema(ctx context.Context, db *pgxpool.Pool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS authors (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL CHECK (length(btrim(name)) > 0),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS genres (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL CHECK (length(btrim(name)) > 0),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS books (
			id BIGSERIAL PRIMARY KEY,
			title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
			author_id BIGINT NOT NULL REFERENCES authors(id),
			genre_id BIGINT NOT NULL REFERENCES genres(id),
			publication_year INTEGER NOT NULL CHECK (publication_year BETWEEN 1 AND 2100),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement); err != nil {
			return fmt.Errorf("create library schema: %w", err)
		}
	}
	return nil
}

func (h *handler) Get(_ apigen.Context, request *http.Request, response http.ResponseWriter) error {
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Referrer-Policy", "same-origin")

	name := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	if info, err := fs.Stat(h.web, name); err != nil || info.IsDir() {
		request = request.Clone(request.Context())
		request.URL.Path = "/"
	}
	h.fileServer.ServeHTTP(response, request)
	return nil
}

func (h *handler) GetV1Healthz(ctx apigen.Context, _ *http.Request, response http.ResponseWriter) error {
	if err := h.db.Ping(ctx); err != nil {
		return apiError("PostgreSQL is unavailable", err, http.StatusServiceUnavailable)
	}
	response.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *handler) GetV1Library(ctx apigen.Context) (*apigen.LibrarySnapshot, error) {
	return h.snapshot(ctx)
}

func (h *handler) PostV1Author(ctx apigen.Context, request *apigen.AddAuthorRequest) (*apigen.LibrarySnapshot, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return nil, apiError("Author name is required", nil, http.StatusBadRequest)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO authors (name) VALUES ($1)`, name); err != nil {
		return nil, apiError("Could not add the author", err, http.StatusBadRequest)
	}
	return h.snapshot(ctx)
}

func (h *handler) PostV1Genre(ctx apigen.Context, request *apigen.AddGenreRequest) (*apigen.LibrarySnapshot, error) {
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return nil, apiError("Genre name is required", nil, http.StatusBadRequest)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO genres (name) VALUES ($1)`, name); err != nil {
		return nil, apiError("Could not add the genre", err, http.StatusBadRequest)
	}
	return h.snapshot(ctx)
}

func (h *handler) PostV1Book(ctx apigen.Context, request *apigen.AddBookRequest) (*apigen.LibrarySnapshot, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" || request.AuthorID <= 0 || request.GenreID <= 0 || request.PublicationYear < 1 || request.PublicationYear > 2100 {
		return nil, apiError("Title, author, genre, and a valid publication year are required", nil, http.StatusBadRequest)
	}
	_, err := h.db.Exec(ctx,
		`INSERT INTO books (title, author_id, genre_id, publication_year) VALUES ($1, $2, $3, $4)`,
		title, request.AuthorID, request.GenreID, request.PublicationYear,
	)
	if err != nil {
		return nil, apiError("Could not add the book; check that its author and genre still exist", err, http.StatusBadRequest)
	}
	return h.snapshot(ctx)
}

func (h *handler) PostV1LibraryRandom(ctx apigen.Context) (*apigen.LibrarySnapshot, error) {
	authors := []string{"Avery North", "Mara Bell", "Jonas Reed", "Nia Calder", "Theo Vale", "Iris Moss"}
	genres := []string{"Field guide", "Small histories", "Speculative fiction", "Natural philosophy", "Travel notes"}
	adjectives := []string{"Hidden", "Last", "Quiet", "Borrowed", "Northern", "Paper"}
	nouns := []string{"Orchard", "Atlas", "Tide", "Constellation", "Archive", "Lantern"}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		return nil, apiError("Could not begin a random shelf", err, http.StatusInternalServerError)
	}
	defer tx.Rollback(ctx)

	var authorID, genreID int64
	if err := tx.QueryRow(ctx, `INSERT INTO authors (name) VALUES ($1) RETURNING id`, authors[rand.IntN(len(authors))]).Scan(&authorID); err != nil {
		return nil, apiError("Could not create a random author", err, http.StatusInternalServerError)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO genres (name) VALUES ($1) RETURNING id`, genres[rand.IntN(len(genres))]).Scan(&genreID); err != nil {
		return nil, apiError("Could not create a random genre", err, http.StatusInternalServerError)
	}
	title := "The " + adjectives[rand.IntN(len(adjectives))] + " " + nouns[rand.IntN(len(nouns))]
	year := 1860 + rand.IntN(165)
	if _, err := tx.Exec(ctx,
		`INSERT INTO books (title, author_id, genre_id, publication_year) VALUES ($1, $2, $3, $4)`,
		title, authorID, genreID, year,
	); err != nil {
		return nil, apiError("Could not create a random book", err, http.StatusInternalServerError)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apiError("Could not save the random shelf", err, http.StatusInternalServerError)
	}
	return h.snapshot(ctx)
}

func (h *handler) snapshot(ctx context.Context) (*apigen.LibrarySnapshot, error) {
	snapshot := &apigen.LibrarySnapshot{}

	rows, err := h.db.Query(ctx, `SELECT id, name FROM authors ORDER BY id`)
	if err != nil {
		return nil, apiError("Could not load authors", err, http.StatusInternalServerError)
	}
	for rows.Next() {
		author := &apigen.Author{}
		if err := rows.Scan(&author.ID, &author.Name); err != nil {
			rows.Close()
			return nil, apiError("Could not read an author", err, http.StatusInternalServerError)
		}
		snapshot.Authors = append(snapshot.Authors, author)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, apiError("Could not load authors", err, http.StatusInternalServerError)
	}
	rows.Close()

	rows, err = h.db.Query(ctx, `SELECT id, name FROM genres ORDER BY id`)
	if err != nil {
		return nil, apiError("Could not load genres", err, http.StatusInternalServerError)
	}
	for rows.Next() {
		genre := &apigen.Genre{}
		if err := rows.Scan(&genre.ID, &genre.Name); err != nil {
			rows.Close()
			return nil, apiError("Could not read a genre", err, http.StatusInternalServerError)
		}
		snapshot.Genres = append(snapshot.Genres, genre)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, apiError("Could not load genres", err, http.StatusInternalServerError)
	}
	rows.Close()

	rows, err = h.db.Query(ctx, `
		SELECT b.id, b.title, b.author_id, a.name, b.genre_id, g.name, b.publication_year
		FROM books b
		JOIN authors a ON a.id = b.author_id
		JOIN genres g ON g.id = b.genre_id
		ORDER BY b.id
	`)
	if err != nil {
		return nil, apiError("Could not load books", err, http.StatusInternalServerError)
	}
	defer rows.Close()
	for rows.Next() {
		book := &apigen.Book{}
		if err := rows.Scan(&book.ID, &book.Title, &book.AuthorID, &book.AuthorName, &book.GenreID, &book.GenreName, &book.PublicationYear); err != nil {
			return nil, apiError("Could not read a book", err, http.StatusInternalServerError)
		}
		snapshot.Books = append(snapshot.Books, book)
	}
	if err := rows.Err(); err != nil {
		return nil, apiError("Could not load books", err, http.StatusInternalServerError)
	}
	return snapshot, nil
}

func apiError(display string, err error, status int32) apigen.ApiErr {
	internal := ""
	if err != nil {
		internal = err.Error()
	}
	return apigen.NewApiErr(display, internal, status)
}
