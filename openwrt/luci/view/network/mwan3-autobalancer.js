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

function known(value) { return typeof value === 'number' && isFinite(value) && value >= 0; }
function age(value) {
	if (!known(value)) return '—';
	if (value < 60) return Math.floor(value) + ' с';
	if (value < 3600) return Math.floor(value / 60) + ' мин';
	return (value / 3600).toFixed(1) + ' ч';
}
function next(value) {
	if (!value) return '—';
	var date = new Date(value);
	return isNaN(date.getTime()) || date.getFullYear() < 2000 ? '—' : date.toLocaleString();
}
function percent(weight, total) { return known(weight) && total > 0 ? (100 * weight / total).toFixed(1) + '%' : '—'; }
function state(channel) {
	if (channel.probe_state === 'measuring') return 'Измерение';
	if (channel.probe_error || channel.probe_state === 'failed') return 'Ошибка';
	if (channel.probe_state === 'historical') return 'Устарело';
	if (known(channel.speed_mbps)) return 'Актуально';
	return 'Неизвестно';
}

return view.extend({
	load: function() {
		return Promise.all([status().catch(function() { return { error: 'status_unavailable' }; }), uci.load('mwan3_autobalancer'), uci.load('mwan3')]);
	},
	refresh: function(report) {
		this.report = report || { error: 'status_unavailable' };
		var r = this.report, channels = Array.isArray(r.channels) ? r.channels : [];
		var sums = { baseline_weight: 0, proposed_weight: 0, applied_weight: 0 };
		channels.forEach(function(c) {
			Object.keys(sums).forEach(function(key) { if (known(c[key])) sums[key] += c[key]; });
		});
		var table = E('table', { 'class': 'table' }, [E('tr', { 'class': 'tr table-titles' },
			['WAN', 'Доступность', 'Измерение', 'Скорость / возраст', 'Штатная доля', 'Предложено', 'Применено', 'Следующий замер', 'Резерв / лимит за сутки'].map(function(t) { return E('th', { 'class': 'th' }, t); }))]);
		channels.forEach(function(c) {
			var values = [c.interface || '—', c.online ? 'Онлайн' : 'Офлайн', state(c),
				(known(c.speed_mbps) ? c.speed_mbps.toFixed(2) + ' Мбит/с' : '—') + ' / ' + age(c.age_seconds),
				percent(c.baseline_weight, sums.baseline_weight), percent(c.proposed_weight, sums.proposed_weight), percent(c.applied_weight, sums.applied_weight),
				next(c.next_probe_at), known(c.budget_used_bytes) && known(c.budget_limit_bytes) ? (c.budget_used_bytes / MiB).toFixed(1) + ' / ' + (c.budget_limit_bytes / MiB).toFixed(1) + ' МиБ' : '—'];
			table.appendChild(E('tr', { 'class': 'tr', 'title': c.probe_error || '' }, values.map(function(v) { return E('td', { 'class': 'td' }, v); })));
		});
		if (!channels.length) table.appendChild(E('tr', {}, E('td', { 'colspan': 9 }, 'Данные каналов недоступны')));
		dom.content(this.summary, [
			E('p', {}, r.error ? 'Контроллер недоступен. Восстановление штатной политики доступно независимо от него.' :
				'Политика: ' + (r.policy || '—') + '; режим: ' + (r.mode === 'automatic' ? 'Автоматический' : 'Наблюдение') + '; следующий замер: ' + next(r.next_probe_at)),
			E('p', {}, r.apply_ready ? 'Автоматическое применение доступно.' : 'Автоматическое применение недоступно: ' + (r.apply_unavailable_reason || r.compatibility_error || r.error || 'ожидание проверки')),
			r.last_error ? E('p', { 'class': 'alert-message warning' }, r.last_error) : '',
			E('div', { 'style': 'overflow-x:auto' }, table),
			E('p', {}, 'Скорость — оценка по завершённым тестовым загрузкам. Резерв включает запланированный объём; это не счётчик фактического трафика. Лимит действует отдельно для каждого WAN, сутки — UTC.')
		]);
		this.probeButton.disabled = !!r.error || !!r.busy || this.actionBusy;
		this.restoreButton.disabled = this.actionBusy;
		var mode = this.modeOption.getUIElement('main');
		if (mode && mode.node) {
			mode.node.querySelectorAll('[data-value="automatic"], option[value="automatic"]').forEach(function(el) {
				el.disabled = !r.apply_ready; el.setAttribute('aria-disabled', String(!r.apply_ready));
			});
		}
	},
	action: function(command) {
		if (this.actionBusy) return;
		this.actionBusy = true; this.refresh(this.report);
		return command().then(function(result) {
			if (result.error || result.last_error) throw new Error(result.error || result.last_error);
			ui.addNotification(null, E('p', {}, command === probe ? 'Цикл измерений поставлен в очередь; веса не применяются.' : 'Штатная политика восстановлена, включён режим наблюдения.'));
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
		var m = this.map = new form.Map('mwan3_autobalancer', 'Автобалансировка', 'Настройте URL и лимиты, затем выполните пробный замер. Установка работает в режиме наблюдения и не запускает загрузки с пустым URL.');
		var s = m.section(form.NamedSection, 'main', 'main', 'Настройки'); s.addremove = false;
		var o = s.option(form.Flag, 'enabled', 'Включено'); o.rmempty = false;
		o = this.modeOption = s.option(form.ListValue, 'mode', 'Режим');
		o.value('observe', 'Наблюдение'); o.value('automatic', 'Автоматическое применение'); o.rmempty = false;
		o.validate = function(section, value) { return value !== 'automatic' || this.report.apply_ready ? true : 'Автоматический режим недоступен: ' + (this.report.apply_unavailable_reason || this.report.error || 'проверка не завершена'); }.bind(this);
		o = s.option(form.ListValue, 'policy', 'Политика mwan3');
		uci.sections('mwan3', 'policy').forEach(function(p) { if (/^[A-Za-z0-9_][A-Za-z0-9_-]{0,14}$/.test(p['.name'])) o.value(p['.name']); });
		o.rmempty = false;
		o.validate = function(section, value) {
			if (!/^[A-Za-z0-9_][A-Za-z0-9_-]{0,14}$/.test(value)) return 'Некорректная политика';
			return this.report.lease_active && value !== this.report.policy ? 'Сначала восстановите предыдущую политику кнопкой ниже' : true;
		}.bind(this);
		o = s.option(form.Value, 'probe_url', 'URL тестовой загрузки', 'HTTP(S), без логина и фрагмента. {bytes} в пути или запросе задаёт размер для ограниченного повторного замера. Пустое поле выключает замеры.');
		o.validate = function(section, value) {
			if (!value) return true;
			try { var url = new URL(value); return /^https?:$/.test(url.protocol) && !url.username && !url.password && !url.hash && !/[\r\n\x00]/.test(value) && !url.host.includes('{bytes}') ? true : 'Некорректный HTTP(S) URL'; }
			catch (err) { return 'Некорректный HTTP(S) URL'; }
		};
		o = s.option(form.ListValue, 'schedule_mode', 'Расписание'); o.value('hybrid', 'После изменений и периодически'); o.value('on-change', 'Только после изменений'); o.rmempty = false;
		function interval(section, optional) {
			var v = section.option(form.Value, 'interval_seconds', 'Интервал, секунды', optional ? 'Пустое поле наследует общее значение.' : 'По умолчанию 21600 (6 часов); используется периодическим расписанием.');
			v.rmempty = optional;
			v.validate = function(id, value) { return optional && value === '' || /^[0-9]+$/.test(value) && Number(value) >= 60 && Number(value) <= 604800 ? true : 'Укажите целое число от 60 до 604800'; };
			return v;
		}
		function effective(id, name) {
			var option = m.lookupOption(name, id), value = option.length ? option[0].formvalue(id) : '';
			if ((value == null || value === '') && id !== 'main') return effective('main', name);
			return Number(value) * MiB;
		}
		function bytes(section, name, label, optional, maximum) {
			var v = section.option(form.Value, name, label + ', МиБ', optional ? 'Пустое поле наследует общее значение.' : 'Лимит отдельно для каждого логического WAN.');
			v.rmempty = optional;
			v.cfgvalue = function(id) { var value = uci.get('mwan3_autobalancer', id, name); return value === undefined || value === '' ? '' : String(Number(value) / MiB); };
			v.validate = function(id, value) {
				var n = optional && value === '' ? effective('main', name) : Number(value) * MiB;
				if (!(optional && value === '') && !(value.trim() !== '' && Number.isSafeInteger(n) && n >= 262144 && n <= maximum)) return 'Укажите размер от 0.25 до ' + maximum / MiB + ' МиБ, кратный одному байту';
				var probeSize = name === 'probe_bytes' ? n : effective(id, 'probe_bytes');
				var budget = name === 'daily_budget_bytes' ? n : effective(id, 'daily_budget_bytes');
				var maxProbe = name === 'max_probe_bytes' ? n : effective('main', 'max_probe_bytes');
				if (probeSize > maxProbe) return 'Объём замера превышает максимальный размер';
				if (budget < probeSize) return 'Суточный резерв меньше объёма одного замера';
				return true;
			};
			v.write = function(id, value) { if (value === '' && optional) uci.unset('mwan3_autobalancer', id, name); else uci.set('mwan3_autobalancer', id, name, String(Number(value) * MiB)); };
			return v;
		}
		interval(s, false);
		bytes(s, 'probe_bytes', 'Объём одного замера', false, 1073741824);
		bytes(s, 'max_probe_bytes', 'Максимум увеличенного замера', false, 1073741824);
		bytes(s, 'daily_budget_bytes', 'Суточный резерв', false, 1099511627776);
		var w = m.section(form.TypedSection, 'wan', 'Переопределения WAN', 'Добавляйте только интерфейсы выбранной политики. Пустые значения наследуются из общих настроек.');
		w.anonymous = true; w.addremove = true;
		o = w.option(form.ListValue, 'interface', 'Логический WAN'); o.rmempty = false;
		var interfaces = new Set();
		uci.sections('mwan3', 'member').forEach(function(member) { if (member.interface) interfaces.add(member.interface); });
		Array.from(interfaces).sort().forEach(function(iface) { o.value(iface); });
		o.validate = function(id, value) {
			var selected = m.lookupOption('policy', 'main')[0].formvalue('main'), policy = uci.get('mwan3', selected, 'use_member') || [], allowed = new Set();
			if (!Array.isArray(policy)) policy = policy.split(/\s+/);
			policy.forEach(function(member) { allowed.add(uci.get('mwan3', member, 'interface')); });
			if (!allowed.has(value)) return 'WAN отсутствует в выбранной политике';
			var duplicates = uci.sections('mwan3_autobalancer', 'wan').filter(function(section) { return section['.name'] !== id && this.formvalue(section['.name']) === value; }.bind(this));
			return duplicates.length ? 'Переопределение этого WAN уже существует' : true;
		};
		interval(w, true); bytes(w, 'probe_bytes', 'Объём одного замера', true, 1073741824); bytes(w, 'daily_budget_bytes', 'Суточный резерв', true, 1099511627776);
		this.summary = E('div');
		this.probeButton = E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'action', probe) }, 'Измерить');
		this.restoreButton = E('button', { 'class': 'cbi-button cbi-button-reset', 'click': ui.createHandlerFn(this, 'action', rollback) }, 'Восстановить штатную политику');
		return m.render().then(function(node) {
			this.refresh(this.report);
			poll.add(function() { return status().then(this.refresh.bind(this)).catch(function() { this.refresh({ error: 'status_unavailable' }); }.bind(this)); }.bind(this), 5);
			return E('div', {}, [this.summary, E('div', { 'class': 'cbi-page-actions' }, [this.probeButton, ' ', this.restoreButton]), node]);
		}.bind(this));
	}
});
