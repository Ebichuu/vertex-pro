package g2oracle

import "testing"

func TestP0SuiteIsCompleteAndUnique(t *testing.T) {
	if err := SuiteValidate(P0Scenarios()); err != nil {
		t.Fatal(err)
	}
}

func TestRerunProducesStableReportAndDigest(t *testing.T) {
	scenario := P0Scenarios()[4]
	observation := observationFor(scenario)
	report, err := Rerun(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusPass || report.EventDigest == "" || report.EventDigest != report.RerunDigest {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunFailsClosedOnSQLAssertionAndEventMismatch(t *testing.T) {
	scenario := P0Scenarios()[1]
	observation := observationFor(scenario)
	observation.Assertions[0].Observed = "TRUSTED"
	observation.Events[0] = "capacity.epoch_published"
	report, err := Run(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusFail || len(report.AssertionFailures) != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunRejectsIncompleteScenarioAndCardinalityMismatch(t *testing.T) {
	scenario := P0Scenarios()[0]
	scenario.Assertions = nil
	if _, err := Run(scenario, observationFor(P0Scenarios()[0])); err == nil {
		t.Fatal("incomplete scenario error = nil")
	}
	scenario = P0Scenarios()[0]
	observation := observationFor(scenario)
	observation.Assertions = observation.Assertions[:1]
	if _, err := Run(scenario, observation); err == nil {
		t.Fatal("cardinality error = nil")
	}
}

func observationFor(scenario Scenario) Observation {
	assertions := make([]SQLAssertion, len(scenario.Assertions))
	copy(assertions, scenario.Assertions)
	for index := range assertions {
		assertions[index].Observed = assertions[index].Expected
	}
	events := append([]string(nil), scenario.ExpectedEvents...)
	return Observation{Assertions: assertions, Events: events}
}
