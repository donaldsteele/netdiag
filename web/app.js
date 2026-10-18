let tileRaf = 0;
let tileCur = {}; // non-reactive copy of the tile values, so tweening does not retrigger its own effect
let hbTimer = 0; // next heartbeat
let tabs = null; // BroadcastChannel shared by every open netdiag tab
let restartOpener = null; // element that opened the restart dialog
let session = ''; // this launch's session token, when the browser keeps no storage
document.addEventListener('alpine:init', () => {
  Alpine.data('app', () => ({
    ifaces: [], ifaceName: '', cidr: '', community: 'public',
    scanId: '', running: false, es: null,
    hosts: [], gateway: '', warnings: [], state: '',
    stage: '', done: 0, total: 0, error: '', quitted: false,
    filter: 'all', sortKey: 'ip', sortDir: 1, drawerIP: '', search: '',
    live: true, liveStatus: null, nowMs: Date.now(), announce: '', announced: {}, liveSynced: false,
    theme: document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark',
    view: 'devices', navOpen: false, views: ['devices', 'health', 'activity', 'history'], health: null, healthOpen: true, healthBusy: false, healthMsg: '',
    conflicts: [], banners: [], seenNotices: {},
    settings: null, draft: null, noInternetHere: false, settingsError: '', storeDown: false, desktopNote: '', audio: null, repairedNote: '', repairedPending: [], repairedShown: [],
    history: [], historyError: '', baseId: '', currentId: '', diff: null, diffError: '', diffBusy: false,
    shown: {},
    remoteCaps: {platform: '', rdp: false, vnc: false}, remoteMsg: {}, remoteShort: {}, remoteBusy: {},
    sessionOff: false, settingsLocked: false,
    watching: null, restartStale: false, backend: 'ok', instanceId: '', hbFails: 0, sseErr: false, tabId: '', beeper: null, focusAt: 0, starting: false,
    log: [], logSeq: 0, logHidden: {}, logStick: true, logSeen: {},
    logKinds: [{id: 'scan', label: 'Scan'}, {id: 'host', label: 'New host'}, {id: 'offline', label: 'Offline'}, {id: 'online', label: 'Online'},
               {id: 'conflict', label: 'Conflict'}, {id: 'health', label: 'Health'}, {id: 'notice', label: 'Notice'}, {id: 'error', label: 'Error'}],
    logPush(kind, text, ip) {
      this.log.push({id: ++this.logSeq, t: new Date().toISOString(), kind: kind, ip: ip || '', text: text});
      if (this.log.length > 500) this.log.splice(0, this.log.length - 500);
    },
    logShown() { return this.log.filter(e => !this.logHidden[e.kind]); },
    logToggle(k) { this.logHidden[k] = !this.logHidden[k]; },
    logTime(e) { return new Date(e.t).toTimeString().slice(0, 8); },
    logLine(e) { return this.logTime(e) + '  ' + e.kind.toUpperCase().padEnd(8) + ' ' + e.text; },
    logText() { return this.logShown().map(e => this.logLine(e)).join('\n'); },
    logRecent() { return this.log.slice(-3); },
    // The log grows with the page (no inner scroller); follow new lines while the page is scrolled to the bottom.
    logOnScroll() { const d = document.documentElement; this.logStick = d.scrollTop + d.clientHeight >= d.scrollHeight - 8; },
    logFollow() { if (this.view === 'activity' && this.logStick) this.$nextTick(() => { const d = document.documentElement; d.scrollTop = d.scrollHeight; }); },
    logHealth(prev, cur) {
      if (!prev || !cur) return;
      const old = {};
      (prev.checks || []).forEach(c => { old[c.id] = c.status; });
      (cur.checks || []).forEach(c => {
        if (old[c.id] !== undefined && old[c.id] !== c.status) this.logPush('health', (c.label || c.id) + ': ' + old[c.id] + ' -> ' + c.status);
      });
    },
    tileDefs: [
      {id: 'alive', label: 'Alive', filter: 'alive', icon: 'wifi'}, {id: 'printers', label: 'Printers', filter: 'printers', icon: 'printer'},
      {id: 'web', label: 'Web', filter: 'web', icon: 'globe'}, {id: 'mdns', label: 'mDNS', filter: 'mdns', icon: 'radio-tower'},
      {id: 'offline', label: 'Offline', filter: 'offline', icon: 'wifi-off'}, {id: 'conflicts', label: 'Conflicts', filter: 'conflicts', icon: 'triangle-alert'}],
    tileCounts() {
      const c = {alive: 0, printers: 0, web: 0, mdns: 0, offline: 0, conflicts: 0};
      for (const h of this.hosts) {
        if (h.alive) c.alive++;
        if (h.isPrinter) c.printers++;
        if ((h.web || []).length) c.web++;
        if (h.mdns) c.mdns++;
        if (this.isOffline(h)) c.offline++;
        if (this.hasConflict(h)) c.conflicts++;
      }
      return c;
    },
    tileTone(t) {
      const n = this.shown[t.id] || 0;
      if (t.id === 'conflicts' && n > 0) return 'tone-bad';
      if (t.id === 'offline' && n > 0) return 'tone-warn';
      return '';
    },
    tileClick(t) { this.filter = this.filter === t.filter ? 'all' : t.filter; },
    tweenTiles(target) {
      let reduce = false;
      try { reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches; } catch (e) {}
      cancelAnimationFrame(tileRaf);
      const from = {...tileCur}, t0 = performance.now(), dur = 350;
      const same = Object.keys(target).every(k => from[k] === target[k]);
      if (reduce || same) { tileCur = {...target}; this.shown = {...target}; return; }
      const step = (now) => {
        const p = Math.min(1, (now - t0) / dur), o = {};
        for (const k in target) o[k] = Math.round((from[k] || 0) + (target[k] - (from[k] || 0)) * p);
        tileCur = o; this.shown = o;
        if (p < 1) tileRaf = requestAnimationFrame(step);
      };
      tileRaf = requestAnimationFrame(step);
    },
    healthStrip() {
      const by = {};
      for (const c of ((this.health || {}).checks || [])) by[c.id] = c;
      const ids = [['gateway', 'Gateway'], ['dhcp', 'DHCP'], ['dns', 'DNS'], ['internet', 'Internet'], ['wifi', 'Wi-Fi'], ['webserver', 'Web server']];
      return ids.map(([id, label]) => {
        const c = by[id];
        if (!c) return {id, label, icon: this.checkIcon(id), status: 'unavailable', value: 'not checked'};
        const lat = this.healthLatency(c);
        return {id, label, icon: this.checkIcon(id), status: c.status, value: this.healthStatusText(c.status) + (lat ? ' ' + lat : '')};
      });
    },
    tabs: [{id: 'all', label: 'All'}, {id: 'printers', label: 'Printers'},
           {id: 'web', label: 'Web'}, {id: 'problems', label: 'Problems'}, {id: 'new', label: 'New'},
           {id: 'offline', label: 'Offline'}, {id: 'conflicts', label: 'Conflicts'}],

    // Alpine calls init() itself (index.html has no x-init): a second call
    // would open a second tab channel and heartbeat.
    async init() {
      this.takeSession();
      try { const v = localStorage.getItem('netdiag.live'); if (v !== null) this.live = v === '1'; } catch (e) {}
      try { const sv = localStorage.getItem('netdiag.view'); if (this.views.includes(sv)) this.view = sv; } catch (e) {}
      if (this.view === 'history') this.loadHistory();
      setInterval(() => { this.nowMs = Date.now(); }, 1000);
      // Sticky table headers sit just under the sticky alert banners.
      const bn = document.getElementById('alerts');
      if (bn && window.ResizeObserver) new ResizeObserver(() => document.documentElement.style.setProperty('--stick-top', bn.offsetHeight + 'px')).observe(bn);
      try {
        const r = await fetch('/api/interfaces');
        this.ifaces = (await r.json()) || [];
        const d = this.ifaces.find(i => i.isDefault) || this.ifaces[0];
        if (d) { this.ifaceName = d.name; this.cidr = d.suggested || d.cidr; }
      } catch (e) { this.error = 'Could not list network interfaces: ' + e.message; }
      this.initTabs();
      await this.reattach();
      this.loadSettings();
      this.loadHealth();
      this.loadRemoteCaps();
      this.startHeartbeat();
    },
    // The settings read needs the session token (v0.6-D14). A page whose
    // token is out of date does not know the person's alert choices, so it
    // stays silent and leaves the beeping to a tab that does, until a
    // working token lets it read them (api()).
    async loadSettings() {
      try {
        const r = await this.api('GET', '/api/settings');
        if (r.status === 401) { this.settingsLocked = true; this.leaveBeeper(); return; }
        if (r.status === 503) { this.storeDown = true; return; }
        if (!r.ok) return;
        const set = await r.json();
        this.takeRepaired(set.repaired);
        delete set.repaired;
        this.settings = set;
        if (this.settings.community) this.community = this.settings.community;
        if (this.settingsLocked) { this.settingsLocked = false; this.offerBeeper(); }
      } catch (e) {}
    },
    async loadHealth() {
      try {
        const r = await fetch('/api/health');
        if (r.ok) this.health = await r.json();
      } catch (e) {}
    },
    async recheck() {
      this.healthBusy = true; this.healthMsg = 'Running checks...';
      try {
        const r = await this.api('POST', '/api/health/run', {interface: this.ifaceName});
        if (r.ok) { this.health = await r.json(); this.healthMsg = 'Checks updated.'; }
        else { this.healthMsg = 'Health check failed (HTTP ' + r.status + ').'; }
      } catch (e) { this.healthMsg = 'Health check failed: ' + e.message; }
      this.healthBusy = false;
    },
    healthIcon(s) { return {ok: '✔', warn: '▲', fail: '✖', info: 'ℹ'}[s] || '–'; },
    healthStatusText(s) { return {ok: 'OK', warn: 'Warning', fail: 'Failed', info: 'Info', unavailable: 'Unavailable'}[s] || s; },
    healthSummary() {
      if (!this.health) return 'Health: not checked';
      const c = this.health.checks || [];
      const f = c.filter(x => x.status === 'fail').length, w = c.filter(x => x.status === 'warn').length;
      if (!f && !w) return 'Health: OK';
      return 'Health: ' + [f ? f + ' fail' : '', w ? w + ' warn' : ''].filter(Boolean).join(', ');
    },
    healthLatency(c) { return c.latencyMs > 0 ? c.latencyMs.toFixed(1) + ' ms' : ''; },
    // Every health check came back fine: none failed or warned, and none
    // could not finish (a timeout, or the router or the name and web
    // checks could not run). Wi-Fi and DHCP details this computer doesn't
    // share don't count. A network without internet on purpose gets here
    // too once Settings says so (v0.6-D15).
    healthFine() {
      const c = (this.health && this.health.checks) || [];
      const done = x => x.status !== 'unavailable' ||
        (x.id === 'wifi' || x.id === 'dhcp') && (x.data || {}).reason !== 'timeout';
      return c.length > 0 && c.every(x => x.status !== 'fail' && x.status !== 'warn' && done(x));
    },
    // The health checks are fine and nothing this page lists is a problem
    // by the Problems card's rules (4.11), each kept only while its alert
    // type is on in Settings: a conflict, an offline printer, any offline
    // device (off by default), a printer alert, a supply at or below the
    // threshold (v0.6-D27). M8's GET /api/problems takes this over.
    // A page that may not read the settings does not know the person's
    // alert types, so it speaks for the health checks alone.
    everythingFine() {
      if (!this.healthFine() || this.settingsLocked) return false;
      const t = (this.settings && this.settings.alerts && this.settings.alerts.triggers) || {};
      const on = (k, d) => typeof t[k] === 'boolean' ? t[k] : d;
      if (on('ipConflict', true) && this.conflicts.length) return false;
      const thr = this.supplyThreshold();
      return !this.hosts.some(h => {
        if (this.isOffline(h) && (on('deviceOffline', false) || h.isPrinter && on('printerOffline', true))) return true;
        const p = h.isPrinter && h.printer;
        if (!p) return false;
        if (on('printerAlert', true) && (p.alerts || []).length) return true;
        return on('supplyLow', true) &&
          (p.supplies || []).some(s => s.state === 'empty' || s.state === 'low' || (s.percent >= 0 && s.percent <= thr));
      });
    },
    // The Health panel's line once the checks are fine: "Everything looks
    // fine" only when nothing else is wrong either, else it speaks for the
    // health checks alone.
    healthFineText() {
      return (this.everythingFine() ? 'Everything looks fine' : 'All health checks look fine') + ' (checked ' + this.healthTime() + ')';
    },
    healthTime() { return this.clock(this.health && this.health.checkedAt) || 'just now'; },
    // "10:42" in local time for an RFC 3339 time, or ''.
    clock(at) { const d = new Date(at || ''); return isNaN(d) ? '' : d.toTimeString().slice(0, 5); },

    // ---- advice (What this means / What to try) ----
    // The buttons to show for a piece of advice. The server builds every
    // target; links are shown only for http(s) targets, and Open Location
    // settings only when this browser is on Windows too.
    adviceActions(adv) {
      const kinds = ['recheck', 'openSettings', 'showDevice', 'osSettings', 'openRouter', 'openDevicePage'];
      return ((adv && adv.actions) || []).filter(a => a && a.label && kinds.includes(a.kind) &&
        (a.kind !== 'osSettings' || this.isWindows()) &&
        (a.kind !== 'openRouter' && a.kind !== 'openDevicePage' || this.linkAction(a)));
    },
    linkAction(a) { return (a.kind === 'openRouter' || a.kind === 'openDevicePage') && /^https?:/i.test(a.target || ''); },
    isWindows() {
      const p = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || '';
      return /^win/i.test(p);
    },
    actionIcon(a) { return {recheck: 'refresh-cw', openSettings: 'settings', showDevice: 'monitor', osSettings: 'map-pin'}[a.kind] || 'external-link'; },
    async runAction(a) {
      if (a.kind === 'recheck') return this.recheck();
      if (a.kind === 'openSettings') return this.openSettings(a.target);
      if (a.kind === 'showDevice') return this.showDevice(a.target);
      if (a.kind === 'osSettings') return this.openOSPage(a.target);
    },
    async openOSPage(page) {
      try {
        const r = await this.api('POST', '/api/os/open', {page: page});
        this.healthMsg = r.ok ? 'Windows Settings is opening. If no window appears, look for it on the taskbar.'
          : r.status === 501 ? "netdiag can't open that page on this computer."
          : 'Could not open it (HTTP ' + r.status + ').';
      } catch (e) { this.healthMsg = 'Could not open it: ' + e.message; }
    },
    // Show device: the device's details in the Devices view.
    showDevice(ip) {
      if (!ip) return;
      this.showView('devices');
      this.$nextTick(() => this.openDrawer(ip));
    },
    // Esc closes an open disclosure and puts focus back on its summary; on a
    // closed one it goes on to close the drawer.
    closeDetails(ev, el) {
      if (!el.open) return;
      ev.stopPropagation();
      el.open = false;
      const s = el.querySelector('summary');
      if (s) s.focus();
    },
    // The web server check has no URL to test. Reports saved before
    // data.configured existed are recognised by their detail text.
    webUnset(c) {
      if (!c || c.id !== 'webserver') return false;
      if (c.data && typeof c.data.configured === 'boolean') return !c.data.configured;
      return c.status === 'info' && /^(not configured|no web server configured)/.test(c.detail || '');
    },
    // The web server check failed on a certificate this computer does not
    // trust, and sent that certificate's fingerprint along, for the web
    // server address set in Settings now (not an older one).
    canTrust(c) {
      return !!(this.settings && c && c.id === 'webserver' && c.status === 'fail' && c.data &&
        /^[0-9a-f]{64}$/.test(c.data.certSHA256 || '') &&
        this.sameWebURL(c.data.url, (this.settings.webServer || {}).url));
    },
    // The check reports its URL with any password hidden (user:xxxxx@host),
    // so both are compared without the password, in the browser's form.
    sameWebURL(a, b) {
      const norm = u => {
        u = String(u || '').trim();
        try { const x = new URL(u); x.password = ''; return x.href; } catch (e) { return u; }
      };
      return norm(a) !== '' && norm(a) === norm(b);
    },
    fpPairs(fp) { return (fp || '').toUpperCase().match(/../g) || []; },
    fpLong(fp) { return this.fpPairs(fp).join(':'); },
    fpShort(fp) { const p = this.fpPairs(fp); return p.slice(0, 8).join(':') + (p.length > 8 ? '…' : ''); },
    async trustCert(c) {
      if (!this.canTrust(c)) return;
      const d = c.data;
      const msg = 'Trust this certificate for the web server ' + (d.url || '') + '?\n\n' +
        'Certificate: ' + (d.certSubject || 'no name') + '\nSHA-256 fingerprint:\n' + this.fpLong(d.certSHA256) + '\n\n' +
        'Only do this for your own server. If its certificate changes later, the check will tell you.';
      if (!window.confirm(msg)) return;
      const doc = JSON.parse(JSON.stringify(this.settings));
      doc.webServer = Object.assign({}, doc.webServer, {trustedCert: d.certSHA256});
      if (await this.putSettings(doc, false)) await this.recheck();
      else this.healthMsg = 'Could not save the trusted certificate.';
    },

    // ---- settings ----
    // section (an advice button's target) names the part of Settings to
    // put focus on.
    openSettings(section) {
      if (!this.settings) {
        this.settingsError = this.storeDown ? 'The data store is unavailable, so settings cannot be changed.'
          : this.settingsLocked ? 'Settings are turned off on this page for safety. Double-click netdiag to open a fresh page.'
          : 'Settings are not loaded yet.';
      }
      else { this.settingsError = ''; }
      this.draft = JSON.parse(JSON.stringify(this.settings || {community: '', webServer: {}, alerts: {triggers: {}}, ignoredConflictMACs: [], mcp: {enabled: false}}));
      this.draft.ignoredConflictMACs = this.draft.ignoredConflictMACs || [];
      this.draft.mcp = this.draft.mcp || {enabled: false};
      this.draft.mcp.enabled = !!this.draft.mcp.enabled;
      this.draft.noInternetNetworks = this.draft.noInternetNetworks || [];
      this.noInternetHere = this.noInternetOn(this.draft);
      this.repairedNote = this.repairedText();
      const focus = {webserver: 'set-weburl', health: 'set-internet', noInternet: 'set-nointernet'}[section];
      this.$nextTick(() => {
        const d = this.$refs.dlg; if (d && !d.open) d.showModal();
        const f = focus && document.getElementById(focus); if (f) f.focus();
      });
    },
    // Settings fields that were not valid in the settings file and were
    // reset when netdiag read it (GET /api/settings "repaired"). They wait
    // in repairedPending until Settings opens, so a save made first (Mute
    // alerts, Enable desktop alerts, Trust this certificate, Ignore this
    // MAC), which writes a valid file, does not lose the notice. Each field
    // is named once per page load.
    takeRepaired(list) {
      for (const p of (Array.isArray(list) ? list : [])) {
        if (!this.repairedPending.includes(p) && !this.repairedShown.includes(p)) this.repairedPending.push(p);
      }
    },
    // A label for every path a load can report (store.RepairPaths;
    // TestSettingsRepairedUI keeps the two in step).
    repairedLabels: {
      community: 'SNMP community', printerStatusPollSec: 'Printer status check in live mode',
      webServer: 'Web server settings', 'webServer.url': 'Web server URL', 'webServer.mustContain': 'Page must contain',
      'webServer.expectStatus': 'Expected HTTP status', 'webServer.trustedCert': 'Trusted certificate',
      internetCheckURL: 'Internet check URL', noInternetNetworks: 'This network has no internet', alerts: 'Alert settings', 'alerts.sound': 'Play a sound', 'alerts.desktop': 'Desktop notifications',
      'alerts.supplyThresholdPct': 'Supply threshold', 'alerts.cooldownSec': 'Repeat-alert cooldown',
      'alerts.triggers': 'Alert types',
      'alerts.triggers.printerOffline': 'Printer goes offline', 'alerts.triggers.deviceOffline': 'Any device goes offline',
      'alerts.triggers.supplyLow': 'Printer supply low', 'alerts.triggers.printerAlert': 'Printer alert',
      'alerts.triggers.newDevice': 'New device', 'alerts.triggers.ipConflict': 'IP or name conflict',
      'alerts.triggers.healthFailed': 'Health check fails', ignoredConflictMACs: 'Ignored conflict MACs',
      mcp: 'Agent access', 'mcp.enabled': 'Allow AI agents to connect',
      chat: 'Team chat', 'chat.enabled': 'Send alerts to team chat', 'chat.minSeverity': 'Team chat alert level',
    },
    // Reset fields that have no control in Settings, so the notice names
    // them without sending people to look for them: team chat until its
    // Settings section exists, and a trusted certificate, which shows only
    // while one is set.
    repairedOffPage: ['chat', 'chat.enabled', 'chat.minSeverity', 'webServer.trustedCert'],
    repairedText() {
      const rep = this.repairedPending;
      if (!rep.length) return '';
      this.repairedShown.push(...rep);
      this.repairedPending = [];
      const label = p => this.repairedLabels[p] || p;
      const names = [...new Set(rep.map(label))];
      const here = [...new Set(rep.filter(p => !this.repairedOffPage.includes(p)).map(label))];
      let t = 'Some settings were not valid and were reset: ' + names.join(', ') + '.';
      if (here.length === names.length) t += ' Check them below.';
      else if (here.length) t += ' Check ' + here.join(', ') + ' below.';
      return t;
    },
    closeSettings() { const d = this.$refs.dlg; if (d && d.open) d.close(); },
    // The network the health checks last looked at (its id), or '': the
    // "This network has no internet" switch is kept per network (v0.6-D15).
    healthNetwork() { return (this.health && this.health.network) || ''; },
    noInternetOn(set) {
      const n = this.healthNetwork();
      return !!n && ((set && set.noInternetNetworks) || []).includes(n);
    },
    async saveSettings() {
      this.settingsError = '';
      const n = v => (v === '' || v === null ? 0 : Number(v));
      this.draft.webServer.expectStatus = n(this.draft.webServer.expectStatus);
      this.draft.alerts.supplyThresholdPct = n(this.draft.alerts.supplyThresholdPct);
      this.draft.alerts.cooldownSec = n(this.draft.alerts.cooldownSec);
      this.draft.printerStatusPollSec = n(this.draft.printerStatusPollSec);
      // A certificate trusted for one server means nothing for another.
      const hostOf = u => { try { return new URL(u).hostname.toLowerCase(); } catch (e) { return ''; } };
      const was = (this.settings && this.settings.webServer) || {};
      if (this.draft.webServer.trustedCert && this.draft.webServer.trustedCert === was.trustedCert &&
          hostOf(this.draft.webServer.url) !== hostOf(was.url)) this.draft.webServer.trustedCert = '';
      const webBefore = JSON.stringify(was);
      // The switch is on or off for this network only; the others keep theirs.
      const net = this.healthNetwork();
      const offBefore = this.noInternetOn(this.settings);
      if (net) {
        const nets = (this.draft.noInternetNetworks || []).filter(x => x !== net);
        if (this.noInternetHere) nets.push(net);
        this.draft.noInternetNetworks = nets;
      }
      const ok = await this.putSettings(this.draft, true);
      if (!ok) return;
      this.closeSettings();
      // A new web server address, trusted certificate or expected answer
      // makes the Web server card out of date, and so does the "no
      // internet" switch for the name and internet cards: check again.
      if (JSON.stringify(this.settings.webServer || {}) !== webBefore || this.noInternetOn(this.settings) !== offBefore) await this.recheck();
    },
    async putSettings(doc, showErr) {
      try {
        const r = await this.api('PUT', '/api/settings', doc);
        if (r.status === 503) { this.storeDown = true; if (showErr) this.settingsError = 'The data store is unavailable.'; return false; }
        const j = await r.json().catch(() => ({}));
        if (!r.ok) { if (showErr) this.settingsError = j.error || ('Could not save settings (HTTP ' + r.status + ')'); return false; }
        this.settings = j;
        if (j.community) this.community = j.community;
        return true;
      } catch (e) { if (showErr) this.settingsError = 'Could not save settings: ' + e.message; return false; }
    },
    removeIgnored(i) { this.draft.ignoredConflictMACs.splice(i, 1); },
    // Before the settings load the default (sound on) holds, but a page that
    // may not read them cannot know the person muted alerts: it stays silent.
    soundOn() { return this.settings ? this.settings.alerts.sound : !this.settingsLocked; },
    async toggleSound() {
      if (!this.settings) return;
      const doc = JSON.parse(JSON.stringify(this.settings));
      doc.alerts.sound = !doc.alerts.sound;
      if (doc.alerts.sound) this.beep('info');
      await this.putSettings(doc, false);
    },
    desktopState() {
      if (typeof Notification === 'undefined') return 'unsupported';
      return Notification.permission;
    },
    async enableDesktop() {
      this.desktopNote = '';
      if (typeof Notification === 'undefined') { this.desktopNote = 'This browser does not support desktop notifications.'; return; }
      let p = Notification.permission;
      if (p === 'default') p = await Notification.requestPermission();
      if (p !== 'granted') { this.desktopNote = 'Desktop alerts are blocked. Allow notifications for this site in your browser settings.'; return; }
      this.desktopNote = 'Desktop alerts enabled.';
      if (this.settings && !this.settings.alerts.desktop) {
        const doc = JSON.parse(JSON.stringify(this.settings)); doc.alerts.desktop = true;
        await this.putSettings(doc, false);
      }
    },

    // ---- alerts ----
    onNotice(n, scanId) {
      // Notice ids (n-<seq>) restart per scan, so the dedupe key includes the scan id.
      if (!n || !n.id) return;
      const key = (scanId || '') + ':' + n.id;
      if (this.seenNotices[key]) return;
      this.seenNotices[key] = true;
      // A replayed notice (the page was reloaded or reconnected) rebuilds its
      // banner silently: no sound, desktop pop-up or log line. A replayed OK
      // notice turns its banner into Fixed the same way.
      if (n.ok) { this.onFixed(n, key); return; }
      if (!n.replay) this.logPush('notice', (n.title || 'Notice') + (n.message ? ': ' + n.message : ''), n.ip);
      // A banner key is unique: never show two banners for one notice.
      this.banners = this.banners.filter(b => b.bid !== key);
      this.banners.unshift(Object.assign({}, n, {bid: key}));
      if (this.banners.length > 50) this.banners.length = 50;
      if (n.replay || !this.isBeeper()) return;
      if (this.soundOn()) this.beep(n.severity);
      if (this.settings && this.settings.alerts.desktop && this.desktopState() === 'granted') {
        try { new Notification(n.title || 'netdiag', {body: n.message || '', tag: n.key || n.id}); } catch (e) {}
      }
    },
    // An OK notice closes the problem its key opened. The fault's banner turns
    // into a Fixed banner in place (a new one if it was dismissed), with no
    // sound, and a desktop notification replaces the fault's: same tag,
    // silent (v0.6-D8). A replayed OK notice only turns its banner into Fixed:
    // no log line or desktop notification. Only the beeping tab raises the
    // desktop notification, as for the fault.
    onFixed(n, bid) {
      // A fixed problem needs no advice: the fault's What to try goes.
      const fixed = {ok: true, severity: 'info', title: n.title, message: n.message, at: n.at, openedAt: n.openedAt, ip: n.ip, advice: null};
      // A banner key is unique: never show two banners for one notice.
      this.banners = this.banners.filter(x => x.bid !== bid);
      const b = n.key ? this.banners.find(x => x.key === n.key && !x.ok) : null;
      if (b) Object.assign(b, fixed);
      else {
        this.banners.unshift(Object.assign({}, n, fixed, {bid: bid}));
        if (this.banners.length > 50) this.banners.length = 50;
      }
      if (n.replay) return;
      this.logPush('notice', 'Fixed: ' + (n.title || 'Notice') + ' ' + this.bannerText(fixed), n.ip);
      if (!this.isBeeper()) return;
      if (this.settings && this.settings.alerts.desktop && this.desktopState() === 'granted') {
        try { new Notification('Fixed: ' + (n.title || 'netdiag'), {body: (n.message ? n.message + ' ' : '') + 'Fixed ' + this.fixedText(n), tag: n.key || n.id, silent: true}); } catch (e) {}
      }
    },
    // The banner's text after its lead. A Fixed banner keeps the OK message,
    // which names what was fixed (the check, the printer alert), so three
    // fixed health checks are three different banners.
    bannerText(b) {
      if (!b.ok) return b.message;
      return this.fixedText(b) + (b.message ? ' ' + b.message : '');
    },
    // "at 10:42 (was down 12 min)." for an OK notice (local time).
    fixedText(n) {
      const at = new Date(n.at), from = new Date(n.openedAt);
      const end = isNaN(at) ? new Date() : at;
      let s = 'at ' + end.toTimeString().slice(0, 5);
      if (!isNaN(from) && end >= from) {
        const m = Math.floor((end - from) / 60000);
        s += ' (was down ' + (m < 1 ? 'less than a minute' : m < 60 ? m + ' min' : Math.floor(m / 60) + ' h ' + (m % 60) + ' min') + ')';
      }
      return s + '.';
    },
    bannerLead(b) {
      return (b.ok ? 'Fixed: ' : b.severity === 'critical' ? 'Critical: ' : b.severity === 'warn' ? 'Warning: ' : 'Notice: ') + b.title;
    },
    // Show device next to Dismiss: for a fault whose advice points at a
    // device this page lists.
    bannerDevice(b) {
      return !b.ok && !!b.ip && !!b.advice && (b.advice.actions || []).some(a => a.kind === 'showDevice') &&
        this.hosts.some(h => h.ip === b.ip);
    },
    // The storm guard's banner for another network offers Restart watching
    // while this tab still watches the old one.
    bannerRestart(b) {
      return !b.ok && b.trigger === 'netdiagLink' && b.code === 'network_changed' && this.state === 'live';
    },
    shownBanners() { return this.banners.slice(0, 3); },
    moreBanners() { return Math.max(0, this.banners.length - 3); },
    dismiss(bid) { this.banners = this.banners.filter(b => b.bid !== bid); },
    dismissAll() { this.banners = []; },
    beep(sev) {
      try {
        const AC = window.AudioContext || window.webkitAudioContext;
        if (!AC) return;
        if (!this.audio) this.audio = new AC();
        const ctx = this.audio, seq = sev === 'critical' ? [880, 660, 880] : sev === 'warn' ? [523] : [440];
        let t = ctx.currentTime;
        seq.forEach(f => {
          const o = ctx.createOscillator(), g = ctx.createGain();
          o.type = 'sine'; o.frequency.value = f; g.gain.value = 0.08;
          o.connect(g); g.connect(ctx.destination);
          o.start(t); o.stop(t + 0.16); t += 0.2;
        });
      } catch (e) {}
    },

    // ---- conflicts ----
    conflictsFor(h) {
      const ids = h.conflicts || [];
      return this.conflicts.filter(c => ids.includes(c.id) || (c.ips || []).includes(h.ip));
    },
    hasConflict(h) { return this.conflictsFor(h).length > 0; },
    // How often the MAC answering at an address changed: A, B, A is 2
    // (mirrors advice.Alternations).
    alternations(hist) {
      let n = 0, last = '';
      for (const o of (hist || [])) {
        const m = String(o.mac || '').toUpperCase();
        if (!m) continue;
        if (last && m !== last) n++;
        last = m;
      }
      return n;
    },
    // "Conflict" only for a mac_flap whose two devices were seen taking
    // turns: the address's MAC history shows at least two alternations.
    // Without a history (a scan that is not live), a mac_flap the scan
    // marked "fail" was seen as a swap. Every other kind is a "Possible
    // conflict" (mirrors advice.SwapSeen).
    conflictSeen(c) {
      if (c.kind !== 'mac_flap') return false;
      const h = this.hosts.find(x => x.ip === (c.ips || [])[0]);
      const hist = (h && h.macHistory) || [];
      if (hist.length) return this.alternations(hist) >= 2;
      return c.severity === 'fail';
    },
    // One card per device in a mac_flap or duplicate_name conflict, from the
    // conflict's MACs and their makers (Conflict.vendors, else a listed
    // device's), the addresses netdiag knows for them and the address's MAC
    // history.
    conflictCards(c) {
      if (c.kind !== 'mac_flap' && c.kind !== 'duplicate_name') return [];
      const ip0 = (c.ips || [])[0];
      const at = this.hosts.find(x => x.ip === ip0);
      const hist = (at && at.macHistory) || [];
      const vendors = {};
      for (const [m, v] of Object.entries(c.vendors || {})) vendors[String(m).toUpperCase()] = v;
      return (c.macs || []).slice(0, 6).map((m, i) => {
        const mac = String(m).toUpperCase();
        const owners = this.hosts.filter(h => String(h.mac || '').toUpperCase() === mac);
        const parts = [vendors[mac] || (owners.find(h => h.vendor) || {}).vendor || '', mac];
        if (c.kind === 'duplicate_name') {
          if (owners.length) parts.push('at ' + owners.map(h => h.ip).join(', '));
        } else {
          const also = owners.filter(h => h.ip !== ip0).map(h => h.ip);
          if (also.length) parts.push('also uses ' + also.join(', '));
          const seen = [...new Set(hist.filter(o => String(o.mac || '').toUpperCase() === mac).map(o => this.clock(o.at)).filter(Boolean))];
          if (seen.length) parts.push('seen at ' + seen.slice(-3).join(', '));
        }
        return {mac: mac, label: 'Device ' + String.fromCharCode(65 + i) + ':', text: parts.filter(Boolean).join(' · ')};
      });
    },
    conflictKind(c) {
      return {mac_flap: 'The MAC address for this IP keeps changing - two devices may share the IP, or a device is switching adapters.',
              mac_multi_ip: 'One MAC address answers for several IPs - often a range extender or router, or a device with several addresses.',
              duplicate_name: 'More than one device uses the same name.'}[c.kind] || c.kind;
    },
    onConflict(ev) {
      if (!ev || !ev.conflict) return;
      const id = ev.conflict.id;
      this.conflicts = this.conflicts.filter(c => c.id !== id);
      if (!ev.resolved) this.conflicts.push(ev.conflict);
      const k = id + (ev.resolved ? ':r' : '');
      if (!this.logSeen[k]) {
        this.logSeen[k] = true;
        const c = ev.conflict;
        this.logPush('conflict', (ev.resolved ? 'Conflict resolved: ' : 'Conflict: ') + (c.detail || c.kind), (c.ips || [])[0]);
      }
    },
    ignoreConfirmText(mac) {
      return 'Ignore MAC address ' + mac + '?\n\n' +
        'netdiag will stop warning about conflicts that involve this device.\n\n' +
        'You can undo this later: open Settings, press Remove next to it under "Ignored conflict MACs", then Save.';
    },
    async ignoreMac(mac) {
      if (this.storeDown) { this.error = 'The data store is unavailable, so the MAC cannot be ignored.'; return; }
      if (!window.confirm(this.ignoreConfirmText(mac))) return;
      try {
        const r = await this.api('POST', '/api/conflicts/ignore', {mac: mac});
        if (r.status === 503) { this.error = 'The data store is unavailable, so the MAC cannot be ignored.'; return; }
        if (!r.ok) { this.error = 'Could not ignore MAC (HTTP ' + r.status + ')'; return; }
        this.announce = 'Ignoring conflicts for ' + mac + '. To undo, remove it in Settings.';
        this.loadSettings();
        this.conflicts = this.conflicts.filter(c => !(c.macs || []).includes(mac));
      } catch (e) { this.error = 'Could not ignore MAC: ' + e.message; }
    },

    // ---- history & compare ----
    async showView(v) {
      if (!this.views.includes(v)) v = 'devices';
      this.view = v;
      this.navOpen = false;
      try { localStorage.setItem('netdiag.view', v); } catch (e) {}
      if (v === 'history') await this.loadHistory();
      if (v === 'activity') { this.logStick = true; this.logFollow(); }
    },
    async loadHistory() {
      this.historyError = '';
      try {
        const r = await fetch('/api/history');
        if (r.status === 503) { this.storeDown = true; this.historyError = 'The data store is unavailable, so history cannot be shown.'; this.history = []; return; }
        if (!r.ok) { this.historyError = 'Could not load history (HTTP ' + r.status + ')'; return; }
        this.history = (await r.json()) || [];
        const ok = this.readableHistory();
        if (!this.currentId && ok.length) this.currentId = ok[0].id;
      } catch (e) { this.historyError = 'Could not load history: ' + e.message; }
    },
    async deleteHistory(id) {
      if (!window.confirm('Delete this saved scan?')) return;
      const r = await this.api('DELETE', '/api/history/' + encodeURIComponent(id));
      if (!r.ok && r.status !== 404) { this.historyError = 'Could not delete (HTTP ' + r.status + ')'; return; }
      if (this.currentId === id) this.currentId = '';
      if (this.baseId === id) this.baseId = '';
      this.diff = null;
      await this.loadHistory();
    },
    readableHistory() { return this.history.filter(h => !h.error); },
    currentOptions() {
      const o = this.readableHistory().map(h => ({id: h.id, label: this.histLabel(h)}));
      if (this.scanId && !o.some(x => x.id === this.scanId)) o.unshift({id: this.scanId, label: 'Current scan (' + this.scanId + ')'});
      return o;
    },
    histLabel(h) { return h.startedAt + ' - ' + h.cidr + (h.live ? ' (live)' : ''); },
    compareQuery() {
      const q = new URLSearchParams();
      if (this.baseId) q.set('base', this.baseId);
      q.set('current', this.currentId);
      return q;
    },
    async runCompare() {
      this.diff = null; this.diffError = '';
      if (!this.currentId) { this.diffError = 'Choose a scan to compare.'; return; }
      this.diffBusy = true;
      try {
        const r = await fetch('/api/compare?' + this.compareQuery().toString());
        const j = await r.json().catch(() => ({}));
        if (r.status === 503) this.diffError = 'The data store is unavailable.';
        else if (!r.ok) this.diffError = j.error || ('Compare failed (HTTP ' + r.status + ')');
        else this.diff = j;
      } catch (e) { this.diffError = 'Compare failed: ' + e.message; }
      this.diffBusy = false;
    },
    exportDiff(f) {
      if (!this.currentId) return;
      const q = this.compareQuery(); q.set('format', f);
      window.location.href = '/api/compare/export?' + q.toString();
    },
    changeText(c) { return c.field + ': ' + (c.from || '(none)') + ' → ' + (c.to || '(none)'); },
    statsText(s) { s = s || {}; return (s.alive || 0) + ' alive, ' + (s.printers || 0) + ' printers, ' + (s.web || 0) + ' web'; },
    saveLive() { try { localStorage.setItem('netdiag.live', this.live ? '1' : '0'); } catch (e) {} },
    toggleTheme() {
      this.theme = this.theme === 'light' ? 'dark' : 'light';
      document.documentElement.setAttribute('data-theme', this.theme);
      try { localStorage.setItem('netdiag.theme', this.theme); } catch (e) {}
    },
    ifaceTitle() { const i = this.ifaces.find(x => x.name === this.ifaceName); return i ? i.name + ' - ' + i.ip : 'Custom target'; },
    pickIface() {
      const i = this.ifaces.find(x => x.name === this.ifaceName);
      if (i) this.cidr = i.suggested || i.cidr;
    },
    // ---- session token (v0.6 4.1) ----
    // netdiag opens the page at /#s=<token> (or /?s=<token>). The page keeps
    // the token in localStorage, which every tab on this address shares, and
    // takes it out of the address bar. A page opened later by a second launch
    // brings the current token, and the other tabs pick it up from storage.
    takeSession() {
      const m = /(?:^|[#&?])s=([A-Za-z0-9_-]+)/.exec(location.hash) || /(?:^|[?&])s=([A-Za-z0-9_-]+)/.exec(location.search);
      if (m) {
        session = m[1];
        try { localStorage.setItem('netdiag.session', session); } catch (e) {}
        try { history.replaceState(null, '', '/'); } catch (e) {}
      }
      window.addEventListener('storage', ev => {
        if (ev.key !== 'netdiag.session' || !ev.newValue) return;
        this.sessionOff = false;
        if (this.settingsLocked) this.loadSettings();
      });
    },
    sessionToken() {
      try { const t = localStorage.getItem('netdiag.session'); if (t) return t; } catch (e) {}
      return session;
    },
    // Every request that changes something, and the settings read, goes
    // through api(), which sends the session token. A 401 means this page's
    // token is out of date: buttons are off until netdiag opens a fresh page.
    // The first call that passes again also reads the settings the page
    // could not read before.
    async api(method, url, body) {
      const headers = {'X-Netdiag-Session': this.sessionToken()};
      if (method === 'POST' || method === 'PUT') headers['Content-Type'] = 'application/json';
      const r = await fetch(url, {method: method, headers: headers, body: body === undefined ? undefined : JSON.stringify(body)});
      this.sessionOff = r.status === 401;
      if (!this.sessionOff && this.settingsLocked && url !== '/api/settings') this.loadSettings();
      return r;
    },
    resetScan() {
      this.error = ''; this.hosts = []; this.warnings = []; this.drawerIP = '';
      this.stage = ''; this.done = 0; this.total = 0; this.state = ''; this.gateway = '';
      this.liveStatus = null; this.announce = ''; this.announced = {}; this.conflicts = [];
      this.seenNotices = {}; this.logSeen = {}; this.remoteMsg = {}; this.remoteShort = {}; this.remoteBusy = {};
    },
    // replace is set only by Restart watching: without it the server never
    // stops a scan that is watching the network and answers 409 instead.
    async start(replace) {
      this.error = '';
      const cidr = this.cidr.trim();
      this.focusAt = Date.now(); // the person is using this tab
      this.starting = true;
      try {
        const body = {cidr: cidr, interface: this.ifaceName, community: this.community, live: !!this.live};
        if (replace === true) body.replaceLive = true;
        const r = await this.api('POST', '/api/scans', body);
        const j = await r.json();
        if (r.status === 409 && j.watching) { this.askRestart(j.watching, true); return; }
        if (!r.ok) { this.error = j.error || ('Scan failed (HTTP ' + r.status + ')'); return; }
        this.resetScan();
        this.scanId = j.id; this.running = true; this.state = 'running';
        this.logPush('scan', (replace === true ? 'Watching restarted: ' : 'Scan started: ') + cidr + (this.live ? ' (live)' : ''));
        this.listen(j.id);
        // The other tabs follow the new scan.
        this.tellTabs({type: 'started', id: j.id});
      } catch (e) { this.error = 'Could not start scan: ' + e.message; }
      finally { this.starting = false; }
    },

    // ---- never lose netdiag (F9) ----
    // A reloaded or reopened page attaches to the scan that is running, so the
    // table, health, banners and live pill come back. The event stream
    // replays them; replayed notices rebuild their banners without a sound.
    // skip: a scan that just ended here, in case it is still reported. live:
    // attach only to a scan in live mode.
    async reattach(skip, live) {
      let sc = null;
      try {
        const r = await fetch('/api/scans/running');
        if (r.ok) sc = ((await r.json()) || {}).scan;
      } catch (e) {}
      if (!sc || !sc.id || sc.id === skip || (live && !(sc.request || {}).live)) return false;
      if (sc.id === this.scanId && this.attached()) return true;
      const q = sc.request || {};
      this.resetScan();
      if (q.cidr) this.cidr = q.cidr;
      if (q.interface) this.ifaceName = this.ifaces.some(i => i.name === q.interface) ? q.interface : '';
      this.live = !!q.live;
      this.scanId = sc.id; this.running = true; this.state = sc.state || 'running';
      this.logPush('scan', 'Reconnected to the running scan: ' + (q.cidr || '') + (q.live ? ' (live)' : ''));
      this.listen(sc.id, true);
      return true;
    },
    // A tab that is not following the scan that is watching the network
    // picks it up, so its alerts reach the tab the person uses: when another
    // tab starts one, when the heartbeat answers, when the tab is focused and
    // when its own scan ends (Restart watching in another tab ends it). Only
    // a scan in live mode: a single scan ends by itself, and the person may
    // still be reading the last results. force: another tab started a scan
    // (or netdiag answers again after it seemed stopped), so a tab still
    // attached to the old one looks too. Never while the restart dialog asks
    // the person.
    async follow(force, skip) {
      if (this.starting || this.quitted || this.backend !== 'ok') return false;
      if (this.attached() && !force) return false;
      const d = this.$refs.restartDlg;
      if (d && d.open) return false;
      return this.reattach(skip, true);
    },
    // Restart watching, and the answer to a 409 from a tab that did not know
    // a scan was watching (stale). w is the server's watching scan.
    async askRestart(w, stale) {
      const opener = document.activeElement;
      if (!w) {
        try {
          const r = await fetch('/api/scans/running');
          const sc = r.ok ? ((await r.json()) || {}).scan : null;
          if (sc) w = {id: sc.id, cidr: (sc.request || {}).cidr, since: sc.liveSince};
        } catch (e) {}
      }
      this.watching = w || {id: this.scanId}; this.restartStale = !!stale;
      restartOpener = opener;
      this.$nextTick(() => {
        const d = this.$refs.restartDlg;
        if (d && !d.open) d.showModal();
        const k = document.getElementById('restart-keep');
        if (k) k.focus();
      });
    },
    restartSince() {
      const t = Date.parse((this.watching || {}).since || '');
      return isNaN(t) ? '' : ' (since ' + new Date(t).toTimeString().slice(0, 5) + ')';
    },
    closeRestart() { const d = this.$refs.restartDlg; if (d && d.open) d.close(); },
    // Focuses the first of els that is shown, trying for a few frames while
    // x-show catches up.
    focusShown(els, tries) {
      const el = els.find(e => e && e.isConnected && e.offsetParent !== null);
      if (el) { el.focus(); return; }
      if ((tries || 0) < 10) requestAnimationFrame(() => this.focusShown(els, (tries || 0) + 1));
    },
    // Keep watching (also Esc): a stale tab attaches to the scan that is
    // watching. Focus goes back to the button that opened the dialog, or to
    // Restart watching when that button is gone.
    async keepWatching() {
      const stale = this.restartStale, btn = document.getElementById('restart-btn');
      this.closeRestart();
      this.focusAt = Date.now();
      if (stale && await this.reattach()) this.focusShown([btn]);
      else this.focusShown([restartOpener, btn]);
    },
    async restartWatching() {
      this.closeRestart();
      this.focusShown([restartOpener, document.getElementById('restart-btn')]);
      await this.start(true);
    },

    // The heartbeat asks GET /api/identity every 15 s while the tab is
    // visible, and every 5 s after a failure or an event stream error. Two
    // failures in a row mean netdiag has stopped; another instanceId means
    // it was restarted and this page is out of date.
    startHeartbeat() {
      document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') this.hbSchedule(0); });
      this.hbTick();
    },
    hbSchedule(ms) { clearTimeout(hbTimer); hbTimer = setTimeout(() => this.hbTick(), ms); },
    async hbTick() {
      if (this.quitted || this.backend === 'restarted') return;
      if (document.visibilityState !== 'hidden' || this.sseErr || this.hbFails) await this.beat();
      this.hbSchedule(this.sseErr || this.hbFails ? 5000 : 15000);
    },
    async beat() {
      let j = null, t = 0, due = Date.now() + 4000;
      const ctl = typeof AbortController === 'undefined' ? null : new AbortController();
      // The call gets 4 s. A timer that fires late means the page itself was
      // busy (drawing a big table), and the answer may be waiting: give it
      // another 4 s rather than count it as netdiag not answering.
      const arm = () => { t = setTimeout(() => {
        if (Date.now() - due < 1000) { ctl.abort(); return; }
        due = Date.now() + 4000; arm();
      }, due - Date.now()); };
      if (ctl) arm();
      try {
        const r = await fetch('/api/identity', {cache: 'no-store', signal: ctl ? ctl.signal : undefined});
        if (r.ok) j = await r.json();
      } catch (e) {}
      clearTimeout(t);
      if (this.quitted) return;
      if (!j || j.app !== 'netdiag') {
        this.hbFails++;
        if (this.hbFails >= 2 && this.backend === 'ok') this.backend = 'stopped';
        return;
      }
      this.hbFails = 0;
      if (!this.instanceId) this.instanceId = j.instanceId || '';
      if (j.instanceId && j.instanceId !== this.instanceId) { this.backend = 'restarted'; return; }
      const back = this.backend === 'stopped';
      if (back) this.backend = 'ok';
      // A scan started by an agent or in another browser: follow it. When
      // netdiag answers again, look even while attached: another tab's word
      // that it started a scan may have come while this tab could not follow.
      this.follow(back);
    },
    reloadPage() { window.location.reload(); },

    // Every open netdiag tab shows the banners, but only one plays the sound
    // and raises desktop pop-ups: of the tabs following the running scan, the
    // one the person focused or used last. The tabs tell each other on a
    // BroadcastChannel: offer (id, and at: its last focus), bye (it stopped
    // following or closed), who (a new tab asks for offers) and started (a
    // scan started: follow it).
    initTabs() {
      this.tabId = Math.random().toString(36).slice(2) + Date.now().toString(36);
      this.focusAt = Date.now();
      if (typeof BroadcastChannel === 'undefined') return;
      try { tabs = new BroadcastChannel('netdiag'); } catch (e) { return; }
      tabs.onmessage = ev => {
        const m = ev.data || {};
        if (m.type === 'offer' && m.id) this.adoptBeeper({id: m.id, at: m.at || 0});
        else if (m.type === 'bye' && this.beeper && m.id === this.beeper.id) { this.beeper = null; this.offerBeeper(); }
        else if (m.type === 'who') this.offerBeeper();
        else if (m.type === 'started' && m.id && m.id !== this.scanId) this.follow(true);
      };
      const focused = () => {
        this.focusAt = Date.now();
        if (this.attached()) this.offerBeeper(); else this.follow(false);
      };
      window.addEventListener('focus', focused);
      window.addEventListener('pageshow', ev => { if (ev.persisted) focused(); });
      window.addEventListener('pagehide', () => this.leaveBeeper());
      this.tellTabs({type: 'who'});
    },
    tellTabs(m) { if (tabs) try { tabs.postMessage(m); } catch (e) {} },
    // The latest focus wins; a tie goes to the larger id, so every tab picks
    // the same one.
    adoptBeeper(c) {
      const b = this.beeper;
      if (!b || c.id === b.id || c.at > b.at || (c.at === b.at && c.id > b.id)) this.beeper = c;
    },
    offerBeeper() {
      if (!this.attached() || this.settingsLocked) return;
      const c = {id: this.tabId, at: this.focusAt};
      this.adoptBeeper(c);
      this.tellTabs({type: 'offer', id: c.id, at: c.at});
    },
    // This tab stopped following the scan, or it is closing: the tabs still
    // following choose again.
    leaveBeeper() {
      if (this.beeper && this.beeper.id === this.tabId) this.beeper = null;
      this.tellTabs({type: 'bye', id: this.tabId});
    },
    // The tab is listening to the running scan (only one runs at a time).
    attached() { return !!this.es && this.running; },
    // A tab that is not following the scan never beeps, nor one that may not
    // read the settings (its token is out of date). No beeper known (it
    // closed) means every following tab may beep until one offers again.
    isBeeper() { return this.attached() && !this.settingsLocked && (!this.beeper || this.beeper.id === this.tabId); },

    // resumed: attaching to a scan that was already running (reattach), so
    // the replay is not announced as a new start.
    listen(id, resumed) {
      if (this.es) this.es.close();
      const es = new EventSource('/api/scans/' + id + '/events');
      this.es = es; this.liveSynced = false;
      this.offerBeeper();
      const on = (t, fn) => es.addEventListener(t, ev => { try { fn(JSON.parse(ev.data)); } catch (e) {} });
      on('host', h => {
        if (h.isNew && !this.logSeen['h:' + h.ip]) {
          this.logSeen['h:' + h.ip] = true;
          if (this.liveSynced) this.logPush('host', 'New device: ' + (h.ip + ' ' + (h.vendor || '')).trim(), h.ip);
        }
        this.upsert(h);
      });
      on('health', r => { this.logHealth(this.health, r); this.health = r; });
      on('conflict', ev => this.onConflict(ev));
      on('notice', n => this.onNotice(n, id));
      on('progress', p => { this.stage = p.stage; this.done = p.done; this.total = p.total; });
      on('stage', s => { this.stage = s.stage; this.done = 0; this.total = 0; this.logPush('scan', 'Stage: ' + s.stage); });
      on('snapshot', r => {
        this.gateway = r.gateway || ''; this.warnings = r.warnings || []; this.state = r.state;
        if (r.health) this.health = r.health;
        if (r.conflicts) this.conflicts = r.conflicts;
        (r.hosts || []).forEach(h => { if (!this.hosts.some(x => x.ip === h.ip)) this.hosts.push(h); });
      });
      on('live', st => {
        this.liveStatus = st; this.state = 'live'; this.stage = 'live'; this.running = true;
        const fresh = this.hosts.filter(h => h.isNew && !this.announced[h.ip]);
        fresh.forEach(h => { this.announced[h.ip] = true; });
        // The first live event of a (re)connection only syncs; replayed hosts are not news.
        if (this.liveSynced && fresh.length) {
          this.announce = fresh.map(h => ('New device: ' + h.ip + ' ' + (h.vendor || '')).trim()).join('. ');
        }
        if (!this.liveSynced && !resumed) this.logPush('scan', 'Live monitoring started');
        this.liveSynced = true;
      });
      // Presence events are live-only (never replayed), so a reconnect does
      // not re-announce old transitions.
      on('presence', p => {
        const h = this.hosts.find(x => x.ip === p.ip) || {};
        this.logPush(p.online ? 'online' : 'offline', p.online ? 'Back online: ' + p.ip : ('Went offline: ' + p.ip + ' ' + (h.vendor || '')).trim() + (p.reason ? ' (' + p.reason + ')' : ''), p.ip);
        this.announce = p.online ? 'Device back online: ' + p.ip
                                 : ('Device went offline: ' + p.ip + ' ' + (h.vendor || '')).trim();
      });
      on('done', r => {
        // Only the stream this tab follows ends its attach (listen closes an
        // old one, which then sends nothing more).
        if (es !== this.es) return;
        this.liveStatus = null;
        if (r.health) this.health = r.health;
        if (r.conflicts) this.conflicts = r.conflicts;
        this.hosts = r.hosts || this.hosts; this.gateway = r.gateway || ''; this.warnings = r.warnings || [];
        this.state = r.state; this.running = false; this.stage = 'finished';
        this.logPush('scan', 'Scan ' + (r.state === 'cancelled' ? 'cancelled' : 'finished') + ': ' + this.hosts.length + ' hosts');
        es.close(); this.es = null;
        this.leaveBeeper();
        // Restart watching in another tab ends this scan: follow the new one.
        this.follow(false, id);
      });
      on('error', m => { this.error = (m && m.message) || 'Scan error'; this.logPush('error', this.error); });
      es.onopen = () => { this.sseErr = false; };
      es.onerror = () => {
        if (es !== this.es) return;
        if (es.readyState === EventSource.CLOSED) { this.running = false; this.leaveBeeper(); }
        // The stream broke: check that netdiag is still there sooner.
        if (!this.sseErr) { this.sseErr = true; if (!this.hbFails) this.hbSchedule(5000); }
      };
    },
    upsert(h) {
      const i = this.hosts.findIndex(x => x.ip === h.ip);
      if (i >= 0) this.hosts[i] = h; else this.hosts.push(h);
    },
    async cancel() { if (this.scanId) await this.api('POST', '/api/scans/' + this.scanId + '/cancel'); },
    exportAs(f) { if (this.scanId) window.location.href = '/api/scans/' + this.scanId + '/export?format=' + f; },
    async quit() {
      let r = null;
      try { r = await this.api('POST', '/api/quit'); } catch (e) {}
      // A page whose token is out of date cannot quit netdiag: it keeps
      // watching, and the page says why.
      if (r && r.status === 401) return;
      if (this.es) this.es.close();
      this.running = false; this.quitted = true;
      clearTimeout(hbTimer);
      this.leaveBeeper();
    },

    stageLabel() {
      const names = {icmp: 'Ping sweep', arp: 'ARP table', probe: 'Probing services',
                     live: 'Initial scan complete - watching for new devices',
                     finished: this.state === 'cancelled' ? 'Cancelled' : 'Finished'};
      if (!this.stage) return 'Idle';
      const n = names[this.stage] || this.stage;
      return this.total > 0 && this.stage !== 'finished' ? n + ' (' + this.done + '/' + this.total + ')' : n;
    },
    percent() {
      if (this.stage === 'live') return 100;
      if (this.stage === 'finished') return this.state === 'done' ? 100 : 0;
      return this.total > 0 ? Math.round(100 * this.done / this.total) : 0;
    },
    stats() {
      return {alive: this.hosts.filter(h => h.alive).length,
              printers: this.hosts.filter(h => h.isPrinter).length,
              web: this.hosts.filter(h => (h.web || []).length).length};
    },
    notices() {
      const n = [...this.warnings];
      if (this.state === 'done' || this.state === 'live') {
        const others = this.hosts.filter(h => h.alive && !h.isSelf && h.ip !== this.gateway);
        const gw = this.hosts.some(h => h.ip === this.gateway && h.alive);
        if (gw && !others.length) n.push('Only the router responded - client isolation may be enabled on this WiFi.');
      }
      return n;
    },
    emptyText() {
      if (this.state === 'live' && this.hosts.length) return 'No hosts match this filter.';
      if (this.state === 'live') return 'No hosts found yet. Watching for new devices...';
      if (this.running) return 'Scanning... hosts will appear as they are found.';
      if (this.state === 'done' && this.hosts.length) return 'No hosts match this filter.';
      if (this.state === 'done') return 'No hosts found.';
      return '> no scan yet - pick a target and press Scan';
    },

    isOffline(h) { return !!h.alive && h.online === false && !h.isSelf; },
    offlineFor(h) {
      const t = Date.parse(h.offlineSince || '');
      if (isNaN(t)) return 'offline';
      const s = Math.max(0, Math.round((this.nowMs - t) / 1000));
      if (s < 60) return 'offline for ' + s + 's';
      const m = Math.floor(s / 60);
      if (m < 60) return 'offline for ' + m + 'm';
      return 'offline for ' + Math.floor(m / 60) + 'h ' + (m % 60) + 'm';
    },
    // The low-supply level from Settings (Alerts > Supply threshold), so the
    // Problems filter and the "Supply low" alert agree. 15 until settings load.
    supplyThreshold() {
      const t = Number(this.settings && this.settings.alerts && this.settings.alerts.supplyThresholdPct);
      return Number.isFinite(t) && t >= 1 && t <= 100 ? t : 15;
    },
    isProblem(h) {
      if (this.isOffline(h)) return true;
      if (this.hasConflict(h)) return true;
      const p = h.printer;
      if (h.isPrinter && p) {
        if ((p.alerts || []).length) return true;
        const thr = this.supplyThreshold();
        if ((p.supplies || []).some(s => s.state === 'empty' || s.state === 'low' || (s.percent >= 0 && s.percent <= thr))) return true;
      }
      return (h.web || []).some(w => w.error || !w.status || w.status >= 400);
    },
    matches(h, f) {
      if (f === 'alive') return !!h.alive;
      if (f === 'mdns') return !!h.mdns;
      if (f === 'printers') return h.isPrinter;
      if (f === 'web') return (h.web || []).length > 0;
      if (f === 'problems') return this.isProblem(h);
      if (f === 'new') return !!h.isNew;
      if (f === 'conflicts') return this.hasConflict(h);
      if (f === 'offline') return this.isOffline(h);
      return true;
    },
    hostText(h) {
      return [h.ip, h.hostname, this.mdnsName(h), h.mac, h.vendor].filter(Boolean).join(" ").toLowerCase();
    },
    count(f) { return this.hosts.filter(h => this.matches(h, f)).length; },
    ipNum(ip) { return ip.split('.').reduce((a, o) => a * 256 + (+o || 0), 0); },
    sortVal(h) {
      if (this.sortKey === 'vendor') return (h.vendor || '￿').toLowerCase();
      if (this.sortKey === 'status') return (h.isPrinter ? 0 : 1) * 10 + (h.alive ? 0 : 5) + (this.isProblem(h) ? -1 : 0);
      return this.ipNum(h.ip);
    },
    visible() {
      const k = this.sortDir;
      const q = this.search.trim().toLowerCase();
      return this.hosts.filter(h => this.matches(h, this.filter) && (!q || this.hostText(h).includes(q))).sort((a, b) => {
        const x = this.sortVal(a), y = this.sortVal(b);
        return (x < y ? -1 : x > y ? 1 : this.ipNum(a.ip) - this.ipNum(b.ip)) * k;
      });
    },
    sortBy(key) {
      if (this.sortKey === key) this.sortDir = -this.sortDir; else { this.sortKey = key; this.sortDir = 1; }
    },
    ariaSort(key) { return this.sortKey === key ? (this.sortDir > 0 ? 'ascending' : 'descending') : 'none'; },
    arrow(key) { return this.sortKey === key ? (this.sortDir > 0 ? ' ▲' : ' ▼') : ''; },
    // The storm guard (v0.6 F9c) says why netdiag holds: this computer lost
    // the network or the router went quiet (checks paused, back by
    // themselves), or this computer is on another network (alerts stopped
    // until Restart watching).
    liveText() {
      const st = this.liveStatus || {};
      if (st.held) {
        if (st.heldReason === 'network_changed') return 'Live · alerts stopped: this computer is on a different network now';
        const since = this.clock(st.heldSince) ? ' (since ' + this.clock(st.heldSince) + ')' : '';
        if (st.heldReason === 'gateway') return 'Live · checks paused: the router stopped answering' + since;
        return 'Live · checks paused: this computer lost the network' + since;
      }
      let last = 'first check pending';
      if (st.lastCheckAt) {
        const s = Math.max(0, Math.round((this.nowMs - Date.parse(st.lastCheckAt)) / 1000));
        last = 'checked ' + s + 's ago';
      }
      return 'Live · ' + (st.newHosts || 0) + ' new · ' + (st.offline || 0) + ' offline' + ' · ' + last +
        ' · watching ' + (st.unseen || 0) + ' addresses';
    },
    typeInfo: {gateway: 'Gateway', printer: 'Printer', computer: 'Computer', phone: 'Phone/tablet', tv: 'TV/streamer', nas: 'NAS/storage',
               camera: 'Camera', network: 'Network gear', iot: 'Smart home/IoT', server: 'Server/web device', self: 'This computer', unknown: 'Unknown'},
    typeOf(h) {
      if (h.deviceType && this.typeInfo[h.deviceType]) return {icon: 'type-' + h.deviceType, label: this.typeInfo[h.deviceType]};
      if (h.isPrinter) return {icon: "type-printer", label: "Printer"};
      if (h.ip === this.gateway) return {icon: "type-gateway", label: "Gateway"};
      if (h.isSelf) return {icon: "type-self", label: "This computer"};
      if ((h.web || []).length) return {icon: "type-server", label: "Web server"};
      return {icon: "type-unknown", label: "Device"};
    },
    // Printer alerts outrank "up": critical/error alerts are bad, others warn.
    printerSev(h) {
      const al = (h.isPrinter && h.printer && h.printer.alerts) || [];
      if (al.some(a => a.severity === 'critical' || a.severity === 'error')) return 'bad';
      return al.length ? 'warn' : '';
    },
    statusIcon(h) {
      if (this.isOffline(h)) return 'circle-x';
      const p = this.printerSev(h);
      if (p) return p === 'bad' ? 'circle-x' : 'triangle-alert';
      return h.alive ? 'circle-check' : 'circle-help';
    },
    statusClass(h) {
      if (this.isOffline(h)) return 's-bad';
      const p = this.printerSev(h);
      if (p) return 's-' + p;
      return h.alive ? 's-ok' : 's-unknown';
    },
    statusShape(s) { return {ok: 'circle-check', warn: 'triangle-alert', fail: 'circle-x', info: 'info'}[s] || 'circle-help'; },
    statusShapeClass(s) { return {ok: 's-ok', warn: 's-warn', fail: 's-bad', info: 's-info'}[s] || 's-unknown'; },
    checkIcon(id) { return {gateway: 'router', dhcp: 'network', dns: 'book-search', internet: 'cloud', wifi: 'wifi', webserver: 'server'}[id] || 'activity'; },
    sevIcon(sev) { return {critical: 'circle-x', error: 'circle-x', warn: 'triangle-alert', info: 'info', ok: 'circle-check'}[sev] || 'info'; },
    sevClass(sev) { return {critical: 's-bad', error: 's-bad', warn: 's-warn', ok: 's-ok'}[sev] || 's-info'; },
    kindIcon(k) { return {scan: 'radar', host: 'circle-plus', offline: 'wifi-off', online: 'wifi', conflict: 'triangle-alert', health: 'heart-pulse', notice: 'bell', error: 'circle-x'}[k] || 'info'; },
    hostName(h) { return h.hostname || this.mdnsName(h); },
    dotClass(h) { return this.isOffline(h) ? "st-off" : h.alive ? "st-up" : "st-down"; },
    dotText(h) {
      if (this.isOffline(h)) return "Offline";
      const p = this.printerSev(h);
      const base = h.alive ? "Up" : "No ping";
      return p ? base + ", printer " + (p === "bad" ? "error" : "warning") : base;
    },
    drawerHost() { return this.hosts.find(h => h.ip === this.drawerIP) || null; },
    drawerHosts() { const h = this.drawerHost(); return h ? [h] : []; },
    rowClick(ev, ip) {
      if (ev.target.closest("a, button, input")) return;
      this.openDrawer(ip);
    },
    openDrawer(ip) {
      this.drawerIP = ip;
      this.$nextTick(() => { const b = this.$refs.drawerClose; if (b) b.focus(); });
    },
    closeDrawer(noFocus) {
      const ip = this.drawerIP;
      if (!ip) return;
      this.drawerIP = "";
      if (noFocus) return;
      this.$nextTick(() => {
        const rows = document.querySelectorAll("#hosts button.rowbtn");
        for (const b of rows) { if (b.dataset.ip === ip) { b.focus(); return; } }
        const m = document.getElementById("main"); if (m) m.focus();
      });
    },
    async copy(text, what) {
      try { await navigator.clipboard.writeText(text); }
      catch (e) {
        const t = document.createElement("textarea");
        t.value = text; t.setAttribute("readonly", ""); t.style.position = "fixed"; t.style.opacity = "0";
        document.body.appendChild(t); t.select();
        try { document.execCommand("copy"); } catch (e2) { /* ignore */ }
        document.body.removeChild(t);
      }
      this.announce = "Copied " + what + " " + text;
    },

    printerSource(p) {
      return {ipp: 'via IPP', snmp: 'via SNMP', 'snmp+ipp': 'via SNMP + IPP'}[p && p.source] || '';
    },
    mdnsName(h) { return h.mdns ? (h.mdns.instance || h.mdns.hostname || '') : ''; },
    statusWord(h) { return this.isOffline(h) ? 'offline' : h.alive ? 'up' : 'no ping'; },
    // Printer details for the Status column, one short line each.
    printerLines(h) {
      if (!h.isPrinter || !h.printer) return [];
      const lines = [];
      if (h.printer.model) lines.push(h.printer.model);
      const al = h.printer.alerts || [];
      if (al.length) lines.push(al.slice(0, 2).map(a => a.description || a.code).join(', ') + (al.length > 2 ? ' +' + (al.length - 2) + ' more' : ''));
      const low = (h.printer.supplies || []).filter(s => s.state === 'low' || s.state === 'empty').length;
      if (low) lines.push(low + ' supply low/empty');
      return lines;
    },
    webNote(w) {
      if (w.error) return 'unreachable (' + w.error + ')';
      return [w.status ? 'HTTP ' + w.status : '', w.title, w.server].filter(Boolean).join(' - ');
    },
    async loadRemoteCaps() {
      try { const r = await fetch('/api/remote'); if (r.ok) this.remoteCaps = await r.json(); }
      catch (e) { /* remote access stays copy-only */ }
    },
    // Remote access services on a device, from its open ports, with what
    // this computer can open them with.
    remoteServices(h) {
      const c = this.remoteCaps, ports = h.openPorts || [], out = [], self = !!h.isSelf, local = this.localIP(h.ip);
      const selfNote = 'This is this computer, so there is nothing to connect to.';
      const farNote = 'This address is not on a local network, so netdiag does not open sessions to it.';
      if (ports.includes(3389)) out.push({kind: 'rdp', port: 3389, label: 'Remote desktop', canOpen: !!c.rdp && !self && local, local,
        openLabel: 'Open remote desktop', copy: h.ip, copyWhat: 'address', copyLabel: 'Copy address',
        help: self ? selfNote : !local ? farNote : c.rdp ? 'Opens ' + c.rdpApp + '. You will need a user name and password for that computer.'
          : c.platform === 'darwin' ? "Install Microsoft's free Windows App from the App Store to open it here, or download the connection file."
          : 'Download the connection file and open it with a Remote Desktop app.'});
      if (ports.includes(5900)) out.push({kind: 'vnc', port: 5900, label: 'Screen sharing (VNC)', canOpen: !!c.vnc && !self && local, local,
        openLabel: 'Open screen sharing', copy: h.ip, copyWhat: 'address', copyLabel: 'Copy address',
        help: self ? selfNote : !local ? farNote : c.vnc ? 'Opens ' + c.vncApp + '. You will need a user name and password for that computer, or its screen sharing password.'
          : "netdiag can't open screen sharing on this computer. Copy the address into your VNC viewer."});
      if (ports.includes(22)) out.push({kind: 'ssh', port: 22, label: 'SSH', canOpen: false,
        copy: 'ssh ' + h.ip, copyWhat: 'SSH command', copyLabel: 'Copy SSH command',
        help: 'A command-line login for your IT helper. netdiag does not open it.'});
      return out;
    },
    // Mirrors remote.Local: sessions open only to private, link-local and
    // 100.64.0.0/10 addresses.
    localIP(ip) {
      const p = String(ip).split('.').map(Number);
      if (p.length !== 4 || p.some(n => !Number.isInteger(n) || n < 0 || n > 255)) return false;
      return p[0] === 10 || (p[0] === 172 && p[1] >= 16 && p[1] <= 31) || (p[0] === 192 && p[1] === 168) ||
        (p[0] === 169 && p[1] === 254) || (p[0] === 100 && p[1] >= 64 && p[1] <= 127);
    },
    remoteLinks(h) { return this.remoteServices(h).filter(r => r.canOpen); },
    rdpFileURL(h) { return '/api/scans/' + encodeURIComponent(this.scanId) + '/remote.rdp?ip=' + encodeURIComponent(h.ip); },
    async openRemote(h, r) {
      const key = h.ip + ':' + r.kind, what = r.kind === 'rdp' ? 'Remote desktop' : 'Screen sharing';
      // One session per click: ignore repeat clicks while it starts.
      if (this.remoteBusy[key]) return;
      this.remoteBusy[key] = true;
      this.remoteMsg[key] = what + ' is starting...';
      this.remoteShort[key] = 'Starting...';
      let msg, ok = false;
      try {
        const res = await this.api('POST', '/api/scans/' + encodeURIComponent(this.scanId) + '/remote', {ip: h.ip, kind: r.kind});
        ok = res.ok;
        if (ok) msg = what + ' to ' + h.ip + ' is opening. If no window appears, look for it ' +
          (this.remoteCaps.platform === 'darwin' ? 'in the Dock.' : 'on the taskbar.');
        else {
          const b = await res.json().catch(() => ({}));
          msg = b.noApp ? 'This computer has no app that opens it.' + (r.kind === 'rdp' ? ' Download the connection file instead.' : '')
            : 'Could not open it: ' + (b.error || 'HTTP ' + res.status);
        }
      } catch (e) { msg = 'Could not open it: ' + e.message; }
      this.remoteMsg[key] = msg;
      this.remoteShort[key] = ok ? 'Opening: look for its window' + (this.remoteCaps.platform === 'darwin' ? ' in the Dock' : ' on the taskbar') : '';
      // The drawer has its own status line; elsewhere use the page's live region.
      if (this.drawerIP !== h.ip) this.announce = msg;
      setTimeout(() => { this.remoteBusy[key] = false; }, ok ? 5000 : 0);
      if (!ok && this.drawerIP !== h.ip) this.openDrawer(h.ip);
    },
    supplyText(s) {
      const st = s.state && s.state !== 'ok' ? ' (' + s.state + ')' : '';
      return (s.percent >= 0 ? s.percent + '%' : 'unknown') + st;
    },
    trayLevel(t) {
      if (t.level < 0 || !t.max) return 'level unknown';
      return Math.round(100 * t.level / t.max) + '%';
    },
    snmpRows(h) {
      const s = h.snmp || {};
      const m = [['Version', s.version], ['Name', s.sysName], ['Description', s.sysDescr], ['Object ID', s.sysObjectID],
        ['Location', s.sysLocation], ['Contact', s.sysContact], ['Uptime', s.sysUpTime], ['Device', s.hrDeviceDescr],
        ['Device status', s.hrDeviceStatus], ['Printer status', s.hrPrinterStatus],
        ['Error state', (s.hrPrinterDetectedErrorState || []).join(', ')]];
      return m.filter(r => r[1]);
    },
  }));
});
