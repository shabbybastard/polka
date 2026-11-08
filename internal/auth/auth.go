// Package auth — users, passwords and sessions. Stored in a separate
// users.db database so that re-importing the collection does not affect accounts.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shabbybastard/polka/internal/sqlitedrv"
	"golang.org/x/crypto/argon2"
)

const (
	RoleUser  = "user"
	RoleAdmin = "admin"

	sessionTTL = 7 * 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid login or password")
	ErrNotFound           = errors.New("user not found")
	ErrLoginTaken         = errors.New("login already exists")
	ErrLastAdmin          = errors.New("cannot remove the last administrator")
	ErrTelegramTaken      = errors.New("telegram id is already linked")
	ErrInvalidTelegramID  = errors.New("invalid telegram id")
)

type User struct {
	ID          int64
	Login       string
	DisplayName string
	Role        string
	Disabled    bool
	TelegramID  int64
	CreatedAt   string
	// PasswordHash is filled by Verify and GetByID. It is never exposed in
	// userJSON; the Basic-auth cache uses it to detect password changes made
	// by another process (for example `polka passwd`) without re-running
	// argon2 on every request.
	PasswordHash string
}

func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

func mapUserConstraint(err error) error {
	if err != nil && strings.Contains(err.Error(), "last administrator") {
		return ErrLastAdmin
	}
	return err
}

