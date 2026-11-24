package sqltext

// programobjects_test.go — the recipes for packages, jobs and synonyms.

import (
	"strings"
	"testing"
)

func TestPackagesQueryShapes(t *testing.T) {
	for _, db := range []string{"oracle", "dm"} {
		q, args, err := PackagesQuery(db, "app")
		if err != nil {
			t.Fatalf("%s: %v", db, err)
		}
		if len(args) != 0 {
			t.Errorf("%s: the recipe inlines the schema, args = %v", db, args)
		}
		for _, want := range []string{"ALL_OBJECTS", "PACKAGE BODY", "GROUP BY OBJECT_NAME"} {
			if !strings.Contains(strings.ToUpper(q), want) {
				t.Errorf("%s: query is missing %q:\n%s", db, want, q)
			}
		}
		// The status must be INVALID when either half is, which the CASE
		// over the invalid flag expresses; a MAX over the status text would
		// depend on lexical ordering.
		if !strings.Contains(strings.ToUpper(q), "STATUS = 'INVALID'") {
			t.Errorf("%s: the status is not an explicit invalid check:\n%s", db, q)
		}
	}
	if _, _, err := PackagesQuery("postgres", "app"); err == nil {
		t.Fatal("postgres reported a package recipe")
	}
}

func TestPackageDefinitionQueryShapes(t *testing.T) {
	q, _, err := PackageDefinitionQuery("oracle", "app", "pkg", "PACKAGE BODY")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ALL_SOURCE", "ORDER BY LINE", "'PACKAGE BODY'"} {
		if !strings.Contains(strings.ToUpper(q), strings.ToUpper(want)) {
			t.Errorf("query is missing %q:\n%s", want, q)
		}
	}
	if _, _, err := PackageDefinitionQuery("mysql", "app", "pkg", "PACKAGE"); err == nil {
		t.Fatal("mysql reported a package source recipe")
	}
}

func TestJobsQueryOnlyOracle(t *testing.T) {
	q, _, err := JobsQuery("oracle", "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ALL_SCHEDULER_JOBS", "NEXT_RUN_DATE"} {
		if !strings.Contains(strings.ToUpper(q), want) {
			t.Errorf("query is missing %q:\n%s", want, q)
		}
	}
	// DM8 has no job catalog on a measured build, so it must NOT get a recipe:
	// a query that cannot run would surface as a broken tree node.
	if _, _, err := JobsQuery("dm", "app"); err == nil {
		t.Fatal("dm reported a job recipe despite having no job catalog")
	}
}

func TestSynonymsQueryShapes(t *testing.T) {
	for _, db := range []string{"oracle", "dm"} {
		q, _, err := SynonymsQuery(db, "app")
		if err != nil {
			t.Fatalf("%s: %v", db, err)
		}
		for _, want := range []string{"ALL_SYNONYMS", "PUBLIC", "LEFT JOIN ALL_OBJECTS"} {
			if !strings.Contains(strings.ToUpper(q), want) {
				t.Errorf("%s: query is missing %q:\n%s", db, want, q)
			}
		}
	}
	if _, _, err := SynonymsQuery("mssql", "app"); err == nil {
		t.Fatal("mssql reported a synonym recipe")
	}
}
