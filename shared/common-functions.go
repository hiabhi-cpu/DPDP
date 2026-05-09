package shared

import "github.com/jackc/pgx/v5/pgtype"

// Convert string to pgtype.Text
func NewText(s string) pgtype.Text {
	return pgtype.Text{
		String: s,
		Valid:  s != "",
	}
}
