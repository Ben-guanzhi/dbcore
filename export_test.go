package dbcore

// Test-only exports. BuildUpdateForTest lets the row-guard tests assert the
// rendered statement without widening the public API: clients never build the
// guarded UPDATE themselves — ApplyBatch does, from RowChange.Expect.

// BuildUpdateForTest exposes buildUpdate to the external test package.
var BuildUpdateForTest = buildUpdate
