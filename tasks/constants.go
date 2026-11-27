package tasks

import (
	"context"
	"fmt"

	"github.com/dev1-alexwei-net/guava/conversations"
)

// LookupConstant reads a named constant from the fileexplorer database using a
// read-only SQLite connection.
func LookupConstant(ctx context.Context, databasePath string, key conversations.ConstantKey) (string, error) {
	index, err := openFileIndex(ctx, databasePath)
	if err != nil {
		return "", err
	}
	defer index.close()

	var value string
	err = index.db.QueryRowContext(ctx, `
		SELECT constants_values.value
		FROM constants_fields
		JOIN constants_values USING (field_name)
		WHERE constants_fields.field_name = ?`, string(key)).Scan(&value)
	if err != nil {
		return "", fmt.Errorf("lookup constant %q: %w", key, err)
	}
	return value, nil
}
