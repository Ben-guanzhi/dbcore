package dbcore

// Dialect isolates every per-engine string difference (identifier quoting,
// placeholders, type-name mapping, paging, catalog introspection) into one place.
// Drivers stay engine-agnostic; all per-engine quirks (the source of P0-4, P1-2,
// P1-3) live only in Dialect implementations (contract §4.5).
type Dialect interface {
	Kind() DatabaseKind

	// QuoteIdent quotes an identifier path, e.g. ("schema","table") -> `"schema"."table"`.
	QuoteIdent(parts ...string) string

	// Placeholder renders the idx-th bind parameter (1-based): "?" / "$1" / "@p1" / ":1".
	Placeholder(idx int) string

	// TypeName maps a metadata column back to a DDL type name.
	TypeName(col ColumnInfo) string

	// LimitOffset renders a LIMIT/OFFSET clause (engine-specific syntax).
	LimitOffset(limit, offset int) string

	// BuildRowsQuery builds a parameterized SELECT for BrowseRows.
	BuildRowsQuery(schema, table string, req *TableRowsRequest) (string, []Value)

	// BuildCountQuery builds a parameterized COUNT(*) for BrowseRowsCount.
	BuildCountQuery(schema, table string, req *TableRowsRequest) (string, []Value)

	// CatalogQuery returns a parameterized SQL string for the given catalog kind.
	// `obj` is the relevant object name (table for columns/indexes/fks, "" otherwise).
	// Parameters (e.g. table names) MUST be bound, never interpolated (P0-4).
	CatalogQuery(kind CatalogKind, schema, obj string) (string, []Value)
}

// CatalogKind selects which introspection query CatalogQuery should produce.
type CatalogKind int

const (
	CatalogSchemas CatalogKind = iota
	CatalogTables
	CatalogColumns
	CatalogIndexes
	CatalogForeignKeys
	// CatalogPrimaryKey lists the primary-key constraint and its columns; used by
	// engines that reconstruct DDL from the catalog (SQL Server).
	CatalogPrimaryKey
	// CatalogBatchColumns is the schema-wide form of CatalogColumns: one round
	// trip for every table's and view's columns, row shape identical to
	// CatalogColumns with the table name PREPENDED (column 0 = table name).
	// Engines that cannot express it in one statement leave the case out —
	// their drivers then don't implement BatchColumnsReader and callers fall
	// back to per-table reads.
	CatalogBatchColumns
)
