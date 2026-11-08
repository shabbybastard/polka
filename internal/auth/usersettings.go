package auth

import (
	"database/sql"
	"fmt"
)

// userSettingKeys lists every per-user key stored in the settings table.
// Settings have no foreign key to users, so they must be deleted explicitly
// when an account is removed; otherwise a later account that reuses the id
// would inherit the previous user's reader address, preferences, KOReader
// key and Telegram language.
func readerEmailSettingKey(userID int64) string {
	return fmt.Sprintf("reader_email:%d", userID)
}

func userSettingKeys(userID int64) []string {
	return []string{
		readerEmailSettingKey(userID),
		fmt.Sprintf("reader_prefs:%d", userID),
		fmt.Sprintf("kosync_key:%d", userID),
		fmt.Sprintf("telegram.lang.%d", userID),
	}
}

func deleteUserSettingsTx(tx *sql.Tx, userID int64) error {
	for _, key := range userSettingKeys(userID) {
		if _, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, key); err != nil {
			return err
		}
	}
	return nil
}

func deleteUserSettings(db *sql.DB, userID int64) error {
	for _, key := range userSettingKeys(userID) {
		if _, err := db.Exec(`DELETE FROM settings WHERE key = ?`, key); err != nil {
			return err
		}
	}
	return nil
}
