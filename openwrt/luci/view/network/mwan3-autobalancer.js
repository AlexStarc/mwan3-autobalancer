'use strict';
'require view';
'require rpc';
'require form';
'require uci';
'require poll';
'require ui';
'require dom';

var status = rpc.declare({ object: 'mwan3.autobalancer', method: 'status', expect: {} });
var probe = rpc.declare({ object: 'mwan3.autobalancer', method: 'probe', expect: {} });
var rollback = rpc.declare({ object: 'mwan3.autobalancer', method: 'rollback', expect: {} });
var MiB = 1048576;
var columnTitles = [_('WAN'), _('Availability'), _('Phase'), _('Measurement'), _('Speed / age'), _('Stock share'), _('Proposed'), _('Applied'), _('Next measurement'), _('Daily reservation / limit')];
var pageCSS = '.mwan3-ab-view{width:100%;max-width:100%;min-width:0;box-sizing:border-box;overflow-wrap:anywhere}' +
	'.mwan3-ab-view .mwan3-ab-table-wrap{max-width:100%;overflow-x:auto}' +
	'.mwan3-ab-view .mwan3-ab-actions{display:flex;flex-wrap:wrap;gap:.75rem;justify-content:flex-start}' +
	'.mwan3-ab-view .mwan3-ab-actions button{max-width:100%;white-space:normal}' +
	'@media(max-width:640px){' +
	'.mwan3-ab-view .mwan3-ab-table-wrap{overflow:visible}' +
	'.mwan3-ab-view .mwan3-ab-table,.mwan3-ab-view .mwan3-ab-table tbody{display:block;width:100%;min-width:0}' +
	'.mwan3-ab-view .mwan3-ab-table .table-titles{display:none}' +
	'.mwan3-ab-view .mwan3-ab-table .mwan3-ab-channel{display:block;margin:0 0 1rem;border:1px solid #ddd;border-radius:.3rem;padding:.5rem}' +
	'.mwan3-ab-view .mwan3-ab-table .td{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.2fr);gap:.75rem;width:auto!important;min-width:0!important;max-width:100%;box-sizing:border-box;padding:.4rem .25rem;text-align:left;white-space:normal;overflow-wrap:anywhere}' +
	'.mwan3-ab-view .mwan3-ab-table .td:before{content:attr(data-title);font-weight:600}' +
	'.mwan3-ab-view .cbi-map,.mwan3-ab-view .cbi-section,.mwan3-ab-view .cbi-value{min-width:0!important;max-width:100%;box-sizing:border-box}' +
	'.mwan3-ab-view .cbi-value{display:block;width:100%!important}' +
	'.mwan3-ab-view .cbi-value-title,.mwan3-ab-view .cbi-value-field{display:block;float:none!important;width:100%!important;min-width:0!important;max-width:100%;margin:0!important;padding:.35rem 0!important;box-sizing:border-box;text-align:left}' +
	'.mwan3-ab-view .cbi-value-title{text-align:left!important;white-space:normal!important;overflow-wrap:anywhere}' +
	'.mwan3-ab-view .cbi-value-field input:not([type=checkbox]):not([type=radio]),.mwan3-ab-view .cbi-value-field select,.mwan3-ab-view .cbi-value-field .cbi-dropdown{width:100%!important;min-width:0!important;max-width:100%;box-sizing:border-box}' +
	'.mwan3-ab-view .cbi-value-description{max-width:100%;white-space:normal}' +
	'}';

