package oracle

// packages.go — the object families only the Oracle family has: PL/SQL
// packages, scheduled jobs and synonyms (W17).
//
// The queries live in sqltext so DM8 can share them; this file is the Oracle
// driver's half: run, then shape the rows.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/sqltext"
)

// Packages lists the schema's packages with which halves exist and whether
// either half is INVALID.
func (d *OracleDriver) Packages(ctx context.Context, schema string) ([]dbcore.PackageInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.PackagesQuery("oracle", s)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.PackageInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		if len(r) < 4 {
			continue
		}
		hasSpec, _ := dbcore.IntFrom(r[1])
		hasBody, _ := dbcore.IntFrom(r[2])
		out = append(out, dbcore.PackageInfo{
			Name:    r[0].S,
			HasSpec: hasSpec == 1,
			HasBody: hasBody == 1,
			Status:  r[3].S,
		})
	}
	return out, nil
}

// PackageDefinition renders a package as CREATE statements: the specification
// first, then the body, each only when that half exists. A package with one
// half answers that half — writing the missing one would be inventing code.
func (d *OracleDriver) PackageDefinition(ctx context.Context, schema, name string) (string, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	spec, err := d.packageSource(ctx, s, name, "PACKAGE")
	if err != nil {
		return "", err
	}
	body, err := d.packageSource(ctx, s, name, "PACKAGE BODY")
	if err != nil {
		return "", err
	}
	if spec == "" && body == "" {
		return "", fmt.Errorf("oracle: package %q not found", name)
	}
	var parts []string
	if spec != "" {
		parts = append(parts, spec)
	}
	if body != "" {
		parts = append(parts, body)
	}
	return strings.Join(parts, "\n\n"), nil
}

// packageSource reads one half of a package's source and prefixes the CREATE
// header ALL_SOURCE omits.
func (d *OracleDriver) packageSource(ctx context.Context, schema, name, part string) (string, error) {
	q, a, err := sqltext.PackageDefinitionQuery("oracle", schema, name, part)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range res.Rows {
		if r[0].Kind != dbcore.ValText {
			continue
		}
		b.WriteString(strings.TrimRight(r[0].S, " \t\r\n"))
		b.WriteString("\n")
	}
	source := strings.TrimSpace(b.String())
	if source == "" {
		return "", nil
	}
	if !strings.HasPrefix(strings.ToUpper(source), "CREATE") {
		source = "CREATE OR REPLACE " + source
	}
	return source, nil
}

// Jobs lists the schema's scheduled jobs from ALL_SCHEDULER_JOBS, which since
// 10g also carries the classic DBMS_JOB entries.
func (d *OracleDriver) Jobs(ctx context.Context, schema string) ([]dbcore.JobInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.JobsQuery("oracle", s)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.JobInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		if len(r) < 4 {
			continue
		}
		out = append(out, dbcore.JobInfo{
			Name:    r[0].S,
			Kind:    r[1].S,
			Enabled: r[2].S,
			NextRun: r[3].S,
		})
	}
	return out, nil
}

// Synonyms lists the synonyms visible from the schema: its own and the public
// ones, with the target's object type where the catalog can resolve it.
func (d *OracleDriver) Synonyms(ctx context.Context, schema string) ([]dbcore.SynonymInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.SynonymsQuery("oracle", s)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SynonymInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		if len(r) < 5 {
			continue
		}
		out = append(out, dbcore.SynonymInfo{
			Name:        r[0].S,
			Owner:       r[1].S,
			TargetOwner: r[2].S,
			TargetName:  r[3].S,
			TargetType:  r[4].S,
		})
	}
	return out, nil
}
