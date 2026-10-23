import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('exact_profile', Path(__file__).with_name('run-exact-profile.py'))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


def report():
    return {
        'status': 'PASS', 'profile': 'QB_504',
        'safety': {**{field: True for field in ('explicit_isolated_lab_opt_in', 'baseline_empty', 'start_inactive', 'trackerless', 'delete_only_added_identity', 'lab_returned_empty', 'secrets_redacted')}, 'production_services_called': False},
        'fault': {'mode': 'none', 'sender_calls': 1, 'automatic_redispatches': 0},
        'placement_gate': {'profile_state': 'EXPECTED'},
        'transaction': {'durable_marker_before_send': True, 'response_fact_recorded': True, 'attempt_final': 'HASH_SEEN', 'waiter_closed': True},
    }


class EvidenceValidation(unittest.TestCase):
    def test_unsafe_or_incomplete_evidence_cannot_be_recorded(self):
        valid = report()
        runner.validate_probe(valid, 'none')
        for section, key, value in (
            ('safety', 'production_services_called', True),
            ('safety', 'lab_returned_empty', False),
            ('fault', 'automatic_redispatches', 1),
            ('fault', 'sender_calls', 2),
            ('transaction', 'durable_marker_before_send', False),
            ('transaction', 'attempt_final', 'UNKNOWN'),
            ('placement_gate', 'profile_state', 'VERIFIED'),
        ):
            with self.subTest(field=key):
                invalid = copy.deepcopy(valid)
                invalid[section][key] = value
                with self.assertRaises(RuntimeError):
                    runner.validate_probe(invalid, 'none')

    def test_killed_process_cannot_claim_response_fact(self):
        killed = report()
        killed['fault'].update(mode='external_kill', child_terminated=True, child_exited_abruptly=True, durable_marker_before_send=True, startup_recovery_promoted_unknown=True, replay_blocked=True, reconciler_outcome='RECONCILED_CONFIRMED')
        killed['transaction'].update(response_fact_recorded=False, attempt_final='RECONCILED_CONFIRMED')
        runner.validate_probe(killed, 'external_kill')
        killed['transaction']['response_fact_recorded'] = True
        with self.assertRaises(RuntimeError):
            runner.validate_probe(killed, 'external_kill')


if __name__ == '__main__':
    unittest.main()
