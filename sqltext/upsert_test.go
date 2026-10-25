package sqltext

import "testing"

func TestUpsertClausePerEngine(t *testing.T) {
	my, err := UpsertClause("mysql", []string{"id", "name"}, []string{"id"})
	if err != nil || my != " ON DUPLICATE KEY UPDATE `id`=VALUES(`id`), `name`=VALUES(`name`)" {
		t.Fatalf("mysql clause = %q (%v)", my, err)
	}
	pg, err := UpsertClause("postgresql", []string{"id", "name"}, []string{"id"})
	if err != nil || pg != ` ON CONFLICT ("id") DO UPDATE SET "id"=excluded."id", "name"=excluded."name"` {
		t.Fatalf("pg clause = %q (%v)", pg, err)
	}
	sq, err := UpsertClause("sqlite", []string{"id"}, []string{"id"})
	if err != nil || sq != ` ON CONFLICT ("id") DO UPDATE SET "id"=excluded."id"` {
		t.Fatalf("sqlite clause = %q (%v)", sq, err)
	}
}

func TestUpsertClauseRejectedOnMSSQLAndOracle(t *testing.T) {
	for _, typ := range []string{"sqlserver", "oracle"} {
		if _, err := UpsertClause(typ, []string{"id", "name"}, []string{"id"}); err == nil {
			t.Errorf("%s: upsert must be rejected, not silently ignored", typ)
		}
	}
}

func TestUpsertClauseRequiresKeysAndKnownColumns(t *testing.T) {
	if _, err := UpsertClause("postgresql", []string{"id"}, nil); err == nil {
		t.Fatal("missing keys must be rejected")
	}
	if _, err := UpsertClause("postgresql", []string{"id"}, []string{"nope"}); err == nil {
		t.Fatal("unknown key must be rejected")
	}
}

func TestUpsertClauseNormalizesEngineSpelling(t *testing.T) {
	// "postgres" (Go/dbcore Kind spelling) must behave like "postgresql".
	pg, err := UpsertClause("postgres", []string{"id", "name"}, []string{"id"})
	if err != nil || pg == "" {
		t.Fatalf("postgres spelling not normalized: %q (%v)", pg, err)
	}
}
