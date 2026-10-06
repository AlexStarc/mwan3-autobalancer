/* Offline tests run the real LuCI view with minimal DOM/form/RPC substitutes. */
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../openwrt/luci/view/network/mwan3-autobalancer.js'), 'utf8');
class Element {
	constructor(tag, attrs = {}, children = []) { this.tag = tag; this.attrs = attrs; this.children = []; this.disabled = false; this.style = {}; this.isConnected = false; this.rect = { top: 20, bottom: 100, width: 390, height: 80 }; this.append(children); }
	append(children) { (Array.isArray(children) ? children : [children]).forEach(c => { if (c !== '') this.children.push(c); }); }
	appendChild(child) { this.children.push(child); }
	querySelectorAll() { return []; }
	getBoundingClientRect() { return this.rect; }
	text() { return this.children.map(c => c instanceof Element ? c.text() : String(c)).join(' '); }
}
const E = (tag, attrs, children) => new Element(tag, attrs, children);
let sections = [{ '.name': 'main', '.type': 'main', mode: 'observe', policy: 'balanced', probe_bytes: '33554432', max_probe_bytes: '134217728', daily_budget_bytes: '268435456', interval_seconds: '21600' }];
const mwan = [{ '.name': 'balanced', '.type': 'policy', use_member: ['wan_a', 'wan_b'] }, { '.name': 'wan_a', '.type': 'member', interface: 'wan' }, { '.name': 'wan_b', '.type': 'member', interface: 'wan2' }];
const uci = {
	load: async () => {}, unload: () => {},
	sections: (config, type) => (config === 'mwan3' ? mwan : sections).filter(s => s['.type'] === type),
	get: (config, id, key) => (config === 'mwan3' ? mwan : sections).find(s => s['.name'] === id)?.[key],
	set: (config, id, key, value) => { sections.find(s => s['.name'] === id)[key] = value; },
	unset: (config, id, key) => { delete sections.find(s => s['.name'] === id)[key]; }
};
class Option {
	constructor(section, name) { this.section = section; this.name = name; this.values = {}; this.input = {}; }
	value(key, value) { this.values[key] = value || key; }
	formvalue(id) { return this.input[id] ?? (this.cfgvalue ? this.cfgvalue(id) : uci.get('mwan3_autobalancer', id, this.name)); }
	getUIElement() { return { node: new Element('select') }; }
}
class Section {
	constructor(map, type, id) { this.map = map; this.type = type; this.id = id; this.options = []; }
	option(klass, name) { const o = new Option(this, name); this.options.push(o); return o; }
}
class Map {
	constructor() { this.sections = []; }
	section(klass, id, type) { const s = new Section(this, klass === 'named' ? type : id, klass === 'named' ? id : null); this.sections.push(s); return s; }
	lookupOption(name, id) { return this.sections.filter(s => s.id === id || s.type === sections.find(x => x['.name'] === id)?.['.type']).flatMap(s => s.options.filter(o => o.name === name)); }
	render() { return Promise.resolve(new Element('form')); }
	reset() { return Promise.resolve(); }
}
const methods = {};
const rpc = { declare: ({ method }) => methods[method] = async () => ({}) };
const form = { Map, NamedSection: 'named', TypedSection: 'typed', Flag: 'flag', ListValue: 'list', Value: 'value' };
const view = { extend: obj => obj };
const ui = { createHandlerFn: () => () => {}, addNotification: () => {} };
const dom = { content: (el, children) => { el.children = []; el.append(children); } };
const poll = { add: () => {} };
const header = new Element('header'); header.position = 'fixed'; header.rect = { top: 0, bottom: 40, width: 390, height: 40 };
const menu = new Element('ul', { id: 'topmenu' }); menu.position = 'relative'; menu.parentElement = header; menu.rect = { top: 40, bottom: 120, width: 390, height: 80 };
const document = { querySelectorAll: () => [header, menu] };
const window = { scrollY: 0, getComputedStyle: el => ({ position: el.position || 'static' }) };
const page = new Function('view', 'rpc', 'form', 'uci', 'poll', 'ui', 'dom', 'E', 'document', 'window', 'requestAnimationFrame', source)(view, rpc, form, uci, poll, ui, dom, E, document, window, callback => callback());
(async () => {
	const root = await page.render([{ apply_ready: false, apply_unavailable_reason: 'watchdog missing', channels: [] }]);
	assert.equal(root.attrs.class, 'mwan3-ab-view');
	assert.match(root.children[0].text(), /@media\(max-width:640px\)/);
	assert.match(root.children[0].text(), /content:attr\(data-title\)/);
	assert.match(root.children[0].text(), /\.mwan3-ab-view \.cbi-value-title/);
	root.isConnected = true; page.headerSpacing();
	assert.equal(root.style.paddingTop, '112px', 'include menu extending beyond the40px fixed header');
	window.scrollY = 500; root.rect.top = -480; page.headerSpacing();
	assert.equal(root.style.paddingTop, '112px', 'polling while scrolled must not grow the top gap');
	window.scrollY = 0; root.rect.top = 20;
	const option = (id, name) => page.map.lookupOption(name, id)[0];
	assert.match(option('main', 'mode').validate('main', 'automatic'), /watchdog missing/);
	assert.equal(option('main', 'mode').validate('main', 'observe'), true);
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
	assert.match(option('main', 'policy').validate('main', 'other'), /Сначала восстановите/);
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
	assert.match(option('override', 'interface').validate('override', 'wan'), /уже существует/);
	assert.notEqual(option('override', 'interface').validate('override', 'unknown'), true);
	page.refresh({ apply_ready: true, busy: false, channels: Array.from({ length: 6 }, (_, i) => ({ interface: `wan${i}`, online: true, enabled: true, metric: 1, baseline_weight: 500, proposed_weight: i === 0 ? 1000 : 0, applied_weight: 0, speed_mbps: null, age_seconds: null, probe_state: i === 1 ? 'historical' : i === 2 ? 'measuring' : i === 3 ? 'failed' : 'unknown', budget_used_bytes: 0, budget_limit_bytes: 268435456 })) });
	const text = page.summary.text();
	assert.match(text, /wan5/); assert.match(text, /16.7%/); assert.match(text, /Устарело/); assert.match(text, /Измерение/); assert.match(text, /Неизвестно/); assert.match(text, /Ошибка/);
	assert.doesNotMatch(text, /0\.00 Мбит/); assert.match(text, /100\.0%/);
	function findTable(element) { if (element.tag === 'table') return element; for (const child of element.children) if (child instanceof Element) { const table = findTable(child); if (table) return table; } }
	function rows() { return findTable(page.summary).children.slice(1).map(row => row.children.map(cell => cell.text())); }
	const channel = (iface, extra = {}) => ({ interface: iface, enabled: true, online: true, metric: 1, baseline_weight: 1000, proposed_weight: 0, applied_weight: 0, speed_mbps: null, age_seconds: null, probe_state: 'unknown', budget_used_bytes: 0, budget_limit_bytes: 268435456, ...extra });
	page.refresh({ apply_ready: true, phase: 'calibrating', channels: [channel('disabled', { enabled: false }), channel('modem1', { proposed_weight: 200, applied_weight: 333, phase: 'calibrating' }), channel('modem2', { proposed_weight: 300, applied_weight: 333, phase: 'maintenance' }), channel('petra', { proposed_weight: 500, applied_weight: 333, phase: 'holding' }), channel('offline', { online: false, metric: 0 }), channel('reserve', { metric: 2, phase: 'disabled' })] });
	let tableRows = rows();
	const labels = ['WAN', 'Доступность', 'Фаза', 'Измерение', 'Скорость / возраст', 'Штатная доля', 'Предложено', 'Применено', 'Следующий замер', 'Резерв / лимит за сутки'];
	findTable(page.summary).children.slice(1).forEach(row => {
		assert.match(row.attrs.class, /mwan3-ab-channel/);
		assert.deepEqual(row.children.map(cell => cell.attrs['data-title']), labels, 'every mobile card metric retains a label');
	});
	assert.equal(tableRows[0][1], 'Отключён'); assert.equal(tableRows[0][5], '0.0%');
	assert.equal(tableRows[1][5], '33.3%'); assert.equal(tableRows[2][5], '33.3%'); assert.equal(tableRows[3][5], '33.3%');
	assert.equal(tableRows[4][5], '0.0%'); assert.equal(tableRows[5][5], '0.0%');
	assert.equal(tableRows[1][6], '20.0%'); assert.equal(tableRows[2][6], '30.0%'); assert.equal(tableRows[3][6], '50.0%');
	assert.equal(tableRows[1][7], '33.3%'); assert.equal(tableRows[0][7], '0.0%');
	assert.match(page.summary.text(), /фаза: Калибровка/);
	assert.equal(tableRows[1][2], 'Калибровка'); assert.equal(tableRows[2][2], 'Периодические замеры'); assert.equal(tableRows[3][2], 'Удержание долей'); assert.equal(tableRows[5][2], 'Замеры выключены');
	page.refresh({ phase: 'error', channels: [channel('spent', { budget_used_bytes: 268435456, probe_error: 'daily probe budget exhausted', probe_state: 'failed', phase: 'error' }), channel('small', { budget_used_bytes: 268435456 - 16 * 1048576 }), channel('inflight', { budget_used_bytes: 268435456, probe_state: 'measuring', probe_error: 'previous failure' }), channel('retry', { budget_used_bytes: 268435456 - 32 * 1048576, probe_error: 'daily probe budget exhausted' })] });
	tableRows = rows();
	assert.equal(tableRows[0][3], 'Суточный резерв исчерпан'); assert.equal(tableRows[0][2], 'Ошибка');
	assert.equal(tableRows[1][3], 'Резерв меньше одного замера');
	assert.equal(tableRows[2][3], 'Измерение');
	assert.equal(tableRows[3][3], 'Недостаточно резерва для повторного замера');
	assert.match(tableRows[0][9], /Суточный резерв исчерпан/); assert.match(page.summary.text(), /фаза: Ошибка/);
	// Logical-WAN overrides set the minimum future reservation independently.
	sections = sections.filter(s => s['.type'] !== 'wan'); sections.push({ '.name': 'small_override', '.type': 'wan', interface: 'wan2', probe_bytes: String(16 * 1048576) });
	page.refresh({ phase: 'maintenance', channels: [channel('wan2', { budget_used_bytes: 268435456 - 16 * 1048576 }), channel('inherited', { budget_used_bytes: 268435456 - 16 * 1048576 })] });
	assert.equal(rows()[0][3], 'Неизвестно'); assert.equal(rows()[1][3], 'Резерв меньше одного замера');
	page.refresh({ error: 'status_unavailable' });
	assert.equal(page.probeButton.disabled, true); assert.equal(page.restoreButton.disabled, false);
	console.log('LuCI fixtures passed: mobile labels/header clearance, exact hours conversion, active shares/phases/quotas, safe settings, fallback Restore');
})().catch(err => { console.error(err); process.exit(1); });
