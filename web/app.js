let tileRaf = 0;
let tileCur = {}; // non-reactive copy of the tile values, so tweening does not retrigger its own effect
let hbTimer = 0; // next heartbeat
let tabs = null; // BroadcastChannel shared by every open netdiag tab
let restartOpener = null; // element that opened the restart dialog
let session = ''; // this launch's session token, when the browser keeps no storage
let pairTimer = 0; // next poll of the team chat pairing request
let wizEpoch = 0; // counts team chat wizard openings and closings
let pairAsking = 0; // team chat pairing requests on their way
let pnTimer = 0; // pending re-read of the Problems right now card
let drawerOpener = null; // element that opened the device drawer (a banner's or card's Show device, a row)
document.addEventListener('alpine:init', () => {
  Alpine.data('app', () => ({
    ifaces: [], ifaceName: '', cidr: '', community: 'public',
    scanId: '', running: false, es: null,
    hosts: [], gateway: '', warnings: [], state: '',
    stage: '', done: 0, total: 0, error: '', quitted: false,
    filter: 'all', sortKey: 'ip', sortDir: 1, drawerIP: '', search: '',
    live: true, liveStatus: null, nowMs: Date.now(), announce: '', announced: {}, liveSynced: false,
    theme: document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark',
    view: 'devices', navOpen: false, views: ['devices', 'health', 'activity', 'history'], health: null, healthOpen: true, healthBusy: false, healthMsg: '', healthAt: '',
    conflicts: [], banners: [], seenNotices: {},
    settings: null, draft: null, draftLoaded: false, noInternetHere: false, settingsError: '', storeDown: false, desktopNote: '', audio: null, repairedNote: '', repairedPending: [], repairedShown: [],
    history: [], historyError: '', baseId: '', currentId: '', diff: null, diffError: '', diffBusy: false,
    shown: {},
    remoteCaps: {platform: '', rdp: false, vnc: false}, remoteMsg: {}, remoteShort: {}, remoteBusy: {},
    sessionOff: false, settingsLocked: false,
    chat: null, chatMsg: '', chatMsgKind: '', chatMsgState: '', chatBusy: false, chatNoteSeen: 0, chatNoteRun: '', wiz: {step: 'closed', mode: 'connect', wait: '', servers: [], tray: null, address: '', connString: '', insp: null, error: '', busy: false,
      code: '', expiresAt: '', adminUrl: '', channels: [], channelId: '', channel: null, testMsg: '', testKind: '', title: 'Connect to team chat', fresh: false, stopped: '', retry: ''},
    problems: null, pnAll: false, pnLimit: 5, pnDismissed: {},
    watching: null, watchedCidr: '', restartStale: false, backend: 'ok', instanceId: '', hbFails: 0, sseErr: false, tabId: '', beeper: null, focusAt: 0, starting: false,
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
      const ids = [['gateway', 'Router'], ['dhcp', 'DHCP'], ['dns', 'DNS'], ['internet', 'Internet'], ['wifi', 'Wi-Fi'], ['webserver', 'Web server']];
      return ids.map(([id, label]) => {
        const c = by[id];
        if (!c) return {id, label, icon: this.checkIcon(id), mark: 'unavailable', value: 'not checked'};
        const lat = this.healthLatency(c);
        return {id, label, icon: this.checkIcon(id), mark: this.healthMark(c), value: this.healthStatusText(c.status) + (lat ? ' ' + lat : '')};
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
      try { for (const k of JSON.parse(localStorage.getItem('netdiag.fixedDismissed') || '[]')) this.pnDismissed[k] = true; } catch (e) {}
      this.loadProblems();
      setInterval(() => this.loadProblems(), 60000);
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
      this.loadChat();
      setInterval(() => this.loadChat(), 30000);
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
        this.settingsArrived();
      } catch (e) {}
    },
    async loadHealth() {
      try {
        const r = await fetch('/api/health');
        if (r.ok) this.health = await r.json();
      } catch (e) {}
    },
    // from: the id of the health card whose button ran the checks. Its own
    // status line says how it went, next to the button (healthMsgFor), and
    // the button reads Checking... meanwhile; otherwise the panel's line
    // under the cards does.
    async recheck(from) {
      this.healthAt = typeof from === 'string' ? from : '';
      this.healthBusy = true; this.healthMsg = 'Running checks...';
      try {
        const r = await this.api('POST', '/api/health/run', {interface: this.ifaceName});
        if (r.ok) { this.health = await r.json(); this.healthMsg = 'Checks updated.'; this.loadProblems(); }
        else { this.healthMsg = r.status === 401 ? '' : 'Health check failed (HTTP ' + r.status + ').'; }
      } catch (e) { this.healthMsg = 'Health check failed: ' + e.message; }
      this.healthBusy = false;
      // The button was disabled while the checks ran, or it went with the
      // problem it was for: focus goes back to it, else to the card's line.
      const at = this.healthAt;
      if (at) this.$nextTick(() => {
        const a = document.activeElement;
        if (a && a !== document.body && a.isConnected) return;
        this.focusShown([document.querySelector('#hc-' + at + ' button[data-act="recheck"]'), document.getElementById('hmsg-' + at)]);
      });
    },
    // The status line of card id, or the panel's ('' for both: the other one speaks).
    healthMsgFor(id) { return this.healthAt === id ? this.healthMsg : ''; },
    healthIcon(s) { return {ok: '✔', warn: '▲', fail: '✖', info: 'ℹ'}[s] || '–'; },
    healthStatusText(s) { return {ok: 'OK', warn: 'Note', fail: 'Failed', info: 'Info', unavailable: 'Could not run'}[s] || s; },
    // A check finished unless it is unavailable: a timeout, or the router
    // or the name and web checks could not run. Wi-Fi and DHCP details this
    // computer doesn't share count as done (problems.checkDone).
    checkDone(c) {
      return c.status !== 'unavailable' || (c.id === 'wifi' || c.id === 'dhcp') && (c.data || {}).reason !== 'timeout';
    },
    // What the Problems right now card makes of check c (problems.Current),
    // as the shape the header chip, the health strip and the Health panel
    // show for it: a failed router check is urgent ('fail'), another failed
    // check or one that could not finish a warning ('warn'), a warning check
    // or details this computer doesn't share a note ('info', as info is).
    healthMark(c) {
      if (c.status === 'fail') return c.id === 'gateway' ? 'fail' : 'warn';
      if (c.status === 'unavailable') return this.checkDone(c) ? 'info' : 'warn';
      if (c.status === 'warn') return 'info';
      return c.status;
    },
    // The header chip says what the card says of the checks: how many
    // failed, how many could not run (warnings) and how many are notes.
    healthCounts() {
      const n = {failing: 0, notRun: 0, notes: 0, urgent: false};
      for (const c of (this.health && this.health.checks) || []) {
        if (c.status === 'fail') { n.failing++; n.urgent = n.urgent || c.id === 'gateway'; }
        else if (!this.checkDone(c)) n.notRun++;
        else if (c.status === 'warn' || c.status === 'unavailable') n.notes++;
      }
      return n;
    },
    healthSummary() {
      if (!this.health) return 'Health: not checked';
      const n = this.healthCounts();
      const parts = [n.failing ? n.failing + ' failing' : '', n.notRun ? n.notRun + ' could not run' : '',
        n.notes ? n.notes + (n.notes === 1 ? ' note' : ' notes') : ''].filter(Boolean);
      return 'Health: ' + (parts.length ? parts.join(', ') : 'OK');
    },
    // fail (urgent), warn, note or ok: the chip's shape, as the card's.
    healthLevel() {
      const n = this.healthCounts();
      return n.urgent ? 'fail' : n.failing || n.notRun ? 'warn' : n.notes ? 'note' : 'ok';
    },
    healthLevelShape() { return {fail: 'circle-x', warn: 'triangle-alert', note: 'info', ok: 'circle-check'}[this.healthLevel()]; },
    healthLevelClass() { return {fail: 's-bad', warn: 's-warn', note: 's-info', ok: 's-ok'}[this.healthLevel()]; },
    healthLatency(c) { return c.latencyMs > 0 ? c.latencyMs.toFixed(1) + ' ms' : ''; },
    // Every health check came back fine: none failed or warned, and none
    // could not finish (checkDone). A network without internet on purpose
    // gets here too once Settings says so (v0.6-D15).
    healthFine() {
      const c = (this.health && this.health.checks) || [];
      return c.length > 0 && c.every(x => x.status !== 'fail' && x.status !== 'warn' && this.checkDone(x));
    },
    // The Health panel's line once the checks are fine: "Everything looks
    // fine" only when the Problems right now card says so too (GET
    // /api/problems, which follows the person's alert types), else it
    // speaks for the health checks alone, so the page never shows two
    // verdicts that disagree.
    healthFineText() {
      return (this.problems && this.problems.everythingFine && !this.gone() ? 'Everything looks fine' : 'All health checks look fine') + ' (checked ' + this.healthTime() + ')';
    },

    // ---- Problems right now (v0.6 #1) ----
    // The card reads GET /api/problems when the page loads, every 60 s, and
    // 2 s after a notice, health, presence or conflict event (one read for a
    // burst of them), so it shows what netdiag thinks is wrong now.
    async loadProblems() {
      clearTimeout(pnTimer); pnTimer = 0;
      try {
        const r = await fetch('/api/problems', {cache: 'no-store'});
        if (r.ok) this.problems = await r.json();
      } catch (e) {}
    },
    problemsSoon() {
      if (!pnTimer) pnTimer = setTimeout(() => this.loadProblems(), 2000);
    },
    // gone: netdiag has stopped (or this page quit it), or it was restarted
    // and this page is out of date, so nothing here is being watched; none:
    // netdiag has looked at nothing; health: only the health checks ran
    // (Check again), no device was looked at; held: the storm guard paused
    // the checks or stopped the alerts, so the list is from before; fine:
    // nothing is a warning or worse; problems: something is; quiet: nothing
    // is so far, but the running scan has not finished checking; notrun:
    // nothing is, but the health checks did not run for this finished scan.
    pnState() {
      const p = this.problems;
      if (!p) return '';
      if (this.gone()) return 'gone';
      if (p.source === 'none') return 'none';
      if (p.source === 'health') return 'health';
      if (p.held) return 'held';
      if (p.everythingFine) return 'fine';
      if ((p.open || []).some(x => x.severity !== 'info')) return 'problems';
      return p.source === 'live' ? 'quiet' : 'notrun';
    },
    // netdiag is not there to watch: it stopped, this page quit it, or it
    // was restarted and this page is out of date. The page then says
    // nothing is watched: no live pill, no "Everything looks fine", and no
    // Stop live or Restart watching.
    gone() { return this.quitted || this.backend !== 'ok'; },
    pnGoneText() {
      if (this.backend === 'restarted' && !this.quitted) return 'netdiag was restarted, so this page is out of date. Reload the page to see what netdiag checks now.';
      // Only a check that happened has a time: netdiag may stop before it
      // looked at anything.
      const p = this.problems || {}, at = p.source !== 'none' && this.pnClock(p.checkedAt);
      return 'netdiag has stopped, so nothing is being checked.' + (at ? ' This is from the last check at ' + at + '.' : '');
    },
    // The storm guard's words, as the live pill says them (liveText).
    pnHeldWord() { return this.problems && this.problems.heldReason === 'network_changed' ? 'Stopped' : 'Paused'; },
    // The card's Restart watching while this computer is on another network,
    // so a dismissed banner does not leave the person without one.
    pnRestart() { return this.pnState() === 'held' && this.problems.heldReason === 'network_changed' && this.state === 'live'; },
    pnHeldText() {
      const p = this.problems || {}, since = this.pnClock(p.heldSince) ? ' (since ' + this.pnClock(p.heldSince) + ')' : '';
      if (p.heldReason === 'network_changed') {
        return 'This computer is on a different network now, so netdiag stopped its alerts. This is from the last check of the network it was watching, at ' + this.pnTime() + '.';
      }
      const why = p.heldReason === 'gateway' ? 'The router stopped answering' : 'This computer lost the local network';
      return why + since + ', so netdiag paused its checks. This is from the last check at ' + this.pnTime() + '.';
    },
    // Problems first (most urgent first, as the server sends them), then the
    // notes, then what was fixed in the last day and not dismissed here.
    pnList() {
      const p = this.problems || {}, open = p.open || [];
      return [...open.filter(x => x.severity !== 'info'), ...open.filter(x => x.severity === 'info'),
        ...(p.fixed || []).filter(f => f.fixedAt && !this.pnDismissed[this.pnFixedID(f)])];
    },
    pnTotal() { return this.pnList().length; },
    // The first pnLimit items unless Show all is on, grouped for the card.
    pnGroups() {
      const list = this.pnList(), shown = this.pnAll ? list : list.slice(0, this.pnLimit);
      return [
        {id: 'open', heading: '', label: 'Problems', items: shown.filter(x => !x.fixedAt && x.severity !== 'info')},
        {id: 'notes', heading: 'Also worth knowing', label: 'Also worth knowing', items: shown.filter(x => !x.fixedAt && x.severity === 'info')},
        {id: 'fixed', heading: 'Fixed in the last day', label: 'Fixed in the last day', items: shown.filter(x => x.fixedAt)},
      ].filter(g => g.items.length);
    },
    // Every item is a shape plus a word: Urgent (critical), Warning, Note,
    // Fixed: the words the banners and the Team chat alert level use too.
    pnSev(p) { return p.fixedAt ? 'ok' : p.severity === 'critical' || p.severity === 'warning' ? p.severity : 'info'; },
    pnIcon(p) { return {critical: 'circle-x', warning: 'triangle-alert', info: 'info', ok: 'circle-check'}[this.pnSev(p)]; },
    pnClass(p) { return {critical: 's-bad', warning: 's-warn', info: 's-info', ok: 's-ok'}[this.pnSev(p)]; },
    pnWord(p) { return {critical: 'Urgent', warning: 'Warning', info: 'Note', ok: 'Fixed'}[this.pnSev(p)]; },
    // A fixed item reads "Fixed at 10:42 (lasted 12 min): Printer back online"
    // ("at Fri 15:00" for a fix of an earlier day).
    pnTitle(p) {
      return p.fixedAt ? this.fixedText({at: p.fixedAt, openedAt: p.openedAt}, this.pnClock(p.fixedAt)).replace(/\.$/, '') + ': ' + p.title : p.title;
    },
    // Which one, the device and since when: "Black Toner at 15% · Front Desk (192.168.1.50) · since 10:12".
    // A fixed item says which one too: "Router", "Door or cover open".
    pnWhere(p) {
      const d = p.device || {};
      const dev = d.ip ? (d.name ? d.name + ' (' + d.ip + ')' : d.ip) : '';
      const since = !p.fixedAt && this.pnClock(p.since) ? 'since ' + this.pnClock(p.since) : '';
      const detail = p.detail || '';
      // A conflict's addresses already name its device.
      return [detail, d.ip && detail.split(', ').includes(d.ip) ? '' : dev, since].filter(Boolean).join(' · ');
    },
    // Show device for an item about a device this page lists.
    pnDevice(p) { return !p.fixedAt && !!p.device && !!p.device.ip && this.hosts.some(h => h.ip === p.device.ip); },
    pnTime() { return this.pnClock(this.problems && this.problems.checkedAt) || 'just now'; },
    // "10:42" today, "Fri 10:42" earlier this week, "10 Oct 10:42" before
    // (local time), or '': the card can speak for a saved scan of days ago.
    pnClock(at) {
      const d = new Date(at || '');
      if (isNaN(d)) return '';
      const t = this.clock(at), now = new Date(this.nowMs || Date.now());
      const day = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
      const ago = Math.round((day(now) - day(d)) / 86400000);
      if (ago === 0) return t;
      if (ago > 0 && ago < 7) return ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getDay()] + ' ' + t;
      return d.getDate() + ' ' + ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][d.getMonth()] + ' ' + t;
    },
    // The card's own lines say netdiag looked at nothing, only ran the health
    // checks, or is gone, so this line is for the other states.
    pnNotWatching() { return !!this.problems && !['', 'none', 'health', 'gone'].includes(this.pnState()) && !this.problems.watching; },
    // The line above already gives the time of the last check when all is
    // fine or held: it is not said twice.
    pnWatchText() {
      const t = this.pnState() === 'fine' || this.pnState() === 'held' ? '' : ' This is from the last check at ' + this.pnTime() + '.';
      return "netdiag isn't watching right now." + t;
    },
    // What to do to watch: tick Live mode only when it is not ticked, and
    // wait for a scan that runs once.
    pnWatchHow() { return this.running ? 'running' : this.live ? 'press' : 'tick'; },
    pnFixedID(f) { return f.key + '@' + f.fixedAt; },
    // Dismiss hides a fixed item in this browser; focus goes to the card's heading.
    pnDismiss(f) {
      this.pnDismissed[this.pnFixedID(f)] = true;
      try { localStorage.setItem('netdiag.fixedDismissed', JSON.stringify(Object.keys(this.pnDismissed).slice(-50))); } catch (e) {}
      this.$nextTick(() => { const h = document.getElementById('pn-h'); if (h) h.focus(); });
    },
    openHealth() {
      this.showView('health');
      this.$nextTick(() => this.$refs.healthPanel && this.$refs.healthPanel.focus());
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
    // Check again waits for a run in progress. The buttons that ask netdiag
    // to do something (Check again, Open Location settings) are off on a
    // page whose session is out of date, like Scan and Quit.
    actionOff(a) {
      return a.kind === 'recheck' && this.healthBusy || this.sessionOff && (a.kind === 'recheck' || a.kind === 'osSettings');
    },
    // from: the id of the health card the button is on.
    async runAction(a, from) {
      if (a.kind === 'recheck') return this.recheck(from);
      if (a.kind === 'openSettings') return this.openSettings(a.target);
      if (a.kind === 'showDevice') return this.showDevice(a.target);
      if (a.kind === 'osSettings') return this.openOSPage(a.target, from);
    },
    // The steps as the Web server card says them. The card has the Trust
    // this certificate and Use this address buttons, so its steps point at
    // the buttons there. A trust step goes when its button is not shown
    // (the certificate is for another address than the one in Settings
    // now), and the step after it loses its "Otherwise, "; the address step
    // (it ends 'set it as "Web server URL" in Settings.') names the address
    // and Use this address. Elsewhere (the banner, chat, Claude) the
    // server's words, which send people here.
    cardSteps(c) {
      const steps = (c && c.advice && c.advice.steps) || [];
      if (!c || c.id !== 'webserver') return steps;
      const use = this.webNewURL(c), out = [];
      let dropped = false;
      for (const s of steps) {
        if (/press Trust this certificate on the Web server card/.test(s)) {
          if (this.canTrust(c)) out.push('If this is your own server, press Trust this certificate below.');
          else dropped = true;
          continue;
        }
        if (use && / If that is the right page, set it as "Web server URL" in Settings\.$/.test(s)) {
          out.push((c.data.reason === 'redirect' ? 'It sends you to ' + use + '.' : 'The server sends you to ' + use + ', which uses a different certificate.') +
            ' If that is the right page, press Use this address below.');
        } else {
          out.push(dropped ? s.replace(/^Otherwise, (\w)/, (m, ch) => ch.toUpperCase()) : s);
        }
        dropped = false;
      }
      return out;
    },
    // The steps as the drawer says them: it shows Device A and Device B
    // itself, so the swap's first step (advice.StepCompareDevices) drops
    // "In netdiag, press Show device and".
    drawerSteps(adv) {
      return ((adv && adv.steps) || []).map(s => s.replace(/^In netdiag, press Show device and (\w)/, (m, ch) => ch.toUpperCase()));
    },
    async openOSPage(page, from) {
      this.healthAt = typeof from === 'string' ? from : '';
      try {
        const r = await this.api('POST', '/api/os/open', {page: page});
        this.healthMsg = r.ok ? 'Windows Settings is opening. If no window appears, look for it on the taskbar.'
          : r.status === 501 ? "netdiag can't open that page on this computer."
          : r.status === 401 ? '' : 'Could not open it (HTTP ' + r.status + ').';
      } catch (e) { this.healthMsg = 'Could not open it: ' + e.message; }
    },
    // Show device: the device's details in the Devices view. Focus goes
    // into the drawer, and back to the button that opened it when it
    // closes (closeDrawer).
    async showDevice(ip) {
      if (!ip) return;
      const from = document.activeElement;
      await this.showView('devices');
      this.openDrawer(ip, from);
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
    // The address the web server check was sent to instead, when Settings
    // could use it: a redirect's target, or the other address of the server
    // whose certificate differs. Only for the URL set in Settings now, and
    // never one with a user name in it (the check hides its password).
    webNewURL(c) {
      if (!this.settings || !c || c.id !== 'webserver' || c.status !== 'fail' || !c.data) return '';
      const d = c.data, u = d.reason === 'redirect' ? d.location : d.reason === 'tls' && !d.certSHA256 ? d.finalURL : '';
      let x = null;
      try { x = new URL(String(u || '')); } catch (e) { return ''; }
      if (!/^https?:$/.test(x.protocol) || x.username || x.password) return '';
      return this.sameWebURL(d.url, (this.settings.webServer || {}).url) ? x.href : '';
    },
    // Use this address: Settings opens with it in "Web server URL", to save
    // or change; nothing is saved until the person presses Save.
    useWebURL(c) {
      const u = this.webNewURL(c);
      if (!u) return;
      this.openSettings('webserver');
      this.draft.webServer.url = u;
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
      if (await this.putSettings(doc, false)) await this.recheck(c.id);
      else { this.healthAt = c.id; this.healthMsg = 'Could not save the trusted certificate.'; }
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
      this.makeDraft();
      const focus = {webserver: 'set-weburl', health: 'set-internet', noInternet: 'set-nointernet'}[section];
      // A team chat button's answer belongs to the last time Settings was open.
      this.chatSay('', '');
      this.loadChat();
      this.$nextTick(() => {
        const d = this.$refs.dlg; if (d && !d.open) d.showModal();
        let f = focus && document.getElementById(focus);
        // A switch that can't be turned on yet takes no focus: its help,
        // which says why, does.
        if (f && f.disabled) f = document.getElementById(f.getAttribute('aria-describedby') || '') || f;
        if (f) { f.scrollIntoView({block: 'nearest'}); f.focus(); }
        if (section === 'chat') this.focusChat();
      });
    },
    // The form's draft: a copy of the loaded settings. Without them (a page
    // whose token is out of date, the data store down, or not loaded yet)
    // the draft is a blank stand-in and draftLoaded is false, so the form
    // and Save stay hidden: saving a blank form would replace every setting.
    makeDraft() {
      this.draftLoaded = !!this.settings;
      this.draft = JSON.parse(JSON.stringify(this.settings || {community: '', webServer: {}, alerts: {triggers: {}}, ignoredConflictMACs: [], mcp: {enabled: false}}));
      this.draft.ignoredConflictMACs = this.draft.ignoredConflictMACs || [];
      this.draft.mcp = this.draft.mcp || {enabled: false};
      this.draft.mcp.enabled = !!this.draft.mcp.enabled;
      this.draft.noInternetNetworks = this.draft.noInternetNetworks || [];
      this.draft.chat = Object.assign({enabled: false, minSeverity: 'warning'}, this.draft.chat || {});
      this.noInternetHere = this.noInternetOn(this.draft);
      this.repairedNote = this.repairedText();
    },
    // The settings came in while Settings shows only why they could not be
    // read (a fresh page brought the token): the form now shows the real
    // settings, never the blank stand-in.
    settingsArrived() {
      const d = this.$refs.dlg;
      if (!d || !d.open || this.draftLoaded) return;
      this.settingsError = '';
      this.makeDraft();
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
    // Reset fields that have no control in Settings, so the notice does not
    // send people to look for them: team chat until it is set up (its switch
    // and Team chat alert level show only then), and a trusted certificate,
    // which shows only while one is set. Each says where it is instead.
    repairedOffPage: ['chat', 'chat.enabled', 'chat.minSeverity', 'webServer.trustedCert'],
    // "Some settings were not valid and were reset to their defaults: X, Y.
    // Check them below.", naming each field once. A field with no control
    // here gets a short clause that says where it is, and "Check … below"
    // then covers only the fields that are below: "Check X below." for one,
    // "Check the others below." for more.
    repairedText() {
      const rep = this.repairedPending;
      if (!rep.length) return '';
      this.repairedShown.push(...rep);
      this.repairedPending = [];
      const label = p => this.repairedLabels[p] || p;
      const names = [...new Set(rep.map(label))];
      // Team chat has controls in Settings only once it is set up.
      const off = p => this.repairedOffPage.includes(p) && !(p.startsWith('chat') && this.chatState() !== 'off');
      const here = [...new Set(rep.filter(p => !off(p)).map(label))];
      let t = 'Some settings were not valid and were reset to their defaults: ' + names.join(', ') + '.';
      if (rep.some(p => off(p) && p.startsWith('chat'))) t += ' Team chat settings show here once team chat is set up.';
      if (rep.some(p => off(p) && p === 'webServer.trustedCert')) t += " To trust the web server's certificate again, use Trust this certificate on the Health page.";
      if (here.length === names.length) t += ' Check them below.';
      else if (here.length === 1) t += ' Check ' + here[0] + ' below.';
      else if (here.length) t += ' Check the others below.';
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
      // Never save the blank stand-in over the real settings.
      if (!this.draftLoaded) return;
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
        // netdiag applied the saved Send switch before it answered: the
        // team chat status says so at once, not at the next status read.
        if (this.chat && j.chat) this.chat = Object.assign({}, this.chat, {sendAlerts: !!j.chat.enabled});
        // The alert types and the supply threshold decide what the card lists.
        this.loadProblems();
        return true;
      } catch (e) { if (showErr) this.settingsError = 'Could not save settings: ' + e.message; return false; }
    },
    removeIgnored(i) { this.draft.ignoredConflictMACs.splice(i, 1); },
    // ---- team chat (bubbles), v0.6 4.10 ----
    // The status (GET /api/bubbles, which needs the session token like the
    // settings) is read at start, every 30 s and when Settings opens. Alerts
    // reach team chat only while netdiag is watching (v0.6-D10).
    async loadChat() {
      try {
        const r = await this.api('GET', '/api/bubbles');
        if (!r.ok) return;
        this.chat = await r.json(); this.chatNotes();
        // A button's answer is about the state it was given in: once the
        // state moves on (the server answers again, or goes away), it goes.
        if (this.chatMsg && this.chatState() !== this.chatMsgState) this.chatSay('', '');
        // Another page saved Send alerts to team chat (netdiag opens a new
        // page on every launch): this page's copy of the settings is out of
        // date, so read them again. The switch in an open Settings follows,
        // unless the person changed it there.
        const on = this.chat.sendAlerts, was = !!(this.settings && this.settings.chat && this.settings.chat.enabled);
        if (typeof on !== 'boolean' || !this.settings || was === on) return;
        await this.loadSettings();
        const now = this.settings && this.settings.chat;
        if (now && this.draft && this.draft.chat && !!this.draft.chat.enabled === was) this.draft.chat.enabled = !!now.enabled;
      } catch (e) {}
    },
    // What the outbox and the polling had to say ("Team chat server moved
    // to 192.168.1.27.") goes to the Activity log under Notice, once each.
    // Ids start again when netdiag does, which the status's run says: a
    // page open across a restart takes the new run's notes from the first.
    chatNotes() {
      const notes = (this.chat && this.chat.activity) || [];
      const run = (this.chat && this.chat.run) || '';
      if (run !== this.chatNoteRun) { this.chatNoteRun = run; this.chatNoteSeen = 0; }
      const top = notes.reduce((m, n) => Math.max(m, n.id), 0);
      if (top < this.chatNoteSeen) this.chatNoteSeen = 0;
      for (const n of notes) {
        if (n.id <= this.chatNoteSeen) continue;
        this.logPush('notice', n.text);
        const e = this.log[this.log.length - 1];
        if (n.at && !isNaN(new Date(n.at))) e.t = n.at;
      }
      this.chatNoteSeen = Math.max(this.chatNoteSeen, top);
    },
    // The answer of a team chat button in Settings: kind is busy (muted
    // progress text), ok (circle-check) or warn (the yellow box), so a
    // failure is a shape plus words, never small grey text. The state it
    // was said in is kept: loadChat clears the answer once that changes.
    chatSay(msg, kind) { this.chatMsg = msg; this.chatMsgKind = msg ? kind : ''; this.chatMsgState = this.chatState(); },
    // Try now: ask the chat server at once and let what waits go.
    async chatTryNow() {
      this.chatBusy = true; this.chatSay('Trying the chat server...', 'busy');
      try {
        const r = await this.api('POST', '/api/bubbles/retry', {});
        if (r.ok) { this.chat = await r.json(); this.chatNotes(); }
        const s = this.chatState();
        if (!r.ok) this.chatSay('Could not try again (HTTP ' + r.status + ').', 'warn');
        else if (s === 'unreachable') this.chatSay("Still can't reach the chat server. netdiag keeps trying.", 'warn');
        else this.chatSay(s === 'connected' ? 'The chat server answers again.' : '', 'ok');
      } catch (e) { this.chatSay('Could not try again: ' + e.message, 'warn'); }
      this.chatBusy = false;
      this.$nextTick(() => this.focusShown([document.getElementById(this.chatState() === 'unreachable' ? 'chat-retry' : 'chat-test')]));
    },
    chatState() { return (this.chat && this.chat.state) || 'off'; },
    // Send alerts to team chat as netdiag applies it: only then does
    // anything go to chat. The status says (sendAlerts), also after another
    // page saved the switch; without it, the settings as this page read
    // them, and until those are read the page never claims it sends.
    chatSendOn() {
      const on = this.chat && this.chat.sendAlerts;
      if (typeof on === 'boolean') return on;
      return !!(this.settings && this.settings.chat && this.settings.chat.enabled);
    },
    // Connected, but Send alerts to team chat is off, so nothing is sent:
    // its own shape, colour and words, never the green "connected" ones.
    chatPaused() { return this.chatState() === 'connected' && !this.chatSendOn(); },
    // The header tool shows only once team chat is set up.
    chatShown() { return this.chatState() !== 'off'; },
    // Each state has its own shape: sending, sending off, can't reach, or
    // stopped.
    chatIcon() {
      const s = this.chatState();
      if (this.chatPaused()) return 'chat-paused';
      return s === 'connected' ? 'chat' : s === 'unreachable' ? 'chat-warn' : 'chat-off';
    },
    chatSince() { const t = this.clock(this.chat && this.chat.since); return t ? ' since ' + t : ''; },
    chatStateWord() {
      if (this.chatPaused()) return 'Connected (sending is off)';
      return {off: 'Not connected', connected: 'Connected', unreachable: "Can't reach the server" + this.chatSince(),
        needs_reconnect: 'Needs reconnecting', no_channel: 'No channel', identity_changed: 'Server identity changed'}[this.chatState()] || this.chatState();
    },
    chatTitle() {
      if (this.chatPaused()) return 'Team chat: connected, sending is off';
      const w = this.chatStateWord();
      return 'Team chat: ' + (this.chatState() === 'unreachable' ? "can't reach the server" + this.chatSince() : w.charAt(0).toLowerCase() + w.slice(1));
    },
    // The header tool's colour class: cs-paused while sending is off.
    chatToolClass() { return 'cs-' + (this.chatPaused() ? 'paused' : this.chatState()); },
    // The word the header's chat button shows next to its shape whenever
    // alerts are not getting through (sending off too); connected and
    // sending is the shape alone (its word is in the title and for screen
    // readers).
    chatToolWord() {
      if (this.chatPaused()) return 'Chat: sending off';
      return {unreachable: "Chat: can't reach", no_channel: 'Chat: no channel', needs_reconnect: 'Chat: reconnect',
        identity_changed: 'Chat: stopped'}[this.chatState()] || '';
    },
    // The button's name starts with the word it shows, so "click Chat
    // reconnect" works for voice control too, then says the state in full.
    chatToolName() {
      const w = this.chatToolWord();
      return (w ? w + '. ' : '') + this.chatTitle();
    },
    // The status word's level matches its box: yellow for what Reconnect,
    // Change channel or Try now puts right, red only for the identity
    // warning (its red box).
    chatStatusClass() {
      if (this.chatPaused()) return 'hs-info';
      return {off: 'hs-info', connected: 'hs-ok', unreachable: 'hs-warn', needs_reconnect: 'hs-warn', no_channel: 'hs-warn'}[this.chatState()] || 'hs-fail';
    },
    // "192.168.1.20", or "192.168.1.20:30000": the usual port (28225) is
    // left out wherever the page names the chat server.
    chatHost(host, port) { return host + (port && Number(port) !== 28225 ? ':' + port : ''); },
    // "Office (192.168.1.20)", the server netdiag is connected to.
    chatServerName() {
      const s = (this.chat || {}).server;
      if (!s) return '';
      const host = this.chatHost(s.host, s.port);
      return s.name ? s.name + ' (' + host + ')' : host;
    },
    // Where alerts go, in parts: "Sending to #netdiag-alerts on Office
    // (192.168.1.20)" only while they do (connected, Send alerts on); "Alerts
    // go to" while the server is away; "Alerts would go to", and how to
    // start, while Send alerts to team chat is off. Nothing when netdiag
    // stopped, or the channel takes no posts.
    chatWhere() {
      const c = this.chat || {}, ch = c.channel, st = this.chatState();
      if (!ch || !c.server || (st !== 'connected' && st !== 'unreachable')) return {lead: '', channel: '', rest: '', next: ''};
      const rest = ' on ' + this.chatServerName();
      if (!this.chatSendOn()) return {lead: 'Alerts would go to', channel: '#' + ch.name, rest, next: '. Turn on Send alerts to team chat and click Save.'};
      return {lead: st === 'connected' ? 'Sending to' : 'Alerts go to', channel: '#' + ch.name, rest, next: ''};
    },
    chatHasChannel() { return !!(this.chat && this.chat.channel); },
    // What is wrong with the connection, in words; identity changed has its
    // own box. reason tells two causes of the same state apart: a token the
    // server refused or a saved connection this computer can't open, and a
    // channel that refused netdiag's post or was archived.
    chatTrouble() {
      const s = this.chatState(), c = this.chat || {}, ch = c.channel ? '#' + c.channel.name : 'the channel';
      // Posts older than an hour are dropped (the outbox's PostTTL), and
      // Send off drops what waits: only say what really goes.
      if (s === 'unreachable') return "Can't reach the chat server" + this.chatSince() + '. netdiag keeps trying.' +
        (this.chatSendOn() ? ' Alerts from the last hour are sent when it is back.' : '');
      if (s === 'needs_reconnect') {
        const why = c.reason === 'unreadable'
          ? "netdiag can't open its saved team chat connection on this computer (it may have been copied from another computer or Windows user), so it has to be connected again."
          : 'The chat server no longer accepts netdiag (your chat admin may have removed it).';
        return why + ' Click Reconnect. Your chat admin may see a second netdiag app; they can remove the old one in Admin > Apps.';
      }
      if (s === 'no_channel') {
        if (c.reason === 'not_allowed') return "netdiag isn't allowed to post in " + ch + ' any more. Ask your chat admin to add netdiag to it again, then send a test alert, or click Change channel.';
        if (c.reason === 'archived') return 'The channel ' + ch + ' is archived. Click Change channel to choose another one.';
        if (!c.channel) return "netdiag isn't posting anywhere yet. Click Choose a channel to pick where alerts go.";
        return "netdiag isn't posting anywhere yet. Click Change channel to choose where alerts go.";
      }
      return '';
    },
    chatPending() {
      const was = (this.settings && this.settings.chat) || {enabled: false, minSeverity: 'warning'};
      const d = (this.draft && this.draft.chat) || was;
      return !!d.enabled !== !!was.enabled || (d.minSeverity || 'warning') !== (was.minSeverity || 'warning');
    },
    // Focus the team chat section's first button (the header tool's target).
    focusChat() {
      const s = this.chatState();
      const id = s === 'off' ? 'chat-connect' : s === 'needs_reconnect' || s === 'identity_changed' ? 'chat-reconnect'
        : s === 'no_channel' ? (this.chatHasChannel() ? 'chat-change' : 'chat-choose') : s === 'unreachable' ? 'chat-retry' : 'chat-test';
      const el = document.getElementById('set-chat');
      if (el) el.scrollIntoView({block: 'nearest'});
      this.focusShown([document.getElementById(id)]);
    },
    async chatTest() {
      this.chatBusy = true; this.chatSay('Sending a test alert...', 'busy');
      const t = await this.sendTest();
      // The answer is about the state the test alert left (a refused one
      // may say the channel takes no posts): read it first.
      await this.loadChat();
      this.chatSay(t.msg, t.ok ? 'ok' : 'warn');
      this.chatBusy = false;
      this.$nextTick(() => this.focusShown([document.getElementById('chat-test')]));
    },
    // Posts the test alert and closes it again; whether it worked, and the
    // answer in words.
    async sendTest() {
      try {
        const r = await this.api('POST', '/api/bubbles/test', {});
        const j = await r.json().catch(() => ({}));
        if (r.ok && j.ok) return {ok: true, msg: 'Test alert sent to #' + j.channel + ' and closed again. Check that it appeared in bubbles.'};
        return {ok: false, msg: j.error || ('Could not send the test alert (HTTP ' + r.status + ').')};
      } catch (e) { return {ok: false, msg: 'Could not send the test alert: ' + e.message}; }
    },
    // Disconnect asks first in its own dialog (4.10 Recovery: Disconnect ·
    // Cancel, with Cancel focused). Esc and Cancel leave everything as it is
    // and give focus back to Disconnect.
    askDisconnect() {
      const d = this.$refs.chatOffDlg; if (d && !d.open) d.showModal();
      this.$nextTick(() => this.focusShown([document.getElementById('chat-off-cancel')]));
    },
    closeDisconnect() { const d = this.$refs.chatOffDlg; if (d && d.open) d.close(); },
    keepChat() {
      this.closeDisconnect();
      this.focusShown([document.getElementById('chat-disconnect')]);
    },
    async chatDisconnect() {
      this.closeDisconnect();
      let msg = '', kind = 'warn';
      try {
        const r = await this.api('DELETE', '/api/bubbles');
        const j = r.ok ? {} : await r.json().catch(() => ({}));
        if (r.ok) { msg = 'Disconnected from team chat.'; kind = 'ok'; }
        else msg = j.error || 'Could not disconnect (HTTP ' + r.status + ').';
      } catch (e) { msg = 'Could not disconnect: ' + e.message; }
      // The answer is about the state Disconnect left: read it first.
      await this.loadChat();
      this.chatSay(msg, kind);
      this.$nextTick(() => this.focusChat());
    },

    // The connect wizard (<dialog id="chat-dialog">). It opens from Settings,
    // which it closes and opens again (with any changes not yet saved) when
    // it closes. mode: connect (find the server), reconnect (pair again with
    // the server netdiag knows) or channel (Change channel). wizEpoch counts
    // its openings and closings: an answer that comes back after the wizard
    // closed (or opened again) never drives it, so a closed wizard never
    // waits for the admin, connects or turns Send alerts on.
    openChatWizard(mode) {
      clearTimeout(pairTimer);
      wizEpoch++;
      this.chatSay('', '');
      // The heading says what this opening does; Change channel reads
      // Choose a channel while none was ever chosen.
      const title = mode === 'reconnect' ? 'Reconnect to team chat'
        : mode === 'channel' ? (this.chatHasChannel() ? 'Change channel' : 'Choose a channel') : 'Connect to team chat';
      Object.assign(this.wiz, {step: '', mode: mode, title: title, wait: '', servers: [], tray: null, address: '', connString: '', insp: null, error: '', busy: false,
        code: '', expiresAt: '', adminUrl: '', channels: [], channelId: '', channel: null, testMsg: '', testKind: '', fresh: false, stopped: '', retry: ''});
      const s = this.$refs.dlg; if (s && s.open) s.close();
      this.$nextTick(() => { const d = this.$refs.chatDlg; if (d && !d.open) d.showModal(); });
      if (mode === 'channel') return this.wizCheckChannels(true);
      if (mode === 'reconnect') return this.wizReconnect();
      return this.wizFind();
    },
    // Esc, Close and Cancel: stop waiting for the admin (also for a request
    // still on its way to the chat server) and go back to Settings.
    closeChatWizard() {
      clearTimeout(pairTimer);
      wizEpoch++;
      if (this.wiz.step === 'approve' || pairAsking) this.api('DELETE', '/api/bubbles/pair').catch(() => {});
      this.wiz.step = 'closed';
      const d = this.$refs.chatDlg; if (d && d.open) d.close();
      this.loadChat().then(() => this.$nextTick(() => {
        const s = this.$refs.dlg; if (s && !s.open && this.draft) s.showModal();
        this.focusChat();
      }));
    },
    wizGo(step) {
      this.wiz.step = step; this.wiz.error = '';
      // Focus the step's first shown field, ticked channel or main button
      // (the heading when it has none), trying for a few frames while x-show
      // catches up.
      const pick = (tries) => {
        if (this.wiz.step !== step) return;
        const box = document.getElementById('wiz-' + step);
        const shown = sel => [...(box ? box.querySelectorAll(sel) : [])].find(e => e.offsetParent !== null);
        const el = shown('input:not([type=radio])') || shown('input[type=radio]:checked') || shown('input[type=radio]') ||
          shown('button.primary') || shown('button, a[href]');
        if (el) el.focus();
        else if (tries < 10) requestAnimationFrame(() => pick(tries + 1));
        else { const h = document.getElementById('chat-h'); if (h) h.focus(); }
      };
      this.$nextTick(() => pick(0));
    },
    // A wizard call: the JSON answer, or null with the error shown. An
    // answer that comes after the wizard closed touches nothing on it; the
    // caller checks wizEpoch.
    // The button pressed is disabled while the call is on its way, so an
    // error that leaves the wizard on the same step gives focus back: to the
    // step's text field (to correct it), else to that button, else to the
    // step's main button. Keyboard and screen reader users keep their place.
    async wizCall(method, url, body, what) {
      const e = wizEpoch, step = this.wiz.step, from = document.activeElement;
      this.wiz.busy = true; this.wiz.error = '';
      let out = null, err = '';
      try {
        const r = await this.api(method, url, body);
        const j = await r.json().catch(() => ({}));
        if (r.ok) out = j;
        else err = j.error || (what + ' (HTTP ' + r.status + ').');
      } catch (ex) { err = what + ': ' + ex.message; }
      if (e === wizEpoch) {
        this.wiz.busy = false; this.wiz.error = err;
        if (err) this.$nextTick(() => {
          if (e !== wizEpoch || this.wiz.step !== step) return;
          const box = document.getElementById('wiz-' + step);
          if (!box) return;
          this.focusShown([box.querySelector('input[type=text]'), box.contains(from) ? from : null, box.querySelector('button.primary')]);
        });
      }
      return out;
    },
    async wizFind() {
      const e = wizEpoch;
      this.wiz.wait = 'Looking for your team chat on the local network…';
      this.wizGo('find');
      const j = await this.wizCall('POST', '/api/bubbles/discover', {interface: this.ifaceName}, 'Could not look for chat servers');
      if (e !== wizEpoch || this.wiz.step !== 'find') return;
      const err = this.wiz.error;
      if (j) {
        // The server this computer's bubbles tray uses has its own card:
        // the same server found on the local network is not listed again.
        const t = j.trayServer || null;
        this.wiz.tray = t;
        this.wiz.servers = (j.servers || []).filter(f => !(t && f.address === t.address && f.port === t.port));
      }
      this.wizGo('list');
      this.wiz.error = err;
    },
    // "192.168.1.20" for the inspected "192.168.1.20:28225", as Settings
    // and the list name the server.
    wizHost(address) {
      const m = String(address || '').match(/^\[?(.*?)\]?:(\d+)$/);
      return m ? this.chatHost(m[1], Number(m[2])) : String(address || '');
    },
    wizUse(f) { return this.wizInspect(f.address + ':' + f.port, f.onboardPort); },
    async wizInspect(address, onboardPort) {
      const e = wizEpoch;
      const j = await this.wizCall('POST', '/api/bubbles/inspect', {address: String(address).trim(), onboardPort: onboardPort || 0}, 'Could not check the chat server');
      if (j && e === wizEpoch) { this.wiz.insp = j; this.wizGo('fingerprint'); }
    },
    wizMatch() {
      const i = this.wiz.insp;
      return this.wizPair({address: i.address, fingerprint: i.fingerprint, confirmed: true}, 'match');
    },
    // Asks the chat server for netdiag's token (It matches, Reconnect, Try
    // again). When the wizard closed while the request was on its way, the
    // request is cancelled once it is known, unless a new wizard is open.
    async wizPair(body, retry) {
      const e = wizEpoch;
      pairAsking++;
      const j = await this.wizCall('POST', '/api/bubbles/pair', body, 'Could not ask the chat server');
      pairAsking--;
      if (e !== wizEpoch) {
        if (j && this.wiz.step === 'closed') this.api('DELETE', '/api/bubbles/pair').catch(() => {});
        return;
      }
      this.wizApprove(j, retry);
    },
    // Reconnect: pair again with the server and pin netdiag knows. After
    // the server's identity changed, the fingerprint is compared again first.
    async wizReconnect() {
      const e = wizEpoch;
      const s = (this.chat && this.chat.server) || null;
      const who = this.chatServerName() || 'the chat server';
      if (this.chatState() === 'identity_changed' && s) {
        this.wiz.wait = 'Asking ' + who + ' for its fingerprint…';
        this.wizGo('find');
        await this.wizInspect(s.host + ':' + s.port, s.onboardPort);
        if (e !== wizEpoch) return;
        if (this.wiz.step === 'find') { const err = this.wiz.error; this.wizGo('address'); this.wiz.address = s.host + ':' + s.port; this.wiz.error = err; }
        return;
      }
      this.wiz.wait = 'Asking ' + who + ' to let netdiag in again…';
      this.wizGo('find');
      return this.wizPair({reconnect: true}, 'reconnect');
    },
    wizApprove(j, retry) {
      if (!j) {
        if (this.wiz.step === 'find') this.wizStop(this.wiz.error, retry);
        return;
      }
      Object.assign(this.wiz, {code: j.code, expiresAt: j.expiresAt || '', adminUrl: j.adminUrl || '', retry: retry});
      this.wizGo('approve');
      this.wizPoll();
    },
    // "This code works for 9:41 more."
    wizLeft() {
      const t = Date.parse(this.wiz.expiresAt);
      if (isNaN(t)) return '';
      const s = Math.max(0, Math.round((t - this.nowMs) / 1000));
      return ' This code works for ' + Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0') + ' more.';
    },
    wizPoll() {
      clearTimeout(pairTimer);
      const e = wizEpoch;
      pairTimer = setTimeout(async () => {
        if (e !== wizEpoch || this.wiz.step !== 'approve') return;
        let j = null;
        try { const r = await this.api('GET', '/api/bubbles/pair/status'); if (r.ok) j = await r.json(); } catch (ex) {}
        if (e !== wizEpoch || this.wiz.step !== 'approve') return;
        const st = j ? j.state : 'pending';
        if (st === 'pending') return this.wizPoll();
        if (st === 'approved') {
          this.wiz.fresh = true;
          return this.wizChannels(j, false);
        }
        this.wizStop((j && j.error) || 'The request stopped.', st === 'denied' ? '' : this.wiz.retry);
      }, 2000);
    },
    wizStop(msg, retry) { this.wiz.stopped = msg; this.wiz.retry = retry || ''; this.wizGo('stopped'); },
    wizRetry() {
      if (this.wiz.retry === 'reconnect') return this.wizReconnect();
      if (this.wiz.insp) return this.wizMatch();
      return this.wizFind();
    },
    async wizConnectString() {
      const e = wizEpoch;
      const j = await this.wizCall('POST', '/api/bubbles/connect', {connectionString: this.wiz.connString.trim()}, 'Could not connect');
      if (!j || e !== wizEpoch) return;
      this.wiz.fresh = true; this.wiz.connString = '';
      this.wizChannels(j, false);
    },
    // Step 6: one channel is used by itself, several are a choice (always
    // for Change channel), none asks the admin to add netdiag to one. An
    // error after connecting (the channels could not be listed, or the
    // connection works but could not be saved) is shown on this step, whose
    // buttons try again; it never goes on to Done by itself.
    wizChannels(j, choose) {
      this.wiz.channels = j.channels || [];
      this.wiz.channelId = (j.channel && j.channel.id) || '';
      if (j.error) { this.wizGo('channel'); this.wiz.error = j.error; return; }
      // A new connection turns Send on even before it has a channel, so
      // closing at "isn't in any channel yet" and picking one later with
      // Choose a channel still sends.
      if (this.wiz.fresh) this.wizSendOn();
      if (j.channel && !(choose && this.wiz.channels.length > 1)) return this.wizReady(j.channel);
      this.wizGo('channel');
    },
    async wizCheckChannels(choose) {
      const e = wizEpoch;
      if (!this.wiz.step) { this.wiz.wait = 'Checking which channels netdiag is in…'; this.wizGo('find'); }
      const j = await this.wizCall('POST', '/api/bubbles/channel', {}, 'Could not list the channels');
      if (e !== wizEpoch) return;
      if (j) this.wizChannels(j, choose);
      else if (this.wiz.step === 'find') this.wizStop(this.wiz.error, '');
    },
    async wizPick() {
      const e = wizEpoch;
      const j = await this.wizCall('POST', '/api/bubbles/channel', {channelId: this.wiz.channelId}, 'Could not choose the channel');
      if (j && j.channel && e === wizEpoch) this.wizReady(j.channel);
    },
    // Alerts have a channel. A new connection turns Send alerts to team chat
    // on (v0.6-D38), if wizChannels has not yet; Change channel leaves it as
    // the person set it, and the Done screen says which it is.
    wizReady(ch) {
      this.wiz.channel = ch;
      this.wizGo('ready');
      if (this.wiz.fresh) this.wizSendOn();
      this.loadChat();
    },
    // Once per new connection: a person who turns Send off later keeps it off.
    async wizSendOn() {
      this.wiz.fresh = false;
      if (!this.settings || this.chatSendOn()) return;
      const doc = JSON.parse(JSON.stringify(this.settings));
      doc.chat = Object.assign({minSeverity: 'warning'}, doc.chat || {}, {enabled: true});
      if (await this.putSettings(doc, false) && this.draft) this.draft.chat = Object.assign({}, this.draft.chat || {}, {enabled: true});
    },
    async wizTest() {
      const e = wizEpoch;
      this.wiz.busy = true; this.wiz.testMsg = 'Sending a test alert...'; this.wiz.testKind = 'busy';
      const t = await this.sendTest();
      if (e !== wizEpoch) return;
      this.wiz.testKind = t.ok ? 'ok' : 'warn';
      this.wiz.testMsg = t.msg; this.wiz.busy = false;
      this.loadChat();
      // The button was disabled while the alert went out: give it focus back.
      this.$nextTick(() => this.focusShown([document.getElementById('wiz-test')]));
    },
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
      // A banner key is unique: never show two banners for one notice. A
      // fault under the key of a banner still open says that problem anew
      // (the storm guard found a stronger reason) and takes its place.
      this.banners = this.banners.filter(b => b.bid !== key && !(n.key && !b.ok && b.key === n.key));
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
    // "at 10:42 (lasted 12 min)." for an OK notice (local time; when
    // names the time another way, such as "Fri 10:42").
    fixedText(n, when) {
      const at = new Date(n.at), from = new Date(n.openedAt);
      const end = isNaN(at) ? new Date() : at;
      let s = 'at ' + (when || end.toTimeString().slice(0, 5));
      if (!isNaN(from) && end >= from) s += ' (lasted ' + this.downFor(Math.floor((end - from) / 60000)) + ')';
      return s + '.';
    },
    // m minutes in words: "12 min", "2 h 5 min", and from a day on "1 day",
    // "8 days 2 h", the days as team chat says them (chat.downFor, which
    // also says a whole hour as "2 h").
    downFor(m) {
      if (m < 1) return 'less than a minute';
      if (m < 60) return m + ' min';
      if (m < 1440) return Math.floor(m / 60) + ' h ' + (m % 60) + ' min';
      const d = Math.floor(m / 1440), h = Math.floor((m % 1440) / 60);
      return (d === 1 ? '1 day' : d + ' days') + (h ? ' ' + h + ' h' : '');
    },
    // The card's words for the severities (pnWord): Urgent, Warning, Note.
    bannerLead(b) {
      return (b.ok ? 'Fixed: ' : b.severity === 'critical' ? 'Urgent: ' : b.severity === 'warn' ? 'Warning: ' : 'Note: ') + b.title;
    },
    // Show device next to Dismiss: for a fault whose advice points at a
    // device this page lists.
    bannerDevice(b) {
      return !b.ok && !!b.ip && !!b.advice && (b.advice.actions || []).some(a => a.kind === 'showDevice') &&
        this.hosts.some(h => h.ip === b.ip);
    },
    // Open Health next to Dismiss: for a health check that keeps failing,
    // whose What to try is on its card in the Health view.
    bannerHealth(b) { return !b.ok && b.trigger === 'healthFailed'; },
    // The storm guard's banner for another network offers Restart watching
    // while this tab still watches the old one.
    bannerRestart(b) {
      return !b.ok && b.trigger === 'netdiagLink' && b.code === 'network_changed' && this.state === 'live' && !this.gone();
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
    // A device's conflicts: one whose id it lists or that names its address,
    // unless it involves a MAC the person ignored (a saved scan keeps the
    // conflicts it found). Mirrors problems.HostIssues.
    conflictsFor(h) {
      const ids = h.conflicts || [];
      const mac = m => String(m).trim().toUpperCase().replace(/-/g, ':');
      const ign = ((this.settings && this.settings.ignoredConflictMACs) || []).map(mac);
      return this.conflicts.filter(c => (ids.includes(c.id) || (c.ips || []).includes(h.ip)) && !(c.macs || []).some(m => ign.includes(mac(m))));
    },
    hasConflict(h) { return this.conflictsFor(h).length > 0; },
    // A device whose only conflicts are one device on several addresses
    // (often an extender) gets the NOTE badge with the info shape, as its
    // drawer box says Note (conflictLevel); any other gets CONFLICT.
    conflictNote(h) { const cs = this.conflictsFor(h); return cs.length > 0 && cs.every(c => c.kind === 'mac_multi_ip'); },
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
    // marked "fail" was seen as a swap (mirrors advice.SwapSeen). One
    // device with several addresses is often normal (an extender): a
    // "Note". Every other kind is a "Possible conflict".
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
    // seen, possible or note: the conflict box's shape, word and colour.
    conflictLevel(c) { return c.kind === 'mac_multi_ip' ? 'note' : this.conflictSeen(c) ? 'seen' : 'possible'; },
    conflictWord(c) { return {seen: 'Conflict', possible: 'Possible conflict', note: 'Note'}[this.conflictLevel(c)]; },
    conflictIcon(c) { return {seen: 'circle-x', possible: 'triangle-alert', note: 'info'}[this.conflictLevel(c)]; },
    conflictIconClass(c) { return {seen: 's-bad', possible: 's-warn', note: 's-info'}[this.conflictLevel(c)]; },
    // The heading's plain words; the addresses and MACs are under Technical details.
    conflictKind(c) {
      if (c.kind === 'mac_flap') return this.conflictSeen(c) ? 'Two devices are using the same address' : 'Two devices may be using the same address';
      return {mac_multi_ip: 'One device uses several addresses', duplicate_name: 'Two devices use the same name'}[c.kind] || c.kind;
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
        if (r.status === 401) return;
        if (!r.ok) { this.error = 'Could not ignore MAC (HTTP ' + r.status + ')'; return; }
        this.announce = 'Ignoring conflicts for ' + mac + '. To undo, remove it in Settings.';
        this.loadSettings();
        this.conflicts = this.conflicts.filter(c => !(c.macs || []).includes(mac));
        this.loadProblems();
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
      if (r.status === 401) return;
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
      // netdiag has stopped (Scan is off): a click there could only fail.
      if (this.gone()) return;
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
        // An out-of-date token: the session banner already says what to do.
        if (r.status === 401) return;
        if (!r.ok) { this.error = j.error || ('Scan failed (HTTP ' + r.status + ')'); return; }
        this.resetScan();
        this.scanId = j.id; this.running = true; this.state = 'running'; this.watchedCidr = cidr;
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
      this.scanId = sc.id; this.running = true; this.state = sc.state || 'running'; this.watchedCidr = q.cidr || '';
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
      // The range this tab's scan watches when netdiag does not say (the
      // address fields may hold another range by now).
      this.watching = w || {id: this.scanId, cidr: this.watchedCidr}; this.restartStale = !!stale;
      restartOpener = opener;
      this.$nextTick(() => {
        const d = this.$refs.restartDlg;
        if (d && !d.open) d.showModal();
        const k = document.getElementById('restart-keep');
        if (k) k.focus();
      });
    },
    // Restart watching in the top bar. While this computer is on another
    // network it reads the interfaces again first, as the banner's and the
    // card's do, so it never restarts on the range this computer left.
    restartTop() { return this.movedAway() ? this.restartHere() : this.askRestart(); },
    movedAway() { const st = this.liveStatus || {}; return !!st.held && st.heldReason === 'network_changed'; },
    // Restart watching from the banner or the card for another network: the
    // interfaces are read again first, so the restart watches the network
    // this computer is on now (its new address range), not the one it left.
    async restartHere() {
      const opener = document.activeElement;
      try {
        const r = await fetch('/api/interfaces');
        if (r.ok) {
          this.ifaces = (await r.json()) || [];
          const i = this.ifaces.find(x => x.name === this.ifaceName) || this.ifaces.find(x => x.isDefault) || this.ifaces[0];
          if (i) { this.ifaceName = i.name; this.cidr = i.suggested || i.cidr; }
        }
      } catch (e) {}
      if (opener && opener.isConnected && document.activeElement !== opener) opener.focus();
      this.askRestart();
    },
    // "(since 10 Oct 09:12)", with the day when it is not today (pnClock).
    restartSince() {
      const t = this.pnClock((this.watching || {}).since);
      return t ? ' (since ' + t + ')' : '';
    },
    // The range netdiag is watching, so "this network" is never unclear
    // when the person typed another one.
    watchedRange() { return (this.watching || {}).cidr || this.watchedCidr || 'this network'; },
    // What the button does: with Live mode unticked (a stale tab) Stop
    // watching and scan ends the watching and runs one scan, and says so.
    restartSettings() {
      const c = this.cidr.trim();
      return this.live ? 'These settings: ' + c + ', live mode.' : 'This stops watching and runs one scan of ' + c + ', without live mode.';
    },
    closeRestart() { const d = this.$refs.restartDlg; if (d && d.open) d.close(); },
    // Focuses the first of els that is shown, trying for a few frames while
    // x-show catches up. els may be a function that returns them, for
    // elements that x-if has not made yet.
    focusShown(els, tries) {
      const el = (typeof els === 'function' ? els() : els).find(e => e && e.isConnected && e.offsetParent !== null && !e.disabled);
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
      on('health', r => { this.logHealth(this.health, r); this.health = r; this.problemsSoon(); });
      on('conflict', ev => { this.onConflict(ev); this.problemsSoon(); });
      on('notice', n => { this.onNotice(n, id); this.problemsSoon(); });
      on('progress', p => { this.stage = p.stage; this.done = p.done; this.total = p.total; });
      on('stage', s => { this.stage = s.stage; this.done = 0; this.total = 0; this.logPush('scan', 'Stage: ' + s.stage); });
      // The first pass ended (snapshot) or a live cycle ended (live, which
      // also says whether the storm guard holds): the card reads again.
      on('snapshot', r => {
        this.gateway = r.gateway || ''; this.warnings = r.warnings || []; this.state = r.state;
        if (r.health) this.health = r.health;
        if (r.conflicts) this.conflicts = r.conflicts;
        (r.hosts || []).forEach(h => { if (!this.hosts.some(x => x.ip === h.ip)) this.hosts.push(h); });
        this.problemsSoon();
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
        this.problemsSoon();
      });
      // Presence events are live-only (never replayed), so a reconnect does
      // not re-announce old transitions.
      on('presence', p => {
        this.problemsSoon();
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
        this.loadProblems();
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
      // Nothing is watched any more: no live pill, Stop live or Restart
      // watching, and Scan and Quit are off (gone).
      this.running = false; this.quitted = true;
      this.state = ''; this.liveStatus = null; this.stage = '';
      clearTimeout(hbTimer);
      this.leaveBeeper();
      // Quit is off now, so keyboard focus moves to the message that says
      // netdiag has stopped instead of being lost.
      this.$nextTick(() => this.focusShown([document.getElementById('quitted')]));
    },

    stageLabel() {
      // Nothing is watched once netdiag stopped, this page quit it, or it
      // was restarted and this page is out of date (gone), whatever the
      // scan was doing.
      if (this.gone()) return this.backend === 'restarted' && !this.quitted ? 'Not watching: this page is out of date' : 'Not watching: netdiag has stopped';
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
    // Why a device is on the Problems tab, broader than the Problems right
    // now card and whatever the alert types: it is offline (phones too), in
    // a conflict, a printer with alerts or a low supply, or a web page that
    // failed. Mirrors problems.HostIssues; testdata/problems/hosts.json holds
    // the cases both are tested against.
    hostIssues(h) {
      const out = [];
      if (this.isOffline(h)) out.push({kind: 'offline'});
      const c = this.conflictsFor(h);
      if (c.length) out.push({kind: 'conflict', detail: c[0].kind});
      const p = h.printer;
      if (h.isPrinter && p) {
        for (const a of (p.alerts || [])) out.push({kind: 'printerAlert', detail: a.description});
        const thr = this.supplyThreshold();
        for (const s of (p.supplies || [])) {
          if (s.state === 'empty' || s.state === 'low' || (s.percent >= 0 && s.percent <= thr)) out.push({kind: 'supplyLow', detail: s.description});
        }
      }
      for (const w of (h.web || [])) if (w.error || !w.status || w.status >= 400) out.push({kind: 'web', detail: w.url});
      return out;
    },
    isProblem(h) { return this.hostIssues(h).length > 0; },
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
        const since = this.pnClock(st.heldSince) ? ' (since ' + this.pnClock(st.heldSince) + ')' : '';
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
      this.openDrawer(ip, ev.currentTarget.querySelector("button.rowbtn"));
    },
    // from: the element that opened the drawer (default: the focused one),
    // which gets focus back when it closes, while it is still shown.
    openDrawer(ip, from) {
      const o = from || document.activeElement;
      drawerOpener = o && o !== document.body && !o.closest("#drawer") ? o : null;
      this.drawerIP = ip;
      this.$nextTick(() => this.focusShown(() => [document.querySelector("#drawer .drawerhead .iconbtn")]));
    },
    closeDrawer(noFocus) {
      const ip = this.drawerIP, back = drawerOpener;
      if (!ip) return;
      this.drawerIP = "";
      drawerOpener = null;
      if (noFocus) return;
      this.$nextTick(() => {
        if (back && back.isConnected && back.offsetParent !== null) { back.focus(); return; }
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
