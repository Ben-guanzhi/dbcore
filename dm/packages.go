package dm

// packages.go — the Oracle-family object families on DM8: PL/SQL packages and
// synonyms (W17).
//
// DM8 keeps the Oracle data dictionary closely enough that the package and
// synonym recipes are shared verbatim. Jobs are the exception: a live
// instance exposed no job catalog at all (see JobsQuery), so this driver does
// not claim JobReader.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/sqltext"
)

// Packages lists the schema's packages with which halves exist and whether
// either half is INVALID.
func (d *DMDriver) Packages(ctx context.Context, schema string) ([]dbcore.PackageInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.PackagesQuery("dm", s)
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
// first, then the body, each only when that half exists.
func (d *DMDriver) PackageDefinition(ctx context.Context, schema, name string) (string, error) {
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
		return "", fmt.Errorf("dm: package %q not found", name)
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

// packageSource reads one half of a package's source.
func (d *DMDriver) packageSource(ctx context.Context, schema, name, part string) (string, error) {
	q, a, err := sqltext.PackageDefinitionQuery("dm", schema, name, part)
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

// Synonyms lists the synonyms visible from the schema.
func (d *DMDriver) Synonyms(ctx context.Context, schema string) ([]dbcore.SynonymInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.SynonymsQuery("dm", s)
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