type Service struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	login         TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	display_name  TEXT NOT NULL DEFAULT '',
	role          TEXT NOT NULL DEFAULT 'user',
	disabled      INTEGER NOT NULL DEFAULT 0,
	telegram_id   INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT NOT NULL
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
CREATE TABLE IF NOT EXISTS pending_book_remap (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	generation INTEGER NOT NULL,
	mapping    TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// The trigger closes the race between adminsBeside() and the actual UPDATE:
// two concurrent requests could each see another admin and demote both.
const adminTriggers = `
CREATE TRIGGER IF NOT EXISTS users_last_admin_update
BEFORE UPDATE OF role, disabled ON users
WHEN OLD.role = 'admin' AND OLD.disabled = 0
 AND (NEW.role != 'admin' OR NEW.disabled = 1)
 AND (SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0 AND id != OLD.id) = 0
BEGIN
 SELECT RAISE(ABORT, 'last administrator');
END;

CREATE TRIGGER IF NOT EXISTS users_last_admin_delete
BEFORE DELETE ON users
WHEN OLD.role = 'admin' AND OLD.disabled = 0
 AND (SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0 AND id != OLD.id) = 0
BEGIN
 SELECT RAISE(ABORT, 'last administrator');
END;
`

func Open(path string) (*Service, error) {
	// synchronous=NORMAL is the same trade-off as polka.db: a power loss
	// can lose the last few transactions, but the database stays intact.
	db, err := sqlitedrv.Open(path, sqlitedrv.Options{
		WAL: true, SyncNormal: true, BusyTimeout: 10000, ForeignKeys: true,
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + progressSchema + settingsSchema + listsSchema + kosyncSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth schema: %w", err)
	}
	if err := migrateProgress(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth migrate progress: %w", err)
	}
	if err := migrateTelegram(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth migrate telegram: %w", err)
	}
	if err := migrateRatingTombstone(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth migrate ratings: %w", err)
	}
	if err := migrateListTombstones(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth migrate lists: %w", err)
	}
	if err := migrateUsersAutoincrement(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth migrate users: %w", err)
	}
	if _, err := db.Exec(adminTriggers); err != nil {
		db.Close()
		return nil, fmt.Errorf("auth admin triggers: %w", err)
	}
	return &Service{db: db}, nil
}

// tableColumns returns the column names of a fixed internal table.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func addColumnIfMissing(db *sql.DB, table, column, ddl string) error {
	cols, err := tableColumns(db, table)
	if err != nil {
		return err
	}
	if cols[column] {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + ddl)
	return err
}

// migrateProgress upgrades users.db from early builds that predate the
// overall/locator/deleted columns. Missing columns are added; existing ones
// are left untouched. Unlike the old implementation, real migration errors
// are returned instead of silently ignored.
func migrateProgress(db *sql.DB) error {
	if err := addColumnIfMissing(db, "reading_progress", "overall", `overall REAL NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "reading_progress", "locator", `locator TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// deleted is a tombstone: a removed position must still be exported
	// and merged, otherwise another device would restore the old progress.
	return addColumnIfMissing(db, "reading_progress", "deleted", `deleted INTEGER NOT NULL DEFAULT 0`)
}

// migrateTelegram upgrades users.db from builds that predate the optional
// Telegram link. It also restores the unique partial index on every start.
func migrateTelegram(db *sql.DB) error {
	if err := addColumnIfMissing(db, "users", "telegram_id", `telegram_id INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_telegram ON users (telegram_id) WHERE telegram_id != 0`); err != nil {
		// Old databases may contain duplicate Telegram links. Do not make
		// the whole account database unusable; SetTelegramID still rejects
		// new duplicates and the admin can resolve the old ones.
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil
		}
		return err
	}
	return nil
}

// migrateUsersAutoincrement rebuilds old users tables that used a plain
// INTEGER PRIMARY KEY. Without AUTOINCREMENT SQLite reuses the maximum rowid
// after the last user is deleted, which lets a new account inherit the old
// user's settings and Basic-auth cache entries. The rebuild is atomic; the
// foreign_keys pragma is disabled only for the duration of the swap so the
// sessions foreign key keeps pointing at the new table.
func migrateUsersAutoincrement(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'users'`).Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT") {
		return nil
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer db.Exec(`PRAGMA foreign_keys = ON`)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE users_new (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			login         TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			display_name  TEXT NOT NULL DEFAULT '',
			role          TEXT NOT NULL DEFAULT 'user',
			disabled      INTEGER NOT NULL DEFAULT 0,
			telegram_id   INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO users_new (id, login, password_hash, display_name, role, disabled, telegram_id, created_at)
		SELECT id, login, password_hash, display_name, role, disabled, telegram_id, created_at FROM users`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE users`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE users_new RENAME TO users`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_telegram ON users (telegram_id) WHERE telegram_id != 0`); err != nil &&
		!strings.Contains(err.Error(), "UNIQUE") {
		return err
	}
	// Keep the AUTOINCREMENT counter above every id that already exists.
	if _, err := tx.Exec(`DELETE FROM sqlite_sequence WHERE name = 'users'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO sqlite_sequence (name, seq) SELECT 'users', coalesce(max(id), 0) FROM users`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) Close() error { return s.db.Close() }

// --- Passwords: argon2id, parameters embedded in the hash ---

// argonSlots bounds concurrent 64 MiB Argon2 computations. Per-IP rate
// limiting does not help against a distributed botnet; this keeps the server
// alive under a password-guessing storm.
var argonSlots = make(chan struct{}, 2)

func argonIDKey(password, salt []byte) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argon2.IDKey(password, salt, 1, 64*1024, 4, 32)
}

func hashPassword(password string) string {
	salt := randomBytes(16)
	key := argonIDKey([]byte(password), salt)
	return fmt.Sprintf("argon2id$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(key))
}

func verifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err1 := hex.DecodeString(parts[1])
	want, err2 := hex.DecodeString(parts[2])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argonIDKey([]byte(password), salt)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand must not fail
	}
	return b
}

// RandomPassword returns a random password for initial setup.
func RandomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := randomBytes(14)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// --- Sessions ---

// EnsureLogin returns the user by login, creating it if
// missing (for desktop mode with auto-login).
func (s *Service) EnsureLogin(ctx context.Context, login, displayName, role string) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT id, login, display_name, role, disabled, telegram_id, created_at
		FROM users WHERE login = ?`, login).
		Scan(&u.ID, &u.Login, &u.DisplayName, &u.Role, &u.Disabled, &u.TelegramID, &u.CreatedAt)
	if err == nil {
		return &u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return s.CreateUser(ctx, login, RandomPassword(), displayName, role)
}

// Verify checks login/password without opening a session (HTTP Basic for OPDS).
func (s *Service) Verify(ctx context.Context, login, password string) (*User, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, login, password_hash, display_name, role, disabled, telegram_id, created_at
		FROM users WHERE login = ?`, strings.TrimSpace(login)).
		Scan(&u.ID, &u.Login, &hash, &u.DisplayName, &u.Role, &u.Disabled, &u.TelegramID, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (u.Disabled || !verifyPassword(password, hash))) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	u.PasswordHash = hash
	return &u, nil
}

// Login verifies credentials and opens a session.
func (s *Service) Login(ctx context.Context, login, password string) (string, *User, error) {
	u, err := s.Verify(ctx, login, password)
	if err != nil {
		return "", nil, err
	}

	token := hex.EncodeToString(randomBytes(32))
	expires := time.Now().UTC().Add(sessionTTL)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash(token), u.ID, expires.Format(time.RFC3339)); err != nil {
		return "", nil, err
	}
	return token, u, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
	return err
}

// GetByToken returns the session's user. A session past half of its
// lifetime gets extended (sliding renewal).
func (s *Service) GetByToken(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var u User
	var expiresStr string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.login, u.display_name, u.role, u.disabled, u.telegram_id, u.created_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, tokenHash(token)).
		Scan(&u.ID, &u.Login, &u.DisplayName, &u.Role, &u.Disabled, &u.TelegramID, &u.CreatedAt, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	expires, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil || time.Now().UTC().After(expires) || u.Disabled {
		s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
		return nil, ErrNotFound
	}

	if time.Until(expires) < sessionTTL/2 {
		newExpires := time.Now().UTC().Add(sessionTTL)
		s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
			newExpires.Format(time.RFC3339), tokenHash(token))
	}
	return &u, nil
}

// --- User management ---

func (s *Service) UsersCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Service) adminsBeside(ctx context.Context, excludeID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE role = ? AND disabled = 0 AND id != ?`,
		RoleAdmin, excludeID).Scan(&n)
	return n, err
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, login, display_name, role, disabled, telegram_id, created_at
		FROM users ORDER BY login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Login, &u.DisplayName, &u.Role, &u.Disabled, &u.TelegramID, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Service) CreateUser(ctx context.Context, login, password, displayName, role string) (*User, error) {
	return s.CreateUserWithTelegram(ctx, login, password, displayName, role, 0)
}

