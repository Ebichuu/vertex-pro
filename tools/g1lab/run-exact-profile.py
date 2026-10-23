#!/usr/bin/env python3
"""Run the exact QB_504 campaign only in an ephemeral Linux Actions runner.

No production address, credential or filesystem is accepted. qB and qui have
only internal Docker networks; the harness can reach qui but not the qB network.
"""
import base64
import datetime as dt
import hashlib
import http.cookiejar
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

QB_SHA = '57f2ce4fe3abcc0c0e8476c4d0fcc00e9657a48fbcfecdad8b4120498060173a'
QUI_COMMIT = 'bfe94b5e4d63f213af747fab39719d47aae99d22'
MODES = ('none', 'response_loss', 'timeout', 'transport_202', 'transport_204', 'transport_409', 'external_kill')
ROOT = Path(__file__).resolve().parents[2]

def require(condition, message):
    if not condition:
        raise RuntimeError(message)

def command(*args, env=None):
    result = subprocess.run(args, cwd=ROOT, env=env, text=True, capture_output=True, timeout=600)
    compose_lifecycle = args[:2] == ('docker', 'compose') and len(args) > 4 and args[4] in ('up', 'down', 'ps', 'port')
    if result.returncode and args[0] == 'docker' and (args[1] in ('build', 'pull') or compose_lifecycle):
        print(result.stderr[-6000:], file=sys.stderr)
    # Never dump subprocess logs: qB startup logs and proxy errors can contain credentials.
    operation = ('docker compose ' + args[4]) if compose_lifecycle else (' '.join(args[:2]) if args[0] == 'docker' else args[0])
    require(result.returncode == 0, f'{operation} operation failed (exit {result.returncode})')
    return result.stdout.strip()

def digest(path):
    return 'sha256:' + hashlib.sha256(Path(path).read_bytes()).hexdigest()

def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + '\n', encoding='utf-8')

def now():
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace('+00:00', 'Z')

def clean_revision():
    require(not command('git', 'status', '--porcelain=v1', '--untracked-files=all'), 'A clean commit is required')
    revision = command('git', 'rev-parse', 'HEAD')
    require(revision == os.environ['GITHUB_SHA'], 'Checkout does not match workflow revision')
    return revision

def compile_binary(package, destination, revision, output, goos='linux'):
    env = dict(os.environ, CGO_ENABLED='0', GOOS=goos, GOARCH='amd64', GOTOOLCHAIN='local')
    command('go', 'build', '-buildvcs=true', '-trimpath', '-o', str(destination), package, env=env)
    info = command('go', 'version', '-m', str(destination))
    for fact in (f'vcs.revision={revision}', 'vcs.modified=false', f'GOOS={goos}', 'GOARCH=amd64', 'CGO_ENABLED=0'):
        require(re.search(r'(?m)^\s*build\s+' + re.escape(fact) + r'$', info), 'Binary build identity mismatch')
    (output / (destination.name + '.buildinfo.txt')).write_text(info + '\n')
    require(clean_revision() == revision, 'Build modified source checkout')

def validate_probe(report, mode):
    require(report['status'] == 'PASS' and report['profile'] == 'QB_504', 'Probe did not pass QB_504')
    safety = report['safety']
    for field in ('explicit_isolated_lab_opt_in', 'baseline_empty', 'start_inactive', 'trackerless', 'delete_only_added_identity', 'lab_returned_empty', 'secrets_redacted'):
        require(safety[field] is True, 'Probe safety invariant failed: ' + field)
    require(safety['production_services_called'] is False, 'Probe reported production access')
    if mode in MODES:
        fault = report['fault']
        require(fault['mode'] == mode and fault['sender_calls'] == 1 and fault['automatic_redispatches'] == 0, 'Dispatch count mismatch')
        if mode == 'external_kill':
            for field in ('child_terminated', 'child_exited_abruptly', 'durable_marker_before_send', 'startup_recovery_promoted_unknown', 'replay_blocked'):
                require(fault[field] is True, 'External kill invariant failed')
            require(report['transaction']['response_fact_recorded'] is False, 'Killed child fabricated response')
            require(fault['reconciler_outcome'] == 'RECONCILED_CONFIRMED', 'Killed child did not reconcile')
        else:
            require(report['transaction']['durable_marker_before_send'] is True and report['transaction']['response_fact_recorded'] is True, 'Dispatch boundary absent')
        expected_final = 'HASH_SEEN' if mode in ('none', 'transport_202') else ('RECONCILED_CONFIRMED' if mode in ('response_loss', 'external_kill') else 'RECONCILED_NO_EFFECT')
        require(report['transaction']['attempt_final'] == expected_final, 'Unexpected attempt final state')
        require(report['placement_gate']['profile_state'] == 'EXPECTED', 'Probe bypassed production profile gate')
        require(report['transaction']['waiter_closed'] is True, 'Waiter did not close')

