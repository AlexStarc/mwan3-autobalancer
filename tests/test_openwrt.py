#!/usr/bin/env python3
"""Shell integration fixtures. All absolute target paths are redirected to a temp tree.

These tests never contact a router, run a real firewall command, or alter host UCI.
The installed-stock-library substitute tests orchestration and readback; it is
not a claim of hardware or vendor integration coverage.
"""
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]


def executable(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(0o755)


JSON_TOOL = r'''#!/usr/bin/env python3
import json, os, sys
try:
 d=json.loads(os.environ.get('FIXTURE_JSON',''))
 if sys.argv[1]=='load': sys.exit(0)
 key=sys.argv[2]; v=d.get(key)
 if sys.argv[1]=='type':
  print('boolean' if isinstance(v,bool) else 'int' if isinstance(v,int) else 'double' if isinstance(v,float) else 'string' if isinstance(v,str) else '')
 elif v is not None: print(int(v) if isinstance(v,bool) else v)
except Exception: sys.exit(1)
'''
FIREWALL = r'''#!/usr/bin/env python3
import json, os, shlex, sys
from pathlib import Path
base=Path(os.environ['FIXTURE_ROOT']); name=Path(sys.argv[0]).name; args=sys.argv[1:]
family='6' if name.startswith('ip6') else '4'; path=base/('firewall'+family+'.json')
state=json.loads(path.read_text()) if path.exists() else {}
if 'restore' in name:
 data=sys.stdin.read()
 if os.environ.get('FIXTURE_IPT_FAIL')=='1': sys.exit(7)
 if '--test' in args: sys.exit(0)
 for line in data.splitlines():
  a=shlex.split(line)
  if not a or a[0] in ('*mangle','COMMIT'): continue
  command,chain=a[:2]
  if command=='-N': state[chain]=[]
  elif command=='-F': state[chain]=[]
  elif command=='-A': state.setdefault(chain,[]).append(a[2:])
  elif command=='-I': state.setdefault(chain,[]).insert(0,a[2:])
 if os.environ.get('FIXTURE_WRONG_READBACK')=='1': state['mwan3_policy_balanced']=[]
 path.write_text(json.dumps(state)); sys.exit(0)
if '-S' not in args: sys.exit(2)
i=args.index('-S'); requested=args[i+1] if len(args)>i+1 else None
for chain,rules in state.items():
 if requested and chain!=requested: continue
 print('-N '+chain)
 for rule in rules:
  r=rule[:]
  if '--probability' in r:
   i=r.index('--probability')+1;r[i]=str(float(r[i])+0.00000000019)
  output=[]
  for index,value in enumerate(r):
   output.append('"'+value+'"' if index>0 and r[index-1]=='--comment' else value)
  print('-A '+chain+' '+' '.join(output))
if requested and requested not in state: sys.exit(1)
'''


class Fixture:
    def __init__(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='mwan3-shell-', dir='/tmp')
        self.root = Path(self.tmp.name)
        self.lib = self.root / 'libexec'
        self.run = self.root / 'run/mwan3-autobalancer'
        self.bin = self.root / 'bin'
        self.proc = self.root / 'proc'
        for p in [self.lib, self.run, self.bin, self.proc, self.root / 'lock', self.root / 'persistent']:
            p.mkdir(parents=True)
        (self.proc / 'uptime').write_text('100.00 0.00\n')
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'], FIXTURE_ROOT=str(self.root))
        replacements = {
            '/usr/libexec/mwan3-autobalancer': str(self.lib),
            '/usr/bin/mwan3-autobalancer': str(self.bin / 'controller'),
            '/usr/share/libubox/jshn.sh': str(self.root / 'jshn.sh'),
            '/lib/functions/network.sh': str(self.root / 'network.sh'),
            '/lib/functions.sh': str(self.root / 'functions.sh'),
            '/lib/mwan3/mwan3.sh': str(self.root / 'mwan3.sh'),
            '/etc/init.d/mwan3-autobalancer': str(self.bin / 'init'),
            '/etc/mwan3-autobalancer': str(self.root / 'persistent'),
            '/var/run': str(self.root / 'run'),
            '/var/lock': str(self.root / 'lock'),
            '/proc/': str(self.proc) + '/',
        }
        def rewrite(text):
            for original, target in replacements.items():
                text = text.replace(original, target)
            return text
        for p in (ROOT / 'openwrt/libexec').iterdir():
            executable(self.lib / p.name, rewrite(p.read_text()))
        executable(self.root / 'rpc', rewrite((ROOT / 'openwrt/rpcd/mwan3.autobalancer').read_text()))
        executable(self.root / 'service', rewrite((ROOT / 'openwrt/init.d/mwan3-autobalancer').read_text()))
        executable(self.bin / 'json-tool', JSON_TOOL)
        executable(self.root / 'jshn.sh', '''json_load() { FIXTURE_JSON=$1; export FIXTURE_JSON; json-tool load; }
json_get_var() { local v; v=$(json-tool var "$2"); export "$1=$v"; }
json_get_type() { local v; v=$(json-tool type "$2"); export "$1=$v"; }
''')
        executable(self.bin / 'flock', '''#!/bin/sh
[ "$FIXTURE_LOCK_BUSY" != 1 ] || exit 1
if [ -n "$FIXTURE_REPLACE_LEASE" ]; then cp "$FIXTURE_REPLACE_LEASE" "$FIXTURE_ROOT/run/mwan3-autobalancer/lease.json"; fi
if [ -n "$FIXTURE_REPLACE_HEARTBEAT" ]; then cp "$FIXTURE_REPLACE_HEARTBEAT" "$FIXTURE_ROOT/run/mwan3-autobalancer/heartbeat.$FIXTURE_OWNER.json"; fi
exit 0
''')
        executable(self.bin / 'uci', '#!/bin/sh\n[ "$2" != get ] || echo balanced\nexit 0\n')
        executable(self.bin / 'logger', '#!/bin/sh\nexit 0\n')
        executable(self.bin / 'init', '#!/bin/sh\nexit 0\n')
        executable(self.bin / 'ubus', '#!/bin/sh\nif [ -n "$FIXTURE_UBUS" ]; then printf \'%s\\n\' "$FIXTURE_UBUS"; else echo \'{}\'; fi\n')
        executable(self.bin / 'jsonfilter', r'''#!/usr/bin/env python3
import json, re, sys
try:
 data=json.load(sys.stdin)
 for part in re.findall(r'\["([^"]+)"\]|\.([A-Za-z_][A-Za-z0-9_]*)',sys.argv[-1]): data=data[part[0] or part[1]]
 print(json.dumps(data) if isinstance(data,(dict,list)) else data)
except Exception: sys.exit(1)
''')
        # Default bounded substitute runs immediately; native bounded behavior is
        # exercised separately where a POSIX setsid is available.
        executable(self.lib / 'bounded', '#!/bin/sh\nshift\nexec "$@"\n')
        for name in ['iptables', 'ip6tables', 'iptables-restore', 'ip6tables-restore']:
            executable(self.bin / name, FIREWALL)
        executable(self.root / 'network.sh', 'network_get_device() { export "$1=eth0"; }\n')
        executable(self.root / 'functions.sh', '''config_load() { :; }
config_get() {
 local value="$4"
 case "$2:$3" in
 balanced:TYPE) value=policy;;
 a:TYPE) value=member;;
 a:interface) value=wan;;
 a:weight) value=3;;
 a:metric) value=1;;
 wan:family) value=ipv4;;
 esac
 export "$1=$value"
}
config_list_foreach() { "$3" a; }
''')
        executable(self.root / 'mwan3.sh', '''NO_IPV6=1
mwan3_init() { MMX_MASK=0x3f00; }
mwan3_create_policies_iptables() {
 $IPT4 -S >/dev/null
 # Deliberately swallow errors, like the installed stock helper.
 printf '*mangle\\n-N mwan3_policy_%s\\n-F mwan3_policy_%s\\n-A mwan3_policy_%s -m mark --mark 0x0/0x3f00 -m comment --comment "wan 3 3" -j MARK --set-xmark 0x100/0x3f00\\nCOMMIT\\n' "$1" "$1" "$1" | $IPT4R || :
}
''')

    def close(self):
        self.tmp.cleanup()

    def call(self, program, *args, input=None, env=None):
        return subprocess.run([str(program), *args], input=input, text=True, capture_output=True, env=dict(self.env, **(env or {})), timeout=15)

    def shell(self, script, env=None):
        return self.call('/bin/sh', '-c', '. ' + shlex.quote(str(self.lib / 'common.sh')) + '\n' + script, env=env)

    def lease(self, **kwargs):
        pid = os.getpid()
        session = 'a' * 32
        lease = dict(policy='balanced', pid=pid, session=session, uptime=10, heartbeat_file=f'heartbeat.{pid}.json', restore_requested=False)
        lease.update(kwargs)
        (self.run / 'lease.json').write_text(json.dumps(lease))
        proc = self.proc / str(pid)
        proc.mkdir(exist_ok=True)
        (proc / 'cmdline').write_bytes((str(self.bin / 'controller') + '\0daemon\0').encode())
        (self.run / f'heartbeat.{pid}.json').write_text(json.dumps(dict(pid=pid, session=session, uptime=99)))
        return lease


class ShellTests(unittest.TestCase):
    def setUp(self): self.f = Fixture()
    def tearDown(self): self.f.close()

    def test_rpc_rejects_payloads_and_unknown_commands(self):
        session = '0123456789abcdef' * 2
        for payload in ['{"command":"once --apply"}', '[]', 'null', '{}\n{"cmd":"x"}', '{"policy":"balanced;touch x"}', 'x' * 4097,
                        json.dumps({'ubus_rpc_session': session, 'command': 'once --apply'}),
                        '{"ubus_rpc_session":"' + session + '","ubus_rpc_session":"' + session + '"}',
                        json.dumps({'ubus_rpc_session': 'a' * 31}),
                        json.dumps({'ubus_rpc_session': session + ' '}),
                        json.dumps({'ubus_rpc_session': 123}),
                        json.dumps({'ubus_rpc_session': session}) + '\n{}']:
            result = self.f.call(self.f.root / 'rpc', 'call', 'probe', input=payload)
            self.assertEqual(json.loads(result.stdout), {'error': 'invalid_request'}, payload)
        result = self.f.call(self.f.root / 'rpc', 'call', 'execute', input='{}')
        self.assertEqual(json.loads(result.stdout), {'error': 'unknown_method'})
        self.assertEqual(json.loads(self.f.call(self.f.root / 'rpc', 'list').stdout), dict(status={}, probe={}, rollback={}))

    def test_authenticated_luci_envelope_is_accepted_without_forwarding(self):
        executable(self.f.bin / 'controller', '''#!/bin/sh
[ "$#" = 1 ] && [ "$1" = status ] || exit 2
printf '{"mode":"observe","channels":[]}\\n'
''')
        session = '0123456789abcdef' * 2
        for payload in [json.dumps({'ubus_rpc_session': session}), '{\n "ubus_rpc_session" : "' + session + '"\n}', '{}']:
            result = self.f.call(self.f.root / 'rpc', 'call', 'status', input=payload)
            self.assertEqual(json.loads(result.stdout), {'mode': 'observe', 'channels': []}, result.stderr)

    def test_status_unavailable_and_duplicate_probe(self):
        result = self.f.call(self.f.root / 'rpc', 'call', 'status', input='{}')
        self.assertEqual(json.loads(result.stdout), {'error': 'status_unavailable'})
        executable(self.f.bin / 'controller', '''#!/bin/sh
[ "$#" = 1 ] && [ "$1" = probe ] || exit 2
printf '{"error":"probe cycle already queued or active"}\\n'
exit 1
''')
        result = self.f.call(self.f.root / 'rpc', 'call', 'probe', input='{}')
        self.assertEqual(json.loads(result.stdout)['error'], 'probe cycle already queued or active')

    def test_active_restore_failure_does_not_claim_success(self):
        self.f.lease()
        executable(self.f.bin / 'controller', '#!/bin/sh\ntouch "$FIXTURE_ROOT/rollback-called"\nprintf \'{"mode":"","last_error":""}\\n\'\nexit 1\n')
        self.f.env['FIXTURE_UBUS'] = json.dumps({'mwan3-autobalancer': {'instances': {'controller': {'pid': os.getpid()}}}})
        sock = socket.socket(socket.AF_UNIX)
        try:
            sock.bind(str(self.f.run / 'control.sock'))
            result = self.f.call(self.f.root / 'rpc', 'call', 'rollback', input='{}')
            self.assertEqual(json.loads(result.stdout), {'error': 'restore_failed'})
            self.assertTrue((self.f.root / 'rollback-called').exists())
            self.assertTrue((self.f.run / 'lease.json').exists())
        finally: sock.close()

    def test_lease_validation_fresh_stale_forced_and_dead(self):
        self.f.lease()
        self.assertEqual(self.f.shell('ab_lease && ab_expired').returncode, 1)
        self.f.lease(restore_requested=True)
        self.assertEqual(self.f.shell('ab_lease && ab_expired').returncode, 0)
        self.f.lease(pid=2147483647, heartbeat_file='heartbeat.2147483647.json')
        self.assertEqual(self.f.shell('ab_lease && ab_expired').returncode, 0)
        self.f.lease()
        heartbeat = self.f.run / f'heartbeat.{os.getpid()}.json'
        data = json.loads(heartbeat.read_text()); data['uptime'] = 9; heartbeat.write_text(json.dumps(data))
        self.assertEqual(self.f.shell('ab_lease && ab_expired').returncode, 0)
        heartbeat.unlink()
        self.assertEqual(self.f.shell('ab_lease && ab_expired').returncode, 0)

    def test_unsafe_lease_is_never_recovered(self):
        for change in [dict(policy='x;touch-pwn'), dict(pid='123'), dict(session='../x'), dict(heartbeat_file='../heartbeat.json'), dict(restore_requested='true')]:
            self.f.lease(**change)
            self.assertNotEqual(self.f.shell('ab_lease').returncode, 0)
            self.assertNotEqual(self.f.call(self.f.lib / 'restore', 'balanced').returncode, 0)
            self.assertTrue((self.f.run / 'lease.json').exists())

    def test_watchdog_rereads_lease_under_lock(self):
        lease = self.f.lease(restore_requested=True)
        replacement = self.f.root / 'replacement'
        replacement.write_text(json.dumps(dict(lease, session='b' * 32)))
        result = self.f.call(self.f.lib / 'restore', 'balanced', lease['session'], env={'FIXTURE_REPLACE_LEASE': str(replacement)})
        self.assertEqual(result.returncode, 3)
        self.assertFalse((self.f.root / 'firewall4.json').exists())
        # Same identity but heartbeat freshened while waiting: still no mutation.
        lease = self.f.lease()
        heartbeat = self.f.run / f'heartbeat.{os.getpid()}.json'
        data = json.loads(heartbeat.read_text()); data['uptime'] = 9; heartbeat.write_text(json.dumps(data))
        newbeat = self.f.root / 'newbeat'; newbeat.write_text(json.dumps(dict(data, uptime=99)))
        result = self.f.call(self.f.lib / 'restore', 'balanced', lease['session'], env={'FIXTURE_REPLACE_HEARTBEAT': str(newbeat), 'FIXTURE_OWNER': str(os.getpid())})
        self.assertEqual(result.returncode, 3)
        self.assertFalse((self.f.root / 'firewall4.json').exists())

    def test_stock_failures_and_wrong_readback_retain_lease(self):
        for fail in ['FIXTURE_IPT_FAIL', 'FIXTURE_WRONG_READBACK']:
            self.f.lease(restore_requested=True)
            result = self.f.call(self.f.lib / 'restore', 'balanced', env={fail: '1'})
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertTrue((self.f.run / 'lease.json').exists())

    def test_stock_recovery_success_and_budget_preservation(self):
        lease = self.f.lease(restore_requested=True)
        budgets = self.f.root / 'persistent/budgets'; budgets.write_text('{"wan":{"day":"2026-10-07","used":33554432}}')
        previous = budgets.read_bytes()
        result = self.f.call(self.f.lib / 'restore', 'balanced', lease['session'])
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.f.run / 'lease.json').exists())
        self.assertEqual(budgets.read_bytes(), previous)
        # Manual restore without any lease is allowed for our selected policy only.
        self.assertEqual(self.f.call(self.f.lib / 'restore', 'balanced').returncode, 0)
        self.assertNotEqual(self.f.call(self.f.lib / 'restore', 'other').returncode, 0)

    def test_fallback_rollback_without_go_or_service(self):
        self.f.lease(restore_requested=True)
        state = self.f.run / 'state.json'; state.write_text('{"apply_blocked":"fixture"}')
        budgets = self.f.root / 'persistent/budgets'; budgets.write_text('quota')
        result = self.f.call(self.f.root / 'rpc', 'call', 'rollback', input='{}')
        self.assertEqual(json.loads(result.stdout), {'mode': 'observe', 'restored': True}, result.stderr)
        self.assertFalse(state.exists()); self.assertEqual(budgets.read_text(), 'quota')

    def test_service_startup_failure_has_bounded_procd_instances(self):
        setup = '''
config_load() { :; }; config_get_bool() { export "$1=1"; }
procd_open_instance() { echo "instance $1"; }
procd_set_param() { echo "$*"; }
procd_close_instance() { :; }
start_service
'''
        result = self.f.call('/bin/sh', '-c', '. ' + shlex.quote(str(self.f.root / 'service')) + '\n' + setup)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.count('respawn 3600 10 3'), 2)
        self.assertEqual(result.stdout.count('term_timeout 5'), 2)
        self.assertIn('instance watchdog', result.stdout); self.assertIn('instance controller', result.stdout)
        self.assertIn('controller daemon', result.stdout)
        # No budgets file was created/reset merely by starting the service.
        self.assertFalse((self.f.root / 'persistent/budgets').exists())

    def test_reload_quiesces_before_restore_and_preserves_automatic_choice(self):
        log = self.f.root / 'stop-order'
        self.f.env['FIXTURE_ORDER'] = str(log)
        executable(self.f.lib / 'stop-instances', '#!/bin/sh\necho stop >> "$FIXTURE_ORDER"\n')
        executable(self.f.lib / 'restore', '#!/bin/sh\necho restore >> "$FIXTURE_ORDER"\n')
        script = '. ' + shlex.quote(str(self.f.root / 'service')) + '''
procd_kill() { echo kill >> "$FIXTURE_ORDER"; }
start() { echo start >> "$FIXTURE_ORDER"; }
reload_service
'''
        result = self.f.call('/bin/sh', '-c', script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(log.read_text().splitlines(), ['stop', 'restore', 'kill', 'start'])
        self.assertNotIn('mode=observe', (ROOT / 'openwrt/init.d/mwan3-autobalancer').read_text())

    def test_disabled_config_starts_no_instances(self):
        script = '. ' + shlex.quote(str(self.f.root / 'service')) + '''
config_load() { :; }; config_get_bool() { export "$1=0"; }
procd_open_instance() { echo unexpected; }
start_service
'''
        result = self.f.call('/bin/sh', '-c', script)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, '')

    def test_status_subprocess_cannot_mask_owner_death(self):
        self.f.lease()
        (self.f.proc / str(os.getpid()) / 'cmdline').write_bytes((str(self.f.bin / 'controller') + '\0status\0').encode())
        self.assertEqual(self.f.shell('ab_lease && ab_expired').returncode, 0)

    def test_busy_stock_lock_is_bounded_and_retains_lease(self):
        self.f.lease(restore_requested=True)
        executable(self.f.bin / 'sleep', '#!/bin/sh\nexit 0\n')
        result = self.f.call(self.f.lib / 'restore', 'balanced', env={'FIXTURE_LOCK_BUSY': '1'})
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.f.run / 'lease.json').exists())
        self.assertFalse((self.f.root / 'firewall4.json').exists())

    def test_watchdog_start_failure_and_cleanup_identity(self):
        (self.f.proc / 'uptime').write_text('bad\n')
        self.assertNotEqual(self.f.call(self.f.lib / 'watchdog').returncode, 0)
        ready = self.f.run / 'watchdog.ready'
        self.assertFalse(ready.exists())
        (self.f.proc / 'uptime').write_text('100.00 0.00\n')
        process = subprocess.Popen([str(self.f.lib / 'watchdog')], env=self.f.env)
        try:
            for _ in range(100):
                if ready.exists(): break
                time.sleep(.02)
            record = json.loads(ready.read_text())
            self.assertEqual(record['pid'], process.pid); self.assertEqual(record['uptime'], 100)
            ready.write_text('{"pid":2147483647,"uptime":100}')
            process.terminate(); process.wait(timeout=8)
            self.assertTrue(ready.exists(), 'old trap erased another watcher record')
        finally:
            if process.poll() is None: process.kill(); process.wait()

    def test_native_bounded_cleans_descendants_and_start_failure(self):
        # Python supplies setsid on macOS; production uses BusyBox setsid.
        executable(self.f.bin / 'setsid', '#!' + sys.executable + '\nimport os,sys\nos.setsid();os.execvp(sys.argv[1],sys.argv[1:])\n')
        original = (ROOT / 'openwrt/libexec/bounded').read_text().replace('/var/run', str(self.f.root / 'run'))
        executable(self.f.lib / 'bounded', original)
        result = self.f.call(self.f.lib / 'bounded', '2', '/nonexistent-command')
        self.assertNotEqual(result.returncode, 0)
        marker = self.f.root / 'late-write'
        result = self.f.call(self.f.lib / 'bounded', '1', '/bin/sh', '-c', '(sleep 3; touch "$1") & wait', 'child', str(marker))
        self.assertEqual(result.returncode, 124, result.stderr)
        time.sleep(3)
        self.assertFalse(marker.exists(), 'timed-out descendant escaped group cleanup')
        self.assertEqual(list((self.f.root / 'run').glob('mwan3-ab-bound.*')), [])