// CreateUserWithTelegram creates an account together with an optional
// Telegram link. Zero means "not linked".
func (s *Service) CreateUserWithTelegram(ctx context.Context, login, password, displayName, role string, telegramID int64) (*User, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return nil, errors.New("login and password are required")
	}
	if telegramID < 0 {
		return nil, ErrInvalidTelegramID
	}
	if role != RoleAdmin {
		role = RoleUser
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (login, password_hash, display_name, role, telegram_id) VALUES (?, ?, ?, ?, ?)`,
		login, hashPassword(password), strings.TrimSpace(displayName), role, telegramID)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "telegram_id"):
			return nil, ErrTelegramTaken
		case strings.Contains(err.Error(), "UNIQUE"):
			return nil, ErrLoginTaken
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetByID(ctx, id)
}

func (s *Service) GetByID(ctx context.Context, id int64) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT id, login, password_hash, display_name, role, disabled, telegram_id, created_at
		FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Login, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Disabled, &u.TelegramID, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// GetByTelegramID returns the account linked to a Telegram user. Disabled
// accounts are returned as-is; callers must re-check the flag on every use.
func (s *Service) GetByTelegramID(ctx context.Context, telegramID int64) (*User, error) {
	if telegramID <= 0 {
		return nil, ErrNotFound
	}
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT id, login, display_name, role, disabled, telegram_id, created_at
		FROM users WHERE telegram_id = ?`, telegramID).
		Scan(&u.ID, &u.Login, &u.DisplayName, &u.Role, &u.Disabled, &u.TelegramID, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// SetTelegramID links or unlinks (telegramID == 0) an account.
func (s *Service) SetTelegramID(ctx context.Context, userID, telegramID int64) error {
	if telegramID < 0 {
		return ErrInvalidTelegramID
	}
	if _, err := s.GetByID(ctx, userID); err != nil {
		return err
	}
	if telegramID != 0 {
		var linkedTo int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?`, telegramID).Scan(&linkedTo)
		if err == nil && linkedTo != userID {
			return ErrTelegramTaken
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET telegram_id = ? WHERE id = ?`, telegramID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "telegram_id") {
			return ErrTelegramTaken
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUser changes the provided fields (nil — leave as is). Demoting
// or disabling the last administrator is forbidden. It is kept as a
// convenience wrapper around UpdateUserFull for callers that do not touch
// Telegram or the reader address.
func (s *Service) UpdateUser(ctx context.Context, id int64, password, displayName, role *string, disabled *bool) error {
	_, err := s.UpdateUserFull(ctx, id, password, displayName, role, disabled, nil, nil)
	return err
}

// UpdateUserFull applies every user-management field in one transaction:
// password, display name, role, disabled flag, Telegram link and the
// per-user reader e-mail. This prevents the admin handler from leaving half
// of an update applied when a later step fails. Password hashing happens
// before the transaction starts, so the database lock is not held during
// argon2.
func (s *Service) UpdateUserFull(ctx context.Context, id int64, password, displayName, role *string, disabled *bool, telegramID *int64, readerEmail *string) (*User, error) {
	u, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if telegramID != nil && *telegramID < 0 {
		return nil, ErrInvalidTelegramID
	}

	demoting := role != nil && *role != RoleAdmin && u.Role == RoleAdmin
	disabling := disabled != nil && *disabled && !u.Disabled
	if u.Role == RoleAdmin && (demoting || disabling) {
		if n, err := s.adminsBeside(ctx, id); err != nil {
			return nil, err
		} else if n == 0 {
			return nil, ErrLastAdmin
		}
	}

	var newHash string
	if password != nil && *password != "" {
		newHash = hashPassword(*password)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if telegramID != nil {
		if *telegramID != 0 {
			var linkedTo int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE telegram_id = ?`, *telegramID).Scan(&linkedTo)
			if err == nil && linkedTo != id {
				return nil, ErrTelegramTaken
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET telegram_id = ? WHERE id = ?`, *telegramID, id); err != nil {
			return nil, err
		}
	}
	if newHash != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, id); err != nil {
			return nil, err
		}
		// A password change resets the user's other sessions.
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return nil, err
		}
	}
	if displayName != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET display_name = ? WHERE id = ?`, strings.TrimSpace(*displayName), id); err != nil {
			return nil, err
		}
	}
	if role != nil {
		r := RoleUser
		if *role == RoleAdmin {
			r = RoleAdmin
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, r, id); err != nil {
			return nil, mapUserConstraint(err)
		}
	}
	if disabled != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET disabled = ? WHERE id = ?`, boolToInt(*disabled), id); err != nil {
			return nil, mapUserConstraint(err)
		}
		if *disabled {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
				return nil, err
			}
		}
	}
	if readerEmail != nil {
		if *readerEmail == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, readerEmailSettingKey(id)); err != nil {
				return nil, err
			}
		} else if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			readerEmailSettingKey(id), *readerEmail); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	u, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin {
		if n, err := s.adminsBeside(ctx, id); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return mapUserConstraint(err)
	}
	if err := deleteUserSettingsTx(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
