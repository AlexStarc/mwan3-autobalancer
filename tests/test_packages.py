#!/usr/bin/env python3
"""Parse actual package Makefiles from a fake SDK cwd and check their manifests.

The stubs expose the OpenWrt variable contracts; real ipk builds remain SDK CI's job.
"""
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class PackageTests(unittest.TestCase):
    def test_sdk_cwd_source_resolution_and_static_go_profile(self):
        with tempfile.TemporaryDirectory(prefix='mwan3-sdk-') as directory:
            sdk = Path(directory)
            for sub in ['include', 'feeds/packages/lang/golang']:
                (sdk / sub).mkdir(parents=True)
            (sdk / 'rules.mk').write_text('INCLUDE_DIR:=$(TOPDIR)/include\nBUILD_DIR:=$(TOPDIR)/build\nSOURCE_DATE_EPOCH:=123\n')
            (sdk / 'include/package.mk').write_text('''
define BuildPackage
$(info SOURCE=$(AB_SOURCE_DIR))
$(info TARGET=$(GO_PKG_BUILD_PKG))
$(info CGO=$(GO_PKG_TARGET_VARS))
$(info LINK=$(GO_PKG_DEFAULT_LDFLAGS))
$(info PIE=$(GO_PKG_ENABLE_PIE))
$(info INSTALL=$(call Package/$(1)/install,/target))
endef
.PHONY: fixture
fixture:
	@true
''')
            (sdk / 'feeds/packages/lang/golang/golang-package.mk').write_text('GO_PKG_TARGET_VARS=GOOS=linux GOARCH=arm64 CGO_ENABLED=1\nGO_PKG_DEFAULT_LDFLAGS=-linkmode external\nGO_PKG_ENABLE_PIE:=1\n')
            for name in ['mwan3-autobalancer', 'luci-app-mwan3-autobalancer']:
                result = subprocess.run(['make', '-f', str(ROOT / 'package' / name / 'Makefile'), 'TOPDIR=' + str(sdk), 'fixture'], cwd=sdk, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('SOURCE=' + str(ROOT), result.stdout)
                self.assertNotIn(str(sdk / 'openwrt'), result.stdout)
                if name == 'mwan3-autobalancer':
                    self.assertIn('CGO_ENABLED=0', result.stdout)
                    self.assertNotIn('CGO_ENABLED=1', result.stdout)
                    self.assertIn('-linkmode internal', result.stdout)
                    self.assertNotIn('-linkmode external', result.stdout)
                    self.assertIn('PIE=\n', result.stdout)
                    self.assertIn('github.com/AlexStarc/mwan3-autobalancer/cmd/mwan3-autobalancer', result.stdout)
                    self.assertIn(str(ROOT / 'openwrt/config/budgets'), result.stdout)
                    self.assertIn('-m0700 /target/etc/mwan3-autobalancer', result.stdout)
                    for helper in ['restore', 'watchdog', 'bounded', 'stock-restore', 'stock-iptables', 'stop-instances', 'fallback-rollback', 'leaf.awk', 'common.sh']:
                        self.assertIn(str(ROOT / 'openwrt/libexec' / helper), result.stdout)
                else:
                    for component in ['view/network/mwan3-autobalancer.js', 'menu.d/luci-app-mwan3-autobalancer.json', 'acl.d/luci-app-mwan3-autobalancer.json']:
                        self.assertIn(str(ROOT / 'openwrt/luci' / component), result.stdout)

    def test_acl_and_observation_defaults(self):
        acl = json.loads((ROOT / 'openwrt/luci/acl.d/luci-app-mwan3-autobalancer.json').read_text())['luci-app-mwan3-autobalancer']
        self.assertEqual(acl['read']['ubus'], {'mwan3.autobalancer': ['status']})
        self.assertEqual(acl['write']['ubus'], {'mwan3.autobalancer': ['probe', 'rollback']})
        self.assertEqual(acl['write']['uci'], ['mwan3_autobalancer'])
        config = (ROOT / 'openwrt/config/mwan3_autobalancer').read_text()
        self.assertIn("option mode 'observe'", config)
        self.assertIn("option probe_url ''", config)
        self.assertIn("option daily_budget_bytes '268435456'", config)
        self.assertEqual(json.loads((ROOT / 'openwrt/config/budgets').read_text()), {})
        core = (ROOT / 'package/mwan3-autobalancer/Makefile').read_text()
        self.assertIn('+iptables-zz-legacy', core)
        self.assertNotIn('+flock', core)
        self.assertNotIn('mwan3 restart', core)
        self.assertIn('stop || exit 1', core)
        self.assertNotIn('rm -f /etc/mwan3-autobalancer/budgets', core)


if __name__ == '__main__':
    unittest.main(verbosity=2)
