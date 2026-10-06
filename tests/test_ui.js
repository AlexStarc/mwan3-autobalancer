/* Offline tests run the real LuCI view with minimal DOM/form/RPC substitutes. */
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../openwrt/luci/view/network/mwan3-autobalancer.js'), 'utf8');
class Element {
	constructor(tag, attrs = {}, children = []) { this.tag = tag; this.attrs = attrs; this.children = []; this.disabled = false; this.append(children); }
	append(children) { (Array.isArray(children) ? children : [children]).forEach(c => { if (c !== '') this.children.push(c); }); }
	appendChild(child) { this.children.push(child); }
	querySelectorAll() { return []; }
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
const page = new Function('view', 'rpc', 'form', 'uci', 'poll', 'ui', 'dom', 'E', source)(view, rpc, form, uci, poll, ui, dom, E);
(async () => {
	await page.render([{ apply_ready: false, apply_unavailable_reason: 'watchdog missing', channels: [] }]);
	const option = (id, name) => page.map.lookupOption(name, id)[0];
	assert.match(option('main', 'mode').validate('main', 'automatic'), /watchdog missing/);
	assert.equal(option('main', 'mode').validate('main', 'observe'), true);
	assert.equal(option('main', 'interval_seconds').validate('main', '60.5'), 'Укажите целое число от 60 до 604800');
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
	assert.equal(option('override', 'probe_bytes').validate('override', ''), true);
	option('override', 'probe_bytes').write('override', '16');
	assert.equal(uci.get('mwan3_autobalancer', 'override', 'probe_bytes'), '16777216');
	option('override', 'probe_bytes').write('override', '');
	assert.equal(uci.get('mwan3_autobalancer', 'override', 'probe_bytes'), undefined);
	sections.push({ '.name': 'duplicate', '.type': 'wan', interface: 'wan' });
	assert.match(option('override', 'interface').validate('override', 'wan'), /уже существует/);
	assert.notEqual(option('override', 'interface').validate('override', 'unknown'), true);
	page.refresh({ apply_ready: true, busy: false, channels: Array.from({ length: 6 }, (_, i) => ({ interface: `wan${i}`, online: true, baseline_weight: 500, proposed_weight: i === 0 ? 1000 : 0, applied_weight: 0, speed_mbps: null, age_seconds: null, probe_state: i === 1 ? 'historical' : i === 2 ? 'measuring' : i === 3 ? 'failed' : 'unknown', budget_used_bytes: 0, budget_limit_bytes: 268435456 })) });
	const text = page.summary.text();
	assert.match(text, /wan5/); assert.match(text, /16.7%/); assert.match(text, /Устарело/); assert.match(text, /Измерение/); assert.match(text, /Неизвестно/); assert.match(text, /Ошибка/);
	assert.doesNotMatch(text, /0\.00 Мбит/); assert.match(text, /100\.0%/);
	page.refresh({ error: 'status_unavailable' });
	assert.equal(page.probeButton.disabled, true); assert.equal(page.restoreButton.disabled, false);
	console.log('LuCI fixtures passed: null/stale/N-WAN shares, safe settings, duplicate overrides, fallback Restore availability');
})().catch(err => { console.error(err); process.exit(1); });