def main():
    require(os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_OS') == 'Linux', 'Only Linux GitHub Actions runners may execute this lab')
    output = Path(os.environ['VERTEX_G1_OUTPUT']).resolve()
    temp_root = Path(os.environ['RUNNER_TEMP']).resolve()
    require(output.is_relative_to(temp_root) and not output.exists(), 'Evidence must be a new runner temporary directory')
    output.mkdir(mode=0o700)
    revision = clean_revision()
    expected_fingerprint = os.environ['VERTEX_G1_QB_BUILD_FINGERPRINT']
    require(re.fullmatch(r'sha256:[0-9a-f]{64}', expected_fingerprint), 'Production qB build fingerprint is required')
    qui_image = os.environ['VERTEX_G1_QUI_IMAGE']
    require(re.fullmatch(r'ghcr\.io/(?:autobrr|ebichuu)/qui@sha256:[0-9a-f]{64}', qui_image), 'qui must use an upstream or project immutable GHCR image reference')
    binary_url = os.environ['VERTEX_G1_QB_BINARY_URL']
    parsed = urllib.parse.urlparse(binary_url)
    allowed_paths = ('/userdocs/qbittorrent-nox-static/releases/download/', '/Ebichuu/vertex-pro/releases/download/')
    require(parsed.scheme == 'https' and parsed.hostname == 'github.com' and not parsed.username and not parsed.query and not parsed.fragment and parsed.path.startswith(allowed_paths), 'qB must come from the upstream or project release repository')
    work = Path(tempfile.mkdtemp(prefix='vertex-g1-', dir=temp_root))
    qb_dir = work / 'qb'
    harness_dir = work / 'harness'
    qb_dir.mkdir()
    harness_dir.mkdir()
    qb_binary = qb_dir / 'qbittorrent-nox'
    urllib.request.urlretrieve(binary_url, qb_binary)
    require(digest(qb_binary) == 'sha256:' + QB_SHA, 'Downloaded qB differs from the production binary')
    compile_binary('./tools/qutransactionprobe', harness_dir / 'probe', revision, output)
    compile_binary('./tools/profileverification', work / 'profileverification', revision, output)
    compile_binary('./cmd/vertex-pro', output / 'vertex-pro.exe', revision, output, goos='windows')
    compile_binary('./tools/profileverification', output / 'profileverification.exe', revision, output, goos='windows')
    # Keep the exact clean-built recorder with the campaign for later offline replay.
    (output / 'profileverification').write_bytes((work / 'profileverification').read_bytes())
    project = 'vertex-g1-' + secrets.token_hex(5)
    qb_tag, harness_tag = project + '-qb', project + '-harness'
    command('docker', 'build', '-f', str(ROOT / 'deploy/compose/lab/images/qb504-exact/Dockerfile'), '-t', qb_tag, str(qb_dir))
    command('docker', 'build', '-f', str(ROOT / 'deploy/compose/lab/images/harness/Dockerfile'), '-t', harness_tag, str(harness_dir))
    command('docker', 'pull', qui_image)
    image_info = json.loads(command('docker', 'image', 'inspect', qui_image))[0]
    require(image_info.get('Config', {}).get('Labels', {}).get('org.opencontainers.image.revision') == QUI_COMMIT, 'qui image does not attest the required commit')
    harness_digest = command('docker', 'image', 'inspect', harness_tag, '--format', '{{.Id}}')
    compose_file = work / 'compose.json'
    hardening = {'read_only': True, 'security_opt': ['no-new-privileges:true'], 'cap_drop': ['ALL']}
    compose = {'name': project, 'services': {
        'qb': {**hardening, 'image': qb_tag, 'tmpfs': ['/config:uid=10001,gid=10001', '/tmp:uid=10001,gid=10001'], 'networks': ['qb']},
        'qui': {**hardening, 'image': qui_image, 'user': '10001:10001', 'entrypoint': ['/bin/sh', '-ec'], 'command': ['/usr/local/bin/qui generate-config --config-dir /config; exec /usr/local/bin/qui serve --config-dir /config --data-dir /config'], 'environment': {'QUI__HOST': '0.0.0.0', 'QUI__PORT': '7476'}, 'tmpfs': ['/config:uid=10001,gid=10001', '/tmp:uid=10001,gid=10001'], 'networks': ['qb', 'proxy']},
        'harness': {**hardening, 'image': harness_tag, 'tmpfs': ['/tmp:uid=10001,gid=10001'], 'networks': ['proxy'], 'profiles': ['probe']}
    }, 'networks': {'qb': {'internal': True}, 'proxy': {'internal': True}}}
    write_json(compose_file, compose)
    def dc(*args, env=None):
        return command('docker', 'compose', '-f', str(compose_file), *args, env=env)
    write_json(output / 'identity.json', {'vertex_commit': revision, 'qui_commit': QUI_COMMIT, 'qui_image': qui_image, 'qb_binary_url': binary_url, 'qb_binary_digest': 'sha256:' + QB_SHA, 'qb_build_fingerprint': expected_fingerprint, 'harness_image_digest': harness_digest, 'networks_internal': True})
    try:
        dc('up', '-d', '--no-build', 'qb', 'qui')
        # A process can exit after compose reports Started. Record only state and
        # pre-auth qui startup messages, never qB logs or container environment.
        qui_id = dc('ps', '-a', '-q', 'qui')
        for _ in range(15):
            state = json.loads(command('docker', 'inspect', qui_id))[0]
            internal_ip = state['NetworkSettings']['Networks'].get(project + '_proxy', {}).get('IPAddress')
            if state['State']['Running'] and internal_ip:
                break
            if not state['State']['Running']:
                startup = subprocess.run(['docker', 'logs', qui_id], text=True, capture_output=True, timeout=15)
                messages = '\n'.join(line for line in (startup.stdout + startup.stderr).splitlines()
                                     if not re.search(r'password|secret|token|api.?key|session', line, re.I))
                print('qui startup: ' + messages[-5000:], file=sys.stderr)
                raise RuntimeError('Isolated qui exited before authentication; exit=' + str(state['State']['ExitCode']))
            time.sleep(1)
        else:
            raise RuntimeError('Isolated qui has no address on the campaign proxy network')
        # Internal-only bridges do not publish ports on current Docker engines.
        # The ephemeral Linux host reaches qui's bridge address for setup, while
        # both container networks remain internal and no host port is exposed.
        require(ipaddress.ip_address(internal_ip).is_private, 'Lab bridge address is not private')
        base = 'http://' + internal_ip + ':7476'
        jar = http.cookiejar.CookieJar()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
        def request(path, body=None):
            data = None if body is None else json.dumps(body).encode()
            req = urllib.request.Request(base + path, data, {'Content-Type': 'application/json', 'Accept': 'application/json', 'Origin': base})
            with opener.open(req, timeout=15) as response:
                payload = response.read()
                return json.loads(payload) if payload else None
        for _ in range(90):
            try:
                request('/health')
                break
            except (OSError, ValueError):
                time.sleep(1)
        else:
            raise RuntimeError('Isolated qui failed to become healthy')
        password = secrets.token_urlsafe(32)
        request('/api/auth/setup', {'username': 'admin', 'password': password})
        request('/api/auth/login', {'username': 'admin', 'password': password})
        version = request('/api/version')['version']
        require(QUI_COMMIT[:8] in version, 'Running qui version does not match required commit')
        qb_id = dc('ps', '-q', 'qb')
        qb_password = None
        for _ in range(45):
            logs = command('docker', 'logs', qb_id)
            match = re.search(r'temporary password is provided for this session:\s+(\S+)', logs, re.I)
            if match:
                qb_password = match.group(1)
                break
            time.sleep(1)
        require(qb_password, 'Isolated qB temporary password not available')
        instance = request('/api/instances/', {'name': 'g1-exact-qb504', 'host': 'http://qb:8080', 'username': 'admin', 'password': qb_password})
        for _ in range(60):
            if any(row['id'] == instance['id'] and row.get('connected') for row in request('/api/instances/')):
                break
            time.sleep(1)
        else:
            raise RuntimeError('Isolated qui did not connect to qB')
        proxy_key = request('/api/client-api-keys/', {'clientName': 'vertex-g1', 'instanceId': instance['id']})['key']
        # Secrets are passed through environment names, not process command arguments.
        probe_env = dict(os.environ, VERTEX_PRO_QUI_URL='http://qui:7476', VERTEX_PRO_QUI_PROXY_KEY=proxy_key, VERTEX_PRO_G1_ISOLATED_LAB='I_CONFIRM_EMPTY_ISOLATED_LAB')
        def probe(mode):
            print('Running isolated probe: ' + mode, flush=True)
            env = dict(probe_env, VERTEX_PRO_G1_FAULT_MODE=mode, VERTEX_PRO_FIXTURE_DIR='/tmp/fixtures')
            keys = ['VERTEX_PRO_QUI_URL', 'VERTEX_PRO_QUI_PROXY_KEY', 'VERTEX_PRO_G1_ISOLATED_LAB', 'VERTEX_PRO_G1_FAULT_MODE', 'VERTEX_PRO_FIXTURE_DIR']
            result = json.loads(dc('run', '--rm', '--no-deps', *[part for key in keys for part in ('-e', key)], 'harness', env=env))
            require(proxy_key not in json.dumps(result), 'Probe output contains a lab credential')
            validate_probe(result, mode)
            return result
        fixture = probe('fixture_capture')
        write_json(output / 'golden-capture.json', fixture)
        fixture_paths = {}
        for item in fixture['payloads']:
            kind = item['kind']
            require(kind in ('add', 'sync', 'tracker', 'transfer_info'), 'Unknown fixture kind')
            require(kind not in fixture_paths, 'Duplicate fixture kind')
            path = output / ('golden-' + kind + '.json')
            path.write_bytes(base64.b64decode(item['base64'], validate=True))
            require(digest(path) == 'sha256:' + item['sha256'] and fixture['fixtures'][kind] == item['sha256'], 'Golden fixture digest mismatch')
            fixture_paths[kind] = path
        require(len(fixture_paths) == 4, 'Incomplete golden fixture set')
        campaign = 'g1-' + os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT']
        for repetition in range(1, 4):
            print(f'Beginning repetition {repetition}/3', flush=True)
            started = now()
            protocol = probe('protocol_suite')
            require(protocol['cases'] and all(case['status'] == 'PASS' for case in protocol['cases']), 'Required protocol cases failed')
            detected = protocol['detected_profile']
            require(detected['QBAppVersion'] == '5.0.4' and detected['WebAPIVersion'] == '2.11.2' and detected['LibtorrentVersion'] == '1.2.20.0', 'Lab profile differs from production')
            require(detected['QBBuildFingerprint'] == expected_fingerprint, 'Lab full build fingerprint differs from production')
            cases = {mode: probe(mode) for mode in MODES}
            finished = now()
            report = {'repetition': repetition, 'protocol': protocol, 'modes': cases, 'golden_capture_digest': digest(output / 'golden-capture.json'), 'golden_fixtures': {kind: digest(path) for kind, path in fixture_paths.items()}, 'started_at': started, 'finished_at': finished}
            report_path = output / f'report-{repetition}.json'
            write_json(report_path, report)
            expires = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(days=30)).replace(microsecond=0).isoformat().replace('+00:00', 'Z')
            count = len(protocol['cases']) + len(MODES)
            run = dict(run_id=f'{campaign}-qb504-{repetition}', campaign_id=campaign, repetition=repetition, required_repetitions=3, instance_id='lab-qb504', profile_id='QB_504', source_kind='REAL_PROXY_LAB', qb_app_version=detected['QBAppVersion'], qb_build_fingerprint=detected['QBBuildFingerprint'], qb_artifact_digest='sha256:' + QB_SHA, webapi_version=detected['WebAPIVersion'], libtorrent_version=detected['LibtorrentVersion'], qui_version=version, qui_image_digest=qui_image.split('@')[1], proxy_route_fingerprint=protocol['proxy_route_fingerprint'], harness_image_digest=harness_digest, vertex_commit=revision, vertex_worktree_clean=True, test_suite_revision='g1-profile-verification-v1', verification_report_digest=digest(report_path), required_case_count=count, passed_case_count=count, lab_isolated=True, lab_returned_empty=True, production_services_called=False, secrets_redacted=True, kill9_case_passed=True, automatic_redispatches=0, verification_state='VERIFIED', started_at=started, finished_at=finished, verified_at=finished, created_at=now(), expires_at=expires)
            for kind, field in (('add', 'add_fixture_digest'), ('sync', 'sync_fixture_digest'), ('tracker', 'tracker_fixture_digest'), ('transfer_info', 'transfer_info_fixture_digest')):
                run[field] = digest(fixture_paths[kind])
            run_path = output / f'run-{repetition}.json'
            write_json(run_path, run)
            args = ['-database', str(output / 'campaign.db'), '-run', str(run_path), '-report', str(report_path), '-binding-output', str(output / 'binding-qb504.json')]
            for kind, flag in (('add', '-add-fixture'), ('sync', '-sync-fixture'), ('tracker', '-tracker-fixture'), ('transfer_info', '-transfer-info-fixture')):
                args += [flag, str(fixture_paths[kind])]
            recorded = json.loads(command(str(work / 'profileverification'), *args))
            write_json(output / f'recorded-{repetition}.json', recorded)
            require(recorded['persisted_state'] == 'VERIFIED', 'Recorder did not accept evidence')
            if repetition == 3:
                require(recorded['profile_state'] == 'VERIFIED' and recorded['binding_written'], 'Three repetitions did not activate exact profile')
            else:
                require(recorded['profile_state'] == 'EXPECTED' and not recorded['binding_written'], 'Profile activated before three repetitions')
        with sqlite3.connect(output / 'campaign.db') as database:
            require(database.execute('PRAGMA integrity_check').fetchone()[0] == 'ok', 'Campaign database integrity failed')
            rows = database.execute('SELECT repetition, verification_state FROM profile_verification_runs ORDER BY repetition').fetchall()
            require(rows == [(1, 'VERIFIED'), (2, 'VERIFIED'), (3, 'VERIFIED')], 'Campaign database rows differ from accepted repetitions')
            database.execute('PRAGMA wal_checkpoint(TRUNCATE)')
        database.close()
    finally:
        dc('down', '--volumes')
    require(not dc('ps', '-a', '-q'), 'Lab containers remain after cleanup')
    write_json(output / 'cleanup.json', {'status': 'PASS', 'project': project, 'containers_remaining': 0})
    write_json(output / 'manifest.json', {'status': 'PASS', 'scope': 'QB_504 exact production artifact only; not a three-profile G1 gate', 'vertex_commit': revision, 'artifacts': {p.name: digest(p) for p in sorted(output.iterdir()) if p.is_file()}})
    print('Exact QB_504 P0 x 3 passed; redacted campaign evidence archived.')

if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Never print exception URLs or subprocess output (which may contain proxy keys).
        if isinstance(error, RuntimeError):
            print(str(error), file=sys.stderr)
        else:
            print('Campaign failed: ' + type(error).__name__, file=sys.stderr)
        sys.exit(1)
