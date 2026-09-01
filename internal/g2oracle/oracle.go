package g2oracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	StatusPass = "PASS"
	StatusFail = "FAIL"
)

type SQLAssertion struct {
	Name     string `json:"name"`
	Query    string `json:"query"`
	Expected string `json:"expected"`
	Observed string `json:"observed"`
}

type Scenario struct {
	ID             string         `json:"id"`
	Priority       string         `json:"priority"`
	InitialData    []string       `json:"initial_data"`
	FaultPoints    []string       `json:"fault_points"`
	Operations     []string       `json:"operations"`
	Assertions     []SQLAssertion `json:"assertions"`
	ExpectedEvents []string       `json:"expected_events"`
}

type Observation struct {
	Assertions []SQLAssertion `json:"assertions"`
	Events     []string       `json:"events"`
}

type Report struct {
	ScenarioID        string   `json:"scenario_id"`
	Status            string   `json:"status"`
	AssertionFailures []string `json:"assertion_failures"`
	EventDigest       string   `json:"event_digest"`
	RerunDigest       string   `json:"rerun_digest"`
}

func P0Scenarios() []Scenario {
	return []Scenario{
		{ID: "G2-CAP-08-ROLLBACK", Priority: "P0", InitialData: []string{"trusted_epoch:1", "job:READY"}, FaultPoints: []string{"after_reservation_insert"}, Operations: []string{"reserve_atomic", "inject_sql_failure", "rollback"}, Assertions: []SQLAssertion{{Name: "no_orphan_reservation", Query: "SELECT COUNT(*) FROM capacity_reservations", Expected: "0"}, {Name: "no_current_attempt", Query: "SELECT current_attempt_id FROM race_jobs", Expected: "NULL"}}, ExpectedEvents: []string{"reservation.rollback"}},
		{ID: "G2-CAP-13-UNKNOWN", Priority: "P0", InitialData: []string{"reconciling_epoch:1", "binding:UNKNOWN"}, FaultPoints: []string{"unknown_amount_left"}, Operations: []string{"reconcile"}, Assertions: []SQLAssertion{{Name: "capacity_remains_reconciling", Query: "SELECT capacity_state FROM capacity_epochs", Expected: "RECONCILING"}, {Name: "no_new_epoch", Query: "SELECT COUNT(*) FROM capacity_epochs", Expected: "1"}}, ExpectedEvents: []string{"capacity.reconcile_blocked"}},
		{ID: "G2-CAP-11-COMPLETED", Priority: "P0", InitialData: []string{"active_reservation:100", "amount_left:KNOWN/0"}, FaultPoints: []string{}, Operations: []string{"final_recheck", "release_completed"}, Assertions: []SQLAssertion{{Name: "terminal_reason", Query: "SELECT release_reason FROM capacity_reservations", Expected: "TERMINAL_COMPLETED"}, {Name: "reflection_equals_reserved", Query: "SELECT reflected_physical_bytes FROM capacity_reservations", Expected: "100"}}, ExpectedEvents: []string{"reservation.completed"}},
		{ID: "G2-CAP-12-REMOVED", Priority: "P0", InitialData: []string{"active_reservation:100", "qb_owner:RESERVATION"}, FaultPoints: []string{"authoritative_qb_delete"}, Operations: []string{"fresh_physical_free", "release_removed"}, Assertions: []SQLAssertion{{Name: "terminal_reason", Query: "SELECT release_reason FROM capacity_reservations", Expected: "TERMINAL_QB_REMOVED"}, {Name: "no_reflection_fabrication", Query: "SELECT reflected_physical_bytes FROM capacity_reservations", Expected: "0"}}, ExpectedEvents: []string{"reservation.qb_removed"}},
		{ID: "G2-CAP-13-STABLE", Priority: "P0", InitialData: []string{"reconciling_epoch:1", "complete_baseline"}, FaultPoints: []string{}, Operations: []string{"stable_snapshot_1", "stable_snapshot_2", "reconcile"}, Assertions: []SQLAssertion{{Name: "new_epoch_trusted", Query: "SELECT capacity_state FROM capacity_epochs ORDER BY allocation_epoch DESC LIMIT 1", Expected: "TRUSTED"}, {Name: "epoch_incremented", Query: "SELECT MAX(allocation_epoch) FROM capacity_epochs", Expected: "2"}}, ExpectedEvents: []string{"capacity.reconcile_started", "capacity.epoch_published"}},
	}
}

func Run(scenario Scenario, observation Observation) (Report, error) {
	if err := validateScenario(scenario); err != nil {
		return Report{}, err
	}
	if len(observation.Assertions) != len(scenario.Assertions) {
		return Report{}, errors.New("ASSERTION_CARDINALITY_MISMATCH")
	}
	failures := make([]string, 0)
	for index, expected := range scenario.Assertions {
		observed := observation.Assertions[index]
		if expected.Name != observed.Name || expected.Query != observed.Query || expected.Expected != observed.Observed {
			failures = append(failures, expected.Name)
		}
	}
	for index, expected := range scenario.ExpectedEvents {
		if index >= len(observation.Events) || observation.Events[index] != expected {
			failures = append(failures, "event:"+expected)
		}
	}
	eventDigest, err := digest(observation.Events)
	if err != nil {
		return Report{}, err
	}
	status := StatusPass
	if len(failures) > 0 {
		status = StatusFail
	}
	return Report{ScenarioID: scenario.ID, Status: status, AssertionFailures: failures, EventDigest: eventDigest}, nil
}

func Rerun(scenario Scenario, observation Observation) (Report, error) {
	first, err := Run(scenario, observation)
	if err != nil {
		return Report{}, err
	}
	second, err := Run(scenario, observation)
	if err != nil {
		return Report{}, err
	}
	if first.EventDigest != second.EventDigest || first.Status != second.Status || strings.Join(first.AssertionFailures, "\x00") != strings.Join(second.AssertionFailures, "\x00") {
		return Report{}, errors.New("NON_DETERMINISTIC_RERUN")
	}
	first.RerunDigest = second.EventDigest
	return first, nil
}

func SuiteValidate(scenarios []Scenario) error {
	seen := make(map[string]struct{}, len(scenarios))
	for _, scenario := range scenarios {
		if err := validateScenario(scenario); err != nil {
			return err
		}
		if _, exists := seen[scenario.ID]; exists {
			return fmt.Errorf("DUPLICATE_SCENARIO:%s", scenario.ID)
		}
		seen[scenario.ID] = struct{}{}
	}
	if len(scenarios) == 0 {
		return errors.New("EMPTY_SCENARIO_SUITE")
	}
	return nil
}

func validateScenario(scenario Scenario) error {
	if strings.TrimSpace(scenario.ID) == "" || strings.TrimSpace(scenario.Priority) == "" {
		return errors.New("SCENARIO_ID_PRIORITY_REQUIRED")
	}
	if len(scenario.InitialData) == 0 || len(scenario.Operations) == 0 || len(scenario.Assertions) == 0 || len(scenario.ExpectedEvents) == 0 {
		return errors.New("SCENARIO_CONTRACT_INCOMPLETE")
	}
	for _, assertion := range scenario.Assertions {
		if strings.TrimSpace(assertion.Name) == "" || strings.TrimSpace(assertion.Query) == "" {
			return errors.New("SQL_ASSERTION_INCOMPLETE")
		}
	}
	return nil
}

func digest(events []string) (string, error) {
	data, err := json.Marshal(events)
	if err != nil {
		return "", fmt.Errorf("marshal events: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
