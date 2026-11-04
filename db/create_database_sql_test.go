package db

import (
	"strings"
	"testing"
)

// TestCreateDatabaseSQLQuotesIdentifier pins the escaping of the CREATE DATABASE
// statement. PostgreSQL cannot take a database name as a query parameter, so the only
// defence against a quote in the configured database name is correct identifier quoting:
// every embedded double quote must be doubled and the payload must stay inside the
// identifier.
func TestCreateDatabaseSQLQuotesIdentifier(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "artex", `CREATE DATABASE "artex"`},
		{"hyphenated", "artex-prod", `CREATE DATABASE "artex-prod"`},
		{"embedded quote", `we"ird`, `CREATE DATABASE "we""ird"`},
		{
			"injection attempt",
			`x"; DROP DATABASE prod; --`,
			`CREATE DATABASE "x""; DROP DATABASE prod; --"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := createDatabaseSQL(c.in)
			if got != c.want {
				t.Fatalf("createDatabaseSQL(%q)\n got: %s\nwant: %s", c.in, got, c.want)
			}
			// Non-vacuity: whatever the name contains, it must sit inside one quoted
			// identifier, so a payload cannot escape into its own statement.
			if !strings.HasPrefix(got, `CREATE DATABASE "`) || !strings.HasSuffix(got, `"`) {
				t.Errorf("identifier is not fully quoted: %s", got)
			}
		})
	}
}