function known(value) { return typeof value === 'number' && isFinite(value) && value >= 0; }
function age(value) {
	if (!known(value)) return '—';
	if (value < 60) return _('%s s').format(Math.floor(value));
	if (value < 3600) return _('%s min').format(Math.floor(value / 60));
	return _('%s h').format((value / 3600).toFixed(1));
}
function next(value) {
	if (!value) return '—';
	var date = new Date(value);
	return isNaN(date.getTime()) || date.getFullYear() < 2000 ? '—' : date.toLocaleString();
}
function percent(weight, total) { return known(weight) && total > 0 ? (100 * weight / total).toFixed(1) + '%' : '—'; }
function phase(value) {
	return { disabled: _('Measurements disabled'), calibrating: _('Calibration'), maintenance: _('Periodic measurements'), holding: _('Holding shares'), error: _('Error') }[value] || value || '—';
}
function probeBytes(iface) {
	var overrides = uci.sections('mwan3_autobalancer', 'wan').filter(function(s) { return s.interface === iface; });
	var value = overrides.length === 1 ? overrides[0].probe_bytes : null;
	if (value == null || value === '') value = uci.get('mwan3_autobalancer', 'main', 'probe_bytes');
	return value != null && value !== '' && known(Number(value)) ? Number(value) : null;
}
function budgetState(channel) {
	if (!known(channel.budget_used_bytes) || !known(channel.budget_limit_bytes)) return '';
	var remaining = Math.max(0, channel.budget_limit_bytes - channel.budget_used_bytes), bytes = probeBytes(channel.interface);
	if (remaining === 0) return _('Daily reservation exhausted');
	if (bytes !== null && remaining < bytes) return _('Reservation is smaller than one measurement');
	if ((channel.probe_error || '').includes('daily probe budget exhausted')) return _('Insufficient reservation for a retry');
	return '';
}
function state(channel) {
	if (channel.probe_state === 'measuring') return _('Measuring');
	var budget = budgetState(channel);
	if (budget) return budget;
	if (channel.probe_error || channel.probe_state === 'failed') return _('Error');
	if (channel.probe_state === 'historical') return _('Stale');
	if (known(channel.speed_mbps)) return _('Current');
	return _('Unknown');
}

