package postgres

// rowScanner abstracts the common surface of *sql.Row and *sql.Rows
// — both expose `Scan(dest ...any) error` with identical semantics.
// Defining a small consumer-side interface here lets each row type's
// scanInto helper accept either, so single-row reads (QueryRowContext)
// and rows-iterator reads (QueryContext + rows.Next) share one code
// path per row type. This is the canonical "accept interfaces, return
// structs" pattern from Effective Go.
//
// The interface lives in its own file (rather than inside any one
// row file) because every row type uses it; placing it next to one
// owner would silently couple the others to that owner's file.
type rowScanner interface {
	Scan(dest ...any) error
}