class GrammarTests(unittest.TestCase):
    def canonical(self, text, updates=False):
        return subprocess.run(['awk', '-v', 'chain=mwan3_policy_balanced', '-v', 'updates=' + ('1' if updates else '0'), '-f', str(ROOT / 'openwrt/libexec/leaf.awk')], input=text, text=True, capture_output=True)

    def test_kernel_probability_and_stock_replay(self):
        first = '-m mark --mark 0x0/0x3f00 -m comment --comment "a 1 1" -j MARK --set-xmark 0x100/0x3f00'
        second = '-m mark --mark 0x0/0x3f00 -m statistic --mode random --probability 0.500 -m comment --comment "b 1 2" -j MARK --set-xmark 0x200/0x3f00'
        expected = self.canonical('*mangle\n-N mwan3_policy_balanced\n-F mwan3_policy_balanced\n-A mwan3_policy_balanced ' + first + '\n-I mwan3_policy_balanced ' + second + '\nCOMMIT\n', True)
        actual = self.canonical('-N mwan3_policy_balanced\n-A mwan3_policy_balanced ' + second.replace('0.500', '0.50000000023') + '\n-A mwan3_policy_balanced ' + first + '\n')
        self.assertEqual(expected.returncode, 0, expected.stderr); self.assertEqual(expected.stdout, actual.stdout)

    def test_all_offline_fallbacks_and_foreign_rules(self):
        for fallback, mark in [('default','0x3f00'), ('blackhole','0x3d00'), ('unreachable','0x3e00')]:
            text = '-N mwan3_policy_balanced\n-A mwan3_policy_balanced -o eth0 -m mark --mark 0x0/0x3f00 -m comment --comment "out wan eth0" -j MARK --set-xmark 0x3f00/0x3f00\n-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -m comment --comment "' + fallback + '" -j MARK --set-xmark ' + mark + '/0x3f00\n'
            self.assertEqual(self.canonical(text).returncode, 0)
        for text in ['-A foreign -j ACCEPT\n', '-A mwan3_policy_balanced -j ACCEPT\n', '*filter\n', '-X mwan3_policy_balanced\n']:
            self.assertNotEqual(self.canonical(text, True).returncode, 0)


if __name__ == '__main__':
    unittest.main(verbosity=2)
