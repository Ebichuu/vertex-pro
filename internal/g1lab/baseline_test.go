package g1lab

import "testing"

func TestBuildReportKeepsFrozenProfilesExpected(t *testing.T) {
	r, err := BuildReport(NewBaseline())
	if err != nil {
		t.Fatal(err)
	}
	if r.BaselineDigest == "" || len(r.Results) != 3 {
		t.Fatalf("report=%+v", r)
	}
	for _, v := range r.Results {
		if v.Status != StatusExpected {
			t.Fatalf("result=%+v", v)
		}
	}
}
func TestValidateRejectsUnsafeOrIncompleteBaseline(t *testing.T) {
	b := NewBaseline()
	b.ExternalCalls = true
	if err := Validate(b); err == nil {
		t.Fatal("external calls accepted")
	}
	b = NewBaseline()
	b.Profiles = b.Profiles[:2]
	if err := Validate(b); err == nil {
		t.Fatal("missing profile accepted")
	}
	b = NewBaseline()
	b.Profiles[0].ID = b.Profiles[1].ID
	if err := Validate(b); err == nil {
		t.Fatal("duplicate profile accepted")
	}
}