return view.extend({
	load: function() {
		return Promise.all([status().catch(function() { return { error: 'status_unavailable' }; }), uci.load('mwan3_autobalancer'), uci.load('mwan3')]);
	},
	headerSpacing: function() {
		if (!this.root || !this.root.isConnected) return;
		var top = this.root.getBoundingClientRect().top + (window.scrollY || 0), bottom = 0;
		document.querySelectorAll('header,.navbar,.navbar-fixed-top,#header,#mainmenu,#topmenu').forEach(function(el) {
			var ancestor = el, pinned = false, extent = 0;
			while (ancestor) {
				var css = window.getComputedStyle(ancestor), box = ancestor.getBoundingClientRect();
				if (box.width > 0 && box.height > 0) extent = Math.max(extent, box.bottom);
				if ((css.position === 'fixed' || css.position === 'sticky') && box.top <= 0) { pinned = true; break; }
				ancestor = ancestor.parentElement;
			}
			// Stock header layout containers can extend below both header and menu.
			// Measure only their ancestry, never transient dropdown descendants.
			if (pinned) bottom = Math.max(bottom, extent);
		});
		// Document coordinates keep the same clearance when polling while scrolled.
		this.root.style.paddingTop = bottom > top ? Math.ceil(bottom - top + 12) + 'px' : '0px';
	},
	refresh: function(report) {
		this.report = report || { error: 'status_unavailable' };
		var r = this.report, channels = Array.isArray(r.channels) ? r.channels : [];
		var sums = { baseline_weight: 0, proposed_weight: 0, applied_weight: 0 };
		var lowest = channels.reduce(function(metric, c) { return c.online && c.enabled && known(c.metric) ? Math.min(metric, c.metric) : metric; }, Infinity);
		function active(c) { return c.online && c.enabled && c.metric === lowest; }
		channels.forEach(function(c) {
			if (active(c) && known(c.baseline_weight)) sums.baseline_weight += c.baseline_weight;
			['proposed_weight', 'applied_weight'].forEach(function(key) { if (known(c[key])) sums[key] += c[key]; });
		});
		var table = E('table', { 'class': 'table mwan3-ab-table' }, [E('tr', { 'class': 'tr table-titles' },
			columnTitles.map(function(t) { return E('th', { 'class': 'th' }, t); }))]);
		channels.forEach(function(c) {
			var budget = budgetState(c);
			var values = [c.interface || '—', !c.enabled ? _('Disabled') : c.online ? _('Online') : _('Offline'), phase(c.phase), state(c),
				(known(c.speed_mbps) ? _('%s Mbit/s').format(c.speed_mbps.toFixed(2)) : '—') + ' / ' + age(c.age_seconds),
				active(c) ? percent(c.baseline_weight, sums.baseline_weight) : '0.0%', percent(c.proposed_weight, sums.proposed_weight), percent(c.applied_weight, sums.applied_weight),
				next(c.next_probe_at), known(c.budget_used_bytes) && known(c.budget_limit_bytes) ? _('%s / %s MiB').format((c.budget_used_bytes / MiB).toFixed(1), (c.budget_limit_bytes / MiB).toFixed(1)) + (budget ? ' — ' + budget : '') : '—'];
			table.appendChild(E('tr', { 'class': 'tr mwan3-ab-channel', 'title': c.probe_error || '' }, values.map(function(v, i) { return E('td', { 'class': 'td', 'data-title': columnTitles[i] }, E('span', {}, v)); })));
		});
		if (!channels.length) table.appendChild(E('tr', {}, E('td', { 'colspan': 10 }, _('Channel data unavailable'))));
		dom.content(this.summary, [
			E('p', {}, r.error ? _('Controller unavailable. Stock policy restoration remains available independently.') :
				_('Policy: %s; mode: %s; phase: %s; next measurement: %s').format(r.policy || '—', r.mode === 'automatic' ? _('Automatic') : _('Observe'), phase(r.phase), next(r.next_probe_at))),
			E('p', {}, r.apply_ready ? _('Automatic application is available.') : _('Automatic application unavailable: %s').format(r.apply_unavailable_reason || r.compatibility_error || r.error || _('Waiting for checks'))),
			r.last_error ? E('p', { 'class': 'alert-message warning' }, r.last_error) : '',
			E('div', { 'class': 'mwan3-ab-table-wrap' }, table),
			E('p', {}, _('Speed is estimated from completed test downloads. The reservation includes planned data; it is not a counter of actual traffic. The limit applies separately to each WAN, per UTC day.'))
		]);
		this.probeButton.disabled = !!r.error || !!r.busy || this.actionBusy;
		this.restoreButton.disabled = this.actionBusy;
		this.headerSpacing();
		var mode = this.modeOption.getUIElement('main');
		if (mode && mode.node) {
			mode.node.querySelectorAll('[data-value="automatic"], option[value="automatic"]').forEach(function(el) {
				el.disabled = !r.apply_ready; el.setAttribute('aria-disabled', String(!r.apply_ready));
			});
			mode.triggerValidation();
		}
	},
	action: function(command) {
		if (this.actionBusy) return;
		this.actionBusy = true; this.refresh(this.report);
		return command().then(function(result) {
			if (result.error || result.last_error) throw new Error(result.error || result.last_error);
			ui.addNotification(null, E('p', {}, command === probe ? _('Measurement cycle queued; weights will not be applied.') : _('Stock policy restored; observe mode enabled.')));
			if (command === rollback) {
				uci.unload('mwan3_autobalancer');
				return uci.load('mwan3_autobalancer').then(function() { return this.map.reset(); }.bind(this));
			}
		}.bind(this)).catch(function(err) { ui.addNotification(null, E('p', {}, String(err)), 'error'); })
			.then(function() { return status().catch(function() { return { error: 'status_unavailable' }; }); })
			.then(function(r) { this.actionBusy = false; this.refresh(r); }.bind(this));
	},
	render: function(data) {
		this.report = data[0]; this.actionBusy = false;
		var m = this.map = new form.Map('mwan3_autobalancer', _('Autobalancing'), _('Set the URL and limits, then run a test measurement. Installation uses observe mode and does not start downloads with an empty URL.'));
		var s = m.section(form.NamedSection, 'main', 'main', _('Settings')); s.addremove = false;
		var o = s.option(form.Flag, 'enabled', _('Enabled')); o.rmempty = false;
		o = this.modeOption = s.option(form.ListValue, 'mode', _('Mode'));
		o.value('observe', _('Observe')); o.value('automatic', _('Automatic application')); o.rmempty = false;
		o.validate = function(section, value) { return value !== 'automatic' || this.report.apply_ready ? true : _('Automatic mode unavailable: %s').format(this.report.apply_unavailable_reason || this.report.error || _('Checks incomplete')); }.bind(this);
		o = s.option(form.ListValue, 'policy', _('mwan3 policy'));
		uci.sections('mwan3', 'policy').forEach(function(p) { if (/^[A-Za-z0-9_][A-Za-z0-9_-]{0,14}$/.test(p['.name'])) o.value(p['.name']); });
		o.rmempty = false;
		o.validate = function(section, value) {
			if (!/^[A-Za-z0-9_][A-Za-z0-9_-]{0,14}$/.test(value)) return _('Invalid policy');
			return this.report.lease_active && value !== this.report.policy ? _('Restore the previous policy using the button below first') : true;
		}.bind(this);
		o = s.option(form.Value, 'probe_url', _('Test download URL'), _('HTTP(S), without credentials or a fragment. {bytes} in the path or query sets the size for a bounded retry. An empty field disables measurements.'));
		o.validate = function(section, value) {
			if (!value) return true;
			try { var url = new URL(value); return /^https?:$/.test(url.protocol) && !url.username && !url.password && !url.hash && !/[\r\n\x00]/.test(value) && !url.host.includes('{bytes}') ? true : _('Invalid HTTP(S) URL'); }
			catch (err) { return _('Invalid HTTP(S) URL'); }
		};
		o = s.option(form.ListValue, 'schedule_mode', _('Schedule')); o.value('hybrid', _('After changes and periodically')); o.value('on-change', _('Only after changes')); o.rmempty = false;
		function interval(section, optional) {
			var v = section.option(form.Value, 'interval_seconds', _('Interval, hours'), optional ? _('An empty field inherits the global value.') : _('Default: 6 hours; used by the periodic schedule.'));
			v.rmempty = optional;
			v.cfgvalue = function(id) { var value = uci.get('mwan3_autobalancer', id, 'interval_seconds'); return value == null || value === '' ? '' : String(Number(value) / 3600); };
			function seconds(value) { return Number(value.replace(',', '.')) * 3600; }
			v.validate = function(id, value) {
				if (optional && value === '') return true;
				var n = seconds(value), rounded = Math.round(n);
				return /^[0-9]+([.,][0-9]+)?$/.test(value) && Number.isSafeInteger(rounded) && Math.abs(n - rounded) < 0.0000001 && rounded >= 60 && rounded <= 604800 ? true : _('Enter between 1 minute and 168 hours, with whole-second precision');
			};
			v.write = function(id, value) { if (optional && value === '') uci.unset('mwan3_autobalancer', id, 'interval_seconds'); else uci.set('mwan3_autobalancer', id, 'interval_seconds', String(Math.round(seconds(value)))); };
			return v;
		}
		function effective(id, name) {
			var option = m.lookupOption(name, id), value = option.length ? option[0].formvalue(id) : '';
			if ((value == null || value === '') && id !== 'main') return effective('main', name);
			return Number(value) * MiB;
		}
		function bytes(section, name, label, optional, maximum) {
			var v = section.option(form.Value, name, _('%s, MiB').format(label), optional ? _('An empty field inherits the global value.') : _('The limit applies separately to each logical WAN.'));
			v.rmempty = optional;
			v.cfgvalue = function(id) { var value = uci.get('mwan3_autobalancer', id, name); return value === undefined || value === '' ? '' : String(Number(value) / MiB); };
			v.validate = function(id, value) {
				var n = optional && value === '' ? effective('main', name) : Number(value) * MiB;
				if (!(optional && value === '') && !(value.trim() !== '' && Number.isSafeInteger(n) && n >= 262144 && n <= maximum)) return _('Enter a size from 0.25 to %s MiB, in whole bytes').format(maximum / MiB);
				var probeSize = name === 'probe_bytes' ? n : effective(id, 'probe_bytes');
				var budget = name === 'daily_budget_bytes' ? n : effective(id, 'daily_budget_bytes');
				var maxProbe = name === 'max_probe_bytes' ? n : effective('main', 'max_probe_bytes');
				if (probeSize > maxProbe) return _('Measurement size exceeds the maximum size');
				if (budget < probeSize) return _('Daily reservation is smaller than one measurement');
				return true;
			};
			v.write = function(id, value) { if (value === '' && optional) uci.unset('mwan3_autobalancer', id, name); else uci.set('mwan3_autobalancer', id, name, String(Number(value) * MiB)); };
			return v;
		}
		interval(s, false);
		bytes(s, 'probe_bytes', _('Size of one measurement'), false, 1073741824);
		bytes(s, 'max_probe_bytes', _('Maximum enlarged measurement'), false, 1073741824);
		bytes(s, 'daily_budget_bytes', _('Daily reservation'), false, 1099511627776);
		var w = m.section(form.TypedSection, 'wan', _('WAN overrides'), _('Add only interfaces in the selected policy. Empty values inherit the global settings.'));
		w.anonymous = true; w.addremove = true;
		o = w.option(form.ListValue, 'interface', _('Logical WAN')); o.rmempty = false;
		var interfaces = new Set();
		uci.sections('mwan3', 'member').forEach(function(member) { if (member.interface) interfaces.add(member.interface); });
		Array.from(interfaces).sort().forEach(function(iface) { o.value(iface); });
		o.validate = function(id, value) {
			var selected = m.lookupOption('policy', 'main')[0].formvalue('main'), policy = uci.get('mwan3', selected, 'use_member') || [], allowed = new Set();
			if (!Array.isArray(policy)) policy = policy.split(/\s+/);
			policy.forEach(function(member) { allowed.add(uci.get('mwan3', member, 'interface')); });
			if (!allowed.has(value)) return _('WAN is not in the selected policy');
			var duplicates = uci.sections('mwan3_autobalancer', 'wan').filter(function(section) { return section['.name'] !== id && this.formvalue(section['.name']) === value; }.bind(this));
			return duplicates.length ? _('An override for this WAN already exists') : true;
		};
		interval(w, true); bytes(w, 'probe_bytes', _('Size of one measurement'), true, 1073741824); bytes(w, 'daily_budget_bytes', _('Daily reservation'), true, 1099511627776);
		this.summary = E('div');
		this.probeButton = E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'action', probe) }, _('Measure'));
		this.restoreButton = E('button', { 'class': 'cbi-button cbi-button-reset', 'click': ui.createHandlerFn(this, 'action', rollback) }, _('Restore stock policy'));
		return m.render().then(function(node) {
			this.refresh(this.report);
			poll.add(function() { return status().then(this.refresh.bind(this)).catch(function() { this.refresh({ error: 'status_unavailable' }); }.bind(this)); }.bind(this), 5);
			this.root = E('div', { 'class': 'mwan3-ab-view' }, [E('style', {}, pageCSS), this.summary, E('div', { 'class': 'cbi-page-actions mwan3-ab-actions' }, [this.probeButton, this.restoreButton]), node]);
			requestAnimationFrame(this.headerSpacing.bind(this));
			return this.root;
		}.bind(this));
	}
});
