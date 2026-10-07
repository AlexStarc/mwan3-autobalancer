/* Offline tests run the real LuCI view with minimal DOM/form/RPC substitutes. */
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../openwrt/luci/view/network/mwan3-autobalancer.js'), 'utf8');
const language = process.argv[2] || 'en';
assert.ok(['en', 'ru'].includes(language));
const po = fs.readFileSync(path.join(__dirname, '../openwrt/luci/po/ru/mwan3-autobalancer.po'), 'utf8');
const catalog = {};
for (const entry of po.split(/\n\n/)) {
	const id = entry.match(/^msgid (".*")$/m), value = entry.match(/^msgstr (".*")$/m);
	if (id && value && JSON.parse(id[1])) {
		const key = JSON.parse(id[1]);
		assert.equal(catalog[key], undefined, 'duplicate catalog key');
		catalog[key] = JSON.parse(value[1]);
		assert.ok(catalog[key], key);
		assert.equal((key.match(/%s/g) || []).length, (catalog[key].match(/%s/g) || []).length, 'format placeholders: ' + key);
	}
}
const _ = message => language === 'ru' ? catalog[message] ?? message : message;
String.prototype.format = function(...args) { let index = 0; return String(this).replace(/%s/g, () => String(args[index++])); };
const msgids = new Set(Array.from(source.matchAll(/_\('([^'\n]*)'\)/g), match => match[1]));
const menuTitle = JSON.parse(fs.readFileSync(path.join(__dirname, '../openwrt/luci/menu.d/luci-app-mwan3-autobalancer.json'), 'utf8'))['admin/network/mwan3-autobalancer'].title;
assert.equal(menuTitle, 'Autobalancing');
msgids.add(menuTitle);
assert.deepEqual(Object.keys(catalog).sort(), Array.from(msgids).sort(), 'all native msgids have complete Russian translations and no stale entries');
assert.doesNotMatch(source, /[А-Яа-яЁё]/, 'English source must not hardcode Russian');
assert.equal(_('Autobalancing'), language === 'ru' ? 'Автобалансировка' : 'Autobalancing');
class Element {
	constructor(tag, attrs = {}, children = []) { this.tag = tag; this.attrs = attrs; this.children = []; this.disabled = false; this.style = {}; this.isConnected = false; this.rect = { top: 20, bottom: 100, width: 390, height: 80 }; this.append(children); }
	append(children) { (Array.isArray(children) ? children : [children]).forEach(c => { if (c !== '') this.children.push(c); }); }
	appendChild(child) { this.children.push(child); }
	querySelectorAll() { return []; }
	setAttribute(key, value) { this.attrs[key] = value; }
	getBoundingClientRect() { return this.rect; }
	text() { return this.children.map(c => c instanceof Element ? c.text() : String(c)).join(' '); }
}
const E = (tag, attrs, children) => new Element(tag, attrs, children);
let sections = [{ '.name': 'main', '.type': 'main', mode: 'observe', policy: 'balanced', probe_bytes: '33554432', max_probe_bytes: '134217728', daily_budget_bytes: '268435456', interval_seconds: '21600' }];
const mwan = [{ '.name': 'balanced', '.type': 'policy', use_member: ['wan_a', 'wan_b'] }, { '.name': 'wan_a', '.type': 'member', interface: 'wan' }, { '.name': 'wan_b', '.type': 'member', interface: 'wan2' }];
const activity = { uciWrites: 0, rpcCalls: 0, renders: 0, resets: 0 };
const uci = {
	load: async () => {}, unload: () => {},
	sections: (config, type) => (config === 'mwan3' ? mwan : sections).filter(s => s['.type'] === type),
	get: (config, id, key) => (config === 'mwan3' ? mwan : sections).find(s => s['.name'] === id)?.[key],
	set: (config, id, key, value) => { activity.uciWrites++; sections.find(s => s['.name'] === id)[key] = value; },
	unset: (config, id, key) => { activity.uciWrites++; delete sections.find(s => s['.name'] === id)[key]; }
};
// Stateful substitute for LuCI's public widget validation contract, not upstream
// implementation code. Native UI/validation retain errors until revalidation:
// https://github.com/openwrt/luci/blob/openwrt-24.10/modules/luci-base/htdocs/luci-static/resources/ui.js
// https://github.com/openwrt/luci/blob/openwrt-24.10/modules/luci-base/htdocs/luci-static/resources/validation.js
class ValidationWidget {
	constructor(option, id) {
		this.option = option; this.id = id; this.node = new Element('select');
		this.automatic = new Element('option', { value: 'automatic' });
		this.node.querySelectorAll = () => [this.automatic];
		const classes = new Set();
		this.node.classList = { add: c => classes.add(c), remove: c => classes.delete(c), contains: c => classes.has(c) };
		this.valid = true; this.error = '';
	}
	getValue() { return this.option.formvalue(this.id); }
	isValid() { return this.valid; }
	getValidationError() { return this.error; }
	triggerValidation() {
		const previous = this.valid;
		const result = this.option.validate ? this.option.validate(this.id, this.getValue()) : true;
		this.valid = result === true; this.error = this.valid ? '' : String(result);
		this.node.classList[this.valid ? 'remove' : 'add']('cbi-input-invalid');
		if (this.valid) delete this.node.attrs['data-tooltip'];
		else this.node.attrs['data-tooltip'] = this.error;
		return previous !== this.valid;
	}
}
class Option {
	constructor(section, name, label, description) { this.section = section; this.name = name; this.label = label; this.description = description; this.values = {}; this.input = {}; this.widgets = {}; }
	value(key, value) { this.values[key] = value || key; }
	formvalue(id) { return this.input[id] ?? (this.cfgvalue ? this.cfgvalue(id) : uci.get('mwan3_autobalancer', id, this.name)); }
	getUIElement(id) { return this.widgets[id] ||= new ValidationWidget(this, id); }
}
class Section {
	constructor(map, type, id) { this.map = map; this.type = type; this.id = id; this.options = []; }
	option(klass, name, label, description) { const o = new Option(this, name, label, description); this.options.push(o); return o; }
}
class Map {
	constructor(config, title, description) { this.sections = []; this.title = title; this.description = description; }
	section(klass, id, type) { const s = new Section(this, klass === 'named' ? type : id, klass === 'named' ? id : null); this.sections.push(s); return s; }
	lookupOption(name, id) { return this.sections.filter(s => s.id === id || s.type === sections.find(x => x['.name'] === id)?.['.type']).flatMap(s => s.options.filter(o => o.name === name)); }
	render() { activity.renders++; return Promise.resolve(new Element('form')); }
	reset() { activity.resets++; return Promise.resolve(); }
}
const methods = {};
const rpc = { declare: ({ method }) => methods[method] = async () => { activity.rpcCalls++; return {}; } };
const form = { Map, NamedSection: 'named', TypedSection: 'typed', Flag: 'flag', ListValue: 'list', Value: 'value' };
const view = { extend: obj => obj };
const notifications = [];
const ui = { createHandlerFn: () => () => {}, addNotification: (title, message) => notifications.push(message.text()) };
const dom = { content: (el, children) => { el.children = []; el.append(children); } };
const poll = { add: () => {} };
const header = new Element('header'); header.position = 'fixed'; header.rect = { top: 0, bottom: 40, width: 390, height: 40 };
const fill = new Element('div', { class: 'fill' }); fill.parentElement = header; fill.rect = { top: 0, bottom: 147, width: 390, height: 147 };
const container = new Element('div', { class: 'container' }); container.parentElement = fill; container.rect = { top: 0, bottom: 147, width: 390, height: 147 };
const menu = new Element('ul', { id: 'topmenu' }); menu.position = 'relative'; menu.parentElement = container; menu.rect = { top: 40, bottom: 120, width: 390, height: 80 };
const submenu = new Element('ul'); submenu.position = 'absolute'; submenu.parentElement = menu; submenu.rect = { top: 120, bottom: 700, width: 390, height: 580 }; menu.appendChild(submenu);
const document = { querySelectorAll: () => [header, menu] };
const window = { scrollY: 0, getComputedStyle: el => ({ position: el.position || 'static' }) };
const page = new Function('view', 'rpc', 'form', 'uci', 'poll', 'ui', 'dom', 'E', 'document', 'window', 'requestAnimationFrame', '_', source)(view, rpc, form, uci, poll, ui, dom, E, document, window, callback => callback(), _);
(async () => {
	const root = await page.render([{ apply_ready: false, apply_unavailable_reason: 'watchdog missing', channels: [] }]);
	assert.equal(root.attrs.class, 'mwan3-ab-view');
	assert.match(root.children[0].text(), /@media\(max-width:640px\)/);
	assert.match(root.children[0].text(), /content:attr\(data-title\)/);
	assert.match(root.children[0].text(), /\.mwan3-ab-view \.cbi-value-title/);
	assert.match(root.children[0].text(), /\.mwan3-ab-view \.cbi-value-title\{[^}]*text-align:left!important;[^}]*white-space:normal!important;[^}]*overflow-wrap:anywhere/);
	root.isConnected = true; page.headerSpacing();
	assert.equal(root.style.paddingTop, '139px', 'clear the147px permanent header containers without measuring the700px dropdown');
	window.scrollY = 500; root.rect.top = -480; page.headerSpacing();
	assert.equal(root.style.paddingTop, '139px', 'polling while scrolled must not grow the top gap');
	window.scrollY = 0; root.rect.top = 20;
	header.position = 'static'; page.headerSpacing();
	assert.equal(root.style.paddingTop, '0px', 'ordinary document headers need no extra clearance');
	header.position = 'fixed'; page.headerSpacing();
	const option = (id, name) => page.map.lookupOption(name, id)[0];
	assert.equal(page.map.title, _('Autobalancing'));
	assert.equal(option('main', 'probe_url').label, _('Test download URL'));
	assert.equal(option('main', 'interval_seconds').label, _('Interval, hours'));
	assert.equal(option('main', 'probe_bytes').label, _('%s, MiB').format(_('Size of one measurement')));
	assert.equal(page.probeButton.text(), _('Measure')); assert.equal(page.restoreButton.text(), _('Restore stock policy'));
	assert.match(option('main', 'mode').validate('main', 'automatic'), /watchdog missing/);
	assert.equal(option('main', 'mode').validate('main', 'observe'), true);
	const modeOption = option('main', 'mode'), widget = modeOption.getUIElement('main');
	const pending = { mode: 'automatic', probe_url: 'https://example.test/unsaved', interval_seconds: '9', daily_budget_bytes: '768' };
	Object.entries(pending).forEach(([name, value]) => { option('main', name).input.main = value; });
	const configBefore = JSON.stringify(sections), activityBefore = { ...activity }, mapBefore = page.map;
	const missingReason = 'watchdog readiness: open /var/run/mwan3-autobalancer/watchdog.ready: no such file or directory';
	page.refresh({ mode: 'observe', apply_ready: false, apply_unavailable_reason: missingReason });
	widget.triggerValidation(); // Seed a cached error as native input validation does.
	assert.equal(widget.isValid(), false);
	assert.equal(widget.getValidationError(), _('Automatic mode unavailable: %s').format(missingReason));
	assert.equal(widget.node.attrs['data-tooltip'], widget.getValidationError());
	assert.equal(widget.node.classList.contains('cbi-input-invalid'), true);
	assert.equal(widget.automatic.disabled, true); assert.equal(widget.automatic.attrs['aria-disabled'], 'true');
	page.refresh({ mode: 'observe', apply_ready: true, apply_unavailable_reason: '' });
	assert.equal(modeOption.validate('main', 'automatic'), true);
	assert.equal(widget.isValid(), true, 'fresh readiness must clear the cached invalid state');
	assert.equal(widget.getValidationError(), ''); assert.equal(widget.node.attrs['data-tooltip'], undefined); assert.equal(widget.node.classList.contains('cbi-input-invalid'), false);
	assert.equal(widget.automatic.disabled, false); assert.equal(widget.automatic.attrs['aria-disabled'], 'false');
	const newReason = 'watchdog readiness record expired';
	page.refresh({ mode: 'observe', apply_ready: false, apply_unavailable_reason: newReason });
	assert.equal(widget.isValid(), false); assert.equal(widget.node.classList.contains('cbi-input-invalid'), true);
	assert.equal(widget.getValidationError(), _('Automatic mode unavailable: %s').format(newReason));
	assert.equal(widget.node.attrs['data-tooltip'], widget.getValidationError());
	assert.equal(widget.automatic.disabled, true); assert.equal(widget.automatic.attrs['aria-disabled'], 'true');
	const changedReason = 'watchdog recorded process unavailable';
	page.refresh({ mode: 'observe', apply_ready: false, apply_unavailable_reason: changedReason });
	assert.equal(widget.getValidationError(), _('Automatic mode unavailable: %s').format(changedReason));
	assert.equal(widget.node.attrs['data-tooltip'], widget.getValidationError());
	assert.equal(modeOption.getUIElement('main'), widget, 'refresh must retain the native widget');
	assert.equal(page.map, mapBefore); assert.equal(widget.getValue(), 'automatic');
	Object.entries(pending).forEach(([name, value]) => assert.equal(option('main', name).formvalue('main'), value, name));
	assert.equal(JSON.stringify(sections), configBefore); assert.deepEqual(activity, activityBefore, 'status refresh must not reset/render forms, write UCI, call RPC or probe');
	Object.keys(pending).forEach(name => { delete option('main', name).input.main; });
	const interval = option('main', 'interval_seconds');
	assert.equal(interval.cfgvalue('main'), '6');
	for (const hours of ['6', '6.5', '0.5', '0.016666666666666666', '168', '6,5']) assert.equal(interval.validate('main', hours), true, hours);
	for (const hours of ['', '0', '169', 'NaN', 'Infinity', '-1', '1e1', '0.0001', '6.0001']) assert.notEqual(interval.validate('main', hours), true, hours);
	interval.write('main', '6.5'); assert.equal(uci.get('mwan3_autobalancer', 'main', 'interval_seconds'), '23400');
	interval.write('main', '6,5'); assert.equal(uci.get('mwan3_autobalancer', 'main', 'interval_seconds'), '23400');
	for (const seconds of [60, 61, 21601, 604800]) {
		uci.set('mwan3_autobalancer', 'main', 'interval_seconds', String(seconds));
		assert.equal(interval.validate('main', interval.cfgvalue('main')), true);
		interval.write('main', interval.cfgvalue('main')); assert.equal(uci.get('mwan3_autobalancer', 'main', 'interval_seconds'), String(seconds));
	}
	interval.write('main', '6');
	assert.equal(option('main', 'probe_url').validate('main', 'https://example.test/{bytes}?n={bytes}'), true);
	for (const url of ['ftp://example.test/x', 'https://a:b@example.test/', 'https://example.test/#x', 'http://{bytes}.example.test']) assert.notEqual(option('main', 'probe_url').validate('main', url), true);
	page.report = { lease_active: true, policy: 'balanced' };
	assert.equal(option('main', 'policy').validate('main', 'other'), _('Restore the previous policy using the button below first'));
	assert.equal(option('main', 'policy').validate('main', 'balanced'), true);
	assert.notEqual(option('main', 'policy').validate('main', 'x;touch-pwn'), true);
	assert.equal(option('main', 'probe_bytes').validate('main', '32'), true);
	assert.notEqual(option('main', 'probe_bytes').validate('main', '129'), true);
	assert.notEqual(option('main', 'daily_budget_bytes').validate('main', '1'), true);
	assert.notEqual(option('main', 'probe_bytes').validate('main', 'NaN'), true);
	sections.push({ '.name': 'override', '.type': 'wan', interface: 'wan', probe_bytes: '', daily_budget_bytes: '' });
	const wanInterval = option('override', 'interval_seconds');
	assert.equal(wanInterval.cfgvalue('override'), ''); assert.equal(wanInterval.validate('override', ''), true);
	wanInterval.write('override', '12'); assert.equal(uci.get('mwan3_autobalancer', 'override', 'interval_seconds'), '43200');
	wanInterval.write('override', ''); assert.equal(uci.get('mwan3_autobalancer', 'override', 'interval_seconds'), undefined);
	assert.equal(option('override', 'probe_bytes').validate('override', ''), true);
	option('override', 'probe_bytes').write('override', '16');
	assert.equal(uci.get('mwan3_autobalancer', 'override', 'probe_bytes'), '16777216');
	option('override', 'probe_bytes').write('override', '');
	assert.equal(uci.get('mwan3_autobalancer', 'override', 'probe_bytes'), undefined);
	sections.push({ '.name': 'duplicate', '.type': 'wan', interface: 'wan' });
	assert.equal(option('override', 'interface').validate('override', 'wan'), _('An override for this WAN already exists'));
	assert.notEqual(option('override', 'interface').validate('override', 'unknown'), true);
	page.refresh({ apply_ready: true, busy: false, channels: Array.from({ length: 6 }, (_, i) => ({ interface: `wan${i}`, online: true, enabled: true, metric: 1, baseline_weight: 500, proposed_weight: i === 0 ? 1000 : 0, applied_weight: 0, speed_mbps: null, age_seconds: null, probe_state: i === 1 ? 'historical' : i === 2 ? 'measuring' : i === 3 ? 'failed' : 'unknown', budget_used_bytes: 0, budget_limit_bytes: 268435456 })) });
	const text = page.summary.text();
	assert.match(text, /wan5/); assert.match(text, /16.7%/); assert.ok(text.includes(_('Stale'))); assert.ok(text.includes(_('Measuring'))); assert.ok(text.includes(_('Unknown'))); assert.ok(text.includes(_('Error')));
	assert.ok(!text.includes(_('%s Mbit/s').format('0.00'))); assert.match(text, /100\.0%/);
	function findTable(element) { if (element.tag === 'table') return element; for (const child of element.children) if (child instanceof Element) { const table = findTable(child); if (table) return table; } }
	function rows() { return findTable(page.summary).children.slice(1).map(row => row.children.map(cell => cell.text())); }
	const channel = (iface, extra = {}) => ({ interface: iface, enabled: true, online: true, metric: 1, baseline_weight: 1000, proposed_weight: 0, applied_weight: 0, speed_mbps: null, age_seconds: null, probe_state: 'unknown', budget_used_bytes: 0, budget_limit_bytes: 268435456, ...extra });
	page.refresh({ apply_ready: true, phase: 'calibrating', channels: [channel('disabled', { enabled: false }), channel('modem1', { proposed_weight: 200, applied_weight: 333, phase: 'calibrating' }), channel('modem2', { proposed_weight: 300, applied_weight: 333, phase: 'maintenance' }), channel('petra', { proposed_weight: 500, applied_weight: 333, phase: 'holding' }), channel('offline', { online: false, metric: 0 }), channel('reserve', { metric: 2, phase: 'disabled' })] });
	let tableRows = rows();
	const labels = ['WAN', _('Availability'), _('Phase'), _('Measurement'), _('Speed / age'), _('Stock share'), _('Proposed'), _('Applied'), _('Next measurement'), _('Daily reservation / limit')];
	findTable(page.summary).children.slice(1).forEach(row => {
		assert.match(row.attrs.class, /mwan3-ab-channel/);
		assert.deepEqual(row.children.map(cell => cell.attrs['data-title']), labels, 'every mobile card metric retains a label');
	});
	assert.equal(tableRows[0][1], _('Disabled')); assert.equal(tableRows[0][5], '0.0%');
	assert.equal(tableRows[1][5], '33.3%'); assert.equal(tableRows[2][5], '33.3%'); assert.equal(tableRows[3][5], '33.3%');
	assert.equal(tableRows[4][5], '0.0%'); assert.equal(tableRows[5][5], '0.0%');
	assert.equal(tableRows[1][6], '20.0%'); assert.equal(tableRows[2][6], '30.0%'); assert.equal(tableRows[3][6], '50.0%');
	assert.equal(tableRows[1][7], '33.3%'); assert.equal(tableRows[0][7], '0.0%');
	assert.ok(page.summary.text().includes(_('Policy: %s; mode: %s; phase: %s; next measurement: %s').format('—', _('Observe'), _('Calibration'), '—')));
	assert.equal(tableRows[1][2], _('Calibration')); assert.equal(tableRows[2][2], _('Periodic measurements')); assert.equal(tableRows[3][2], _('Holding shares')); assert.equal(tableRows[5][2], _('Measurements disabled'));
	page.refresh({ phase: 'error', channels: [channel('spent', { budget_used_bytes: 268435456, probe_error: 'daily probe budget exhausted', probe_state: 'failed', phase: 'error' }), channel('small', { budget_used_bytes: 268435456 - 16 * 1048576 }), channel('inflight', { budget_used_bytes: 268435456, probe_state: 'measuring', probe_error: 'previous failure' }), channel('retry', { budget_used_bytes: 268435456 - 32 * 1048576, probe_error: 'daily probe budget exhausted' })] });
	tableRows = rows();
	assert.equal(tableRows[0][3], _('Daily reservation exhausted')); assert.equal(tableRows[0][2], _('Error'));
	assert.equal(tableRows[1][3], _('Reservation is smaller than one measurement'));
	assert.equal(tableRows[2][3], _('Measuring'));
	assert.equal(tableRows[3][3], _('Insufficient reservation for a retry'));
	assert.ok(tableRows[0][9].includes(_('Daily reservation exhausted'))); assert.ok(page.summary.text().includes(_('Policy: %s; mode: %s; phase: %s; next measurement: %s').format('—', _('Observe'), _('Error'), '—')));
	// Logical-WAN overrides set the minimum future reservation independently.
	sections = sections.filter(s => s['.type'] !== 'wan'); sections.push({ '.name': 'small_override', '.type': 'wan', interface: 'wan2', probe_bytes: String(16 * 1048576) });
	page.refresh({ phase: 'maintenance', channels: [channel('wan2', { budget_used_bytes: 268435456 - 16 * 1048576 }), channel('inherited', { budget_used_bytes: 268435456 - 16 * 1048576 })] });
	assert.equal(rows()[0][3], _('Unknown')); assert.equal(rows()[1][3], _('Reservation is smaller than one measurement'));
	page.refresh({ channels: [channel('seconds', { speed_mbps: 12.5, age_seconds: 25 }), channel('minutes', { speed_mbps: 8, age_seconds: 120 }), channel('hours', { speed_mbps: 5, age_seconds: 5400 })] });
	assert.equal(rows()[0][3], _('Current'));
	assert.equal(rows()[0][4], _('%s Mbit/s').format('12.50') + ' / ' + _('%s s').format(25));
	assert.ok(rows()[1][4].endsWith(_('%s min').format(2)));
	assert.ok(rows()[2][4].endsWith(_('%s h').format('1.5')));
	assert.ok(rows()[0][9].includes(_('%s / %s MiB').format('0.0', '256.0')));
	assert.equal(option('main', 'probe_bytes').validate('main', '0.1'), _('Enter a size from 0.25 to %s MiB, in whole bytes').format(1024));
	await page.action(methods.probe);
	assert.equal(notifications.pop(), _('Measurement cycle queued; weights will not be applied.'));
	await page.action(methods.rollback);
	assert.equal(notifications.pop(), _('Stock policy restored; observe mode enabled.'));
	page.refresh({ error: 'status_unavailable' });
	assert.ok(page.summary.text().includes(_('Controller unavailable. Stock policy restoration remains available independently.')));
	assert.equal(page.probeButton.disabled, true); assert.equal(page.restoreButton.disabled, false);
	console.log(language + ' LuCI fixtures passed: mobile labels/header clearance, exact hours conversion, active shares/phases/quotas, safe settings, fallback Restore');
	if (!process.argv[2]) process.stdout.write(require('node:child_process').execFileSync(process.execPath, [__filename, 'ru'], { encoding: 'utf8' }));
})().catch(err => { console.error(err); process.exit(1); });
