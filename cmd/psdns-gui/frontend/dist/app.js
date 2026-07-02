// psdns control panel — frontend logic. Talks to the Wails-bound Go App, which
// is exposed at window.go.gui.App (or window.go.main.App). When neither exists
// (e.g. opened directly in a browser for design preview) a mock backend keeps
// the UI fully interactive.

(function () {
  "use strict";

  // ---- backend binding (tolerant of namespace) + browser-preview mock --------
  function realApp() {
    const go = window.go || {};
    return (go.gui && go.gui.App) || (go.main && go.main.App) || null;
  }

  const mock = {
    Version: async () => "dev (preview)",
    GetConfig: async () => ({
      dohUrl: "https://1.1.1.1/dns-query", dohBootstrap: "",
      dohFallbacks: "", dohHedgeDelay: "250ms",
      dnsListen: "127.0.0.1:53", proxyListen: "127.0.0.1:8080",
      socksListen: "127.0.0.1:1080", frag: "split", fragDelay: "0s", timeout: "10s",
      setSystemProxy: true,
    }),
    GetStatus: async () => ({ running: false, mode: "proxy", listeners: [] }),
    Start: async (m) => ({
      running: true, mode: m,
      listeners: [
        { kind: "http", addr: "127.0.0.1:8080", up: true },
        { kind: "socks", addr: "127.0.0.1:1080", up: true },
      ],
    }),
    Stop: async () => ({ running: false, mode: "", listeners: [] }),
    SetConfig: async (c) => ({ config: c, warnings: [] }),
    CheckUpdate: async () => ({ current: "dev", latest: "", newer: false }),
    ApplyUpdate: async () => { throw "업데이트 자동 설치는 곧 제공될 예정이에요."; },
    SystemProxySupported: async () => true,
    Quit: async () => {},
  };

  const app = realApp() || mock;
  const rt = window.runtime || null;

  // ---- state -----------------------------------------------------------------
  let ui = null;            // current UIConfig (editable while stopped)
  let mode = "proxy";       // selected run mode
  let running = false;
  let lastState = null;     // last supervisor State
  let lastFrag = "split";   // remembered SNI strategy
  let latestUpdate = null;  // last CheckResult with newer=true

  // ---- helpers ---------------------------------------------------------------
  const $ = (id) => document.getElementById(id);
  const hostOf = (addr) => {
    if (!addr) return addr;
    const i = addr.lastIndexOf(":");
    return i > 0 ? addr.slice(0, i) : addr;
  };

  function toast(msg) {
    const t = $("toast");
    t.textContent = msg;
    t.hidden = false;
    clearTimeout(toast._t);
    toast._t = setTimeout(() => { t.hidden = true; }, 1500);
  }

  function showError(msg) {
    const el = $("listenerErr");
    el.textContent = msg;
    el.hidden = !msg;
  }
  function clearError() { showError(""); }

  function showWarnings(warns) {
    const box = $("warnBox");
    if (warns && warns.length) {
      box.textContent = warns.join("\n");
      box.hidden = false;
    } else {
      box.hidden = true;
    }
  }

  async function copyText(text) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
        return true;
      }
    } catch (e) { /* fall through */ }
    try {
      const ta = document.createElement("textarea");
      ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.select();
      const ok = document.execCommand("copy");
      document.body.removeChild(ta);
      return ok;
    } catch (e) { return false; }
  }

  function openURL(url) {
    if (!url) return;
    if (rt && rt.BrowserOpenURL) rt.BrowserOpenURL(url);
    else window.open(url, "_blank");
  }

  // ---- rendering -------------------------------------------------------------
  function render() {
    renderMode();
    renderSNI();
    renderSysProxy();
    renderConn();
    renderAdvanced();
    renderStatus();
    setControlsEnabled(!running);
  }

  function renderStatus() {
    const badge = $("statusBadge"), badgeText = $("statusBadgeText");
    const title = $("heroTitle"), sub = $("heroSub");
    const btn = $("powerBtn"), btnText = $("powerBtnText"), icon = btn.querySelector(".btn__icon");

    const failed = (lastState && lastState.listeners || []).filter((l) => l.err);

    if (!running) {
      badge.className = "badge badge--off"; badgeText.textContent = "꺼짐";
      title.textContent = "지금은 꺼져 있어요";
      sub.textContent = "켜면 차단된 사이트에 다시 들어갈 수 있어요";
      btn.classList.remove("is-on"); btnText.textContent = "보호 시작하기"; icon.textContent = "▶";
      clearError();
      return;
    }

    btn.classList.add("is-on"); btnText.textContent = "중지하기"; icon.textContent = "■";

    if (failed.length) {
      badge.className = "badge badge--err"; badgeText.textContent = "일부 실패";
      title.textContent = "일부 기능을 못 켰어요";
      sub.textContent = "아래 안내를 확인해 주세요";
      showError(failed.map((l) => l.err).join("\n"));
    } else {
      badge.className = "badge badge--on"; badgeText.textContent = "정상 작동 중";
      title.textContent = "보호하고 있어요";
      sub.textContent = mode === "resolve"
        ? "시스템 DNS 차단을 우회하고 있어요"
        : "이제 막혀 있던 사이트도 열어 볼 수 있어요";
      clearError();
    }
  }

  function renderMode() {
    document.querySelectorAll("#modeSeg .segment__btn").forEach((b) => {
      const on = b.dataset.mode === mode;
      b.classList.toggle("is-active", on);
      b.setAttribute("aria-checked", on ? "true" : "false");
    });
    const hints = {
      proxy: "이 방법을 추천해요. 따로 권한이 필요 없어요",
      resolve: "컴퓨터 전체에 적용돼요. 켤 때 관리자 권한이 필요할 수 있어요",
      run: "프록시와 시스템 DNS를 함께 켜요",
    };
    $("modeHint").textContent = hints[mode] || "";
    $("sniNote").hidden = mode !== "resolve";
  }

  function renderSNI() {
    const on = ui && ui.frag && ui.frag !== "none";
    const sw = $("sniToggle");
    sw.setAttribute("aria-checked", on ? "true" : "false");
    $("sniMethods").hidden = !on;
    document.querySelectorAll("#sniMethods .chip").forEach((c) => {
      c.classList.toggle("is-active", on && c.dataset.frag === ui.frag);
    });
  }

  function renderSysProxy() {
    // Default on: an unset value (older config) reads as enabled.
    const on = !ui || ui.setSystemProxy !== false;
    $("sysProxyToggle").setAttribute("aria-checked", on ? "true" : "false");
  }

  function renderConn() {
    if (!ui) return;
    const card = $("connCard");
    const proxyRow = $("httpAddr").closest(".addr");
    const socksRow = $("socksAddr").closest(".addr");
    const dnsHint = $("dnsHint");
    const howto = card.querySelector(".howto");

    // Prefer live listener addresses when running.
    const byKind = {};
    (lastState && lastState.listeners || []).forEach((l) => { byKind[l.kind] = l.addr; });

    $("httpAddr").textContent = byKind.http || ui.proxyListen;
    $("socksAddr").textContent = byKind.socks || ui.socksListen;
    $("dnsHintText").textContent = "컴퓨터의 DNS 서버를 " + hostOf(ui.dnsListen) + " 로 바꿔 주세요";

    const proxyish = mode === "proxy" || mode === "run";
    proxyRow.hidden = !proxyish;
    socksRow.hidden = !proxyish;
    howto.hidden = !proxyish;
    dnsHint.hidden = !(mode === "resolve" || mode === "run");

    card.classList.toggle("is-dim", !running);
  }

  function renderAdvanced() {
    if (!ui) return;
    $("fDoh").value = ui.dohUrl || "";
    $("fBootstrap").value = ui.dohBootstrap || "";
    $("fDohFallbacks").value = ui.dohFallbacks || "";
    $("fDohHedgeDelay").value = ui.dohHedgeDelay || "";
    $("fFragDelay").value = ui.fragDelay || "";
    $("fTimeout").value = ui.timeout || "";
    $("fHttp").value = ui.proxyListen || "";
    $("fSocks").value = ui.socksListen || "";
    $("fDns").value = ui.dnsListen || "";
  }

  function setControlsEnabled(on) {
    // These are <button>s, so the disabled attribute alone blocks interaction
    // and focus; the muted look is supplied by CSS ([disabled] rules).
    document.querySelectorAll("#modeSeg .segment__btn, #sniToggle, #sysProxyToggle, #sniMethods .chip")
      .forEach((el) => { el.toggleAttribute("disabled", !on); });
    document.querySelectorAll("#advanced input")
      .forEach((el) => { el.disabled = !on; });
  }

  // ---- config persistence ----------------------------------------------------
  function readAdvancedInto(c) {
    c.dohUrl = $("fDoh").value.trim();
    c.dohBootstrap = $("fBootstrap").value.trim();
    c.dohFallbacks = $("fDohFallbacks").value.trim();
    c.dohHedgeDelay = $("fDohHedgeDelay").value.trim();
    c.fragDelay = $("fFragDelay").value.trim();
    c.timeout = $("fTimeout").value.trim();
    c.proxyListen = $("fHttp").value.trim();
    c.socksListen = $("fSocks").value.trim();
    c.dnsListen = $("fDns").value.trim();
  }

  async function persist() {
    try {
      const res = await app.SetConfig(ui);
      ui = res.config || ui;
      showWarnings(res.warnings);
      clearError();
      return true;
    } catch (e) {
      showError(String(e.message || e));
      return false;
    }
  }

  // ---- actions ---------------------------------------------------------------
  async function togglePower() {
    const btn = $("powerBtn");
    btn.disabled = true;
    try {
      if (!running) {
        if (!(await persist())) { return; }
        $("heroTitle").textContent = "켜는 중이에요…";
        lastState = await app.Start(mode);
      } else {
        lastState = await app.Stop();
      }
      running = !!(lastState && lastState.running);
      if (lastState && lastState.mode) mode = lastState.mode;
      render();
      notifyFallback();
    } catch (e) {
      showError(String(e.message || e));
    } finally {
      btn.disabled = false;
    }
  }

  // notifyFallback explains why a proxy bound to an alternate port: the configured
  // one was busy or OS-reserved (common on Windows with Hyper-V/WSL/Docker). The
  // connection card already shows the real address; this is the one-time "why".
  function notifyFallback() {
    const fb = (lastState && lastState.listeners || []).find((l) => l.fallback && l.up);
    if (fb) toast("기본 포트가 사용 중이라 " + fb.addr + " 로 열었어요");
  }

  function selectMode(m) {
    if (running) return;
    mode = m;
    renderMode();
    renderConn();
  }

  async function toggleSNI() {
    if (running || !ui) return;
    if (ui.frag === "none") ui.frag = lastFrag || "split";
    else { lastFrag = ui.frag; ui.frag = "none"; }
    renderSNI();
    await persist();
  }

  async function toggleSysProxy() {
    if (running || !ui) return;
    ui.setSystemProxy = ui.setSystemProxy === false; // off -> on, otherwise -> off
    renderSysProxy();
    await persist();
  }

  async function selectFrag(frag) {
    if (running || !ui) return;
    lastFrag = frag; ui.frag = frag;
    renderSNI();
    await persist();
  }

  async function onAdvancedChange() {
    if (running || !ui) return;
    readAdvancedInto(ui);
    await persist();
    renderConn();
  }

  // ---- update ----------------------------------------------------------------
  function showUpdate(res) {
    if (!res || !res.newer) return;
    latestUpdate = res;
    $("updateDot").hidden = false;
    $("updateBanner").hidden = false;
    $("updateBannerSub").textContent =
      (res.current || "") + " → " + (res.latest || "") + " · 더 잘 뚫려요";
  }

  async function checkUpdate(silent) {
    try {
      const res = await app.CheckUpdate();
      showUpdate(res);
      if (!silent && (!res || !res.newer)) toast("최신 버전을 쓰고 있어요");
    } catch (e) {
      if (!silent) toast("업데이트 확인에 실패했어요");
    }
  }

  // stageLabel mirrors the CLI's stageLabel (cmd/psdns/main.go) so the modal
  // shows what the updater is doing as update:progress events arrive.
  function stageLabel(stage) {
    switch (stage) {
      case "download": return "내려받는 중";
      case "verify": return "검증 중";
      case "extract": return "압축 해제";
      case "replace": return "교체 중";
      case "done": return "완료";
      default: return "준비 중";
    }
  }

  function openUpdateModal() {
    $("updateModalBody").textContent = "새 버전을 받고 있어요…";
    $("updateProgress").style.width = "10%";
    $("updateReleaseLink").hidden = true;
    $("updateClose").hidden = true;
    $("updateModal").hidden = false;
  }

  async function applyUpdate() {
    openUpdateModal();
    try {
      await app.ApplyUpdate();
      $("updateModalBody").textContent = "업데이트를 마쳤어요. 곧 다시 시작돼요.";
      $("updateProgress").style.width = "100%";
    } catch (e) {
      $("updateModalBody").textContent = String(e.message || e);
      $("updateProgress").style.width = "0%";
      const link = $("updateReleaseLink");
      if (latestUpdate && latestUpdate.releaseUrl) {
        link.hidden = false;
        link.onclick = (ev) => { ev.preventDefault(); openURL(latestUpdate.releaseUrl); };
      }
      $("updateClose").hidden = false;
    }
  }

  // ---- modal helpers ---------------------------------------------------------
  function openModal(id) { $(id).hidden = false; }
  function closeModal(id) { $(id).hidden = true; }

  // ---- wiring ----------------------------------------------------------------
  function wire() {
    $("powerBtn").addEventListener("click", togglePower);

    document.querySelectorAll("#modeSeg .segment__btn").forEach((b) => {
      b.addEventListener("click", () => selectMode(b.dataset.mode));
    });

    $("sniToggle").addEventListener("click", toggleSNI);
    $("sysProxyToggle").addEventListener("click", toggleSysProxy);
    document.querySelectorAll("#sniMethods .chip").forEach((c) => {
      c.addEventListener("click", () => selectFrag(c.dataset.frag));
    });

    document.querySelectorAll("#advanced input").forEach((el) => {
      el.addEventListener("change", onAdvancedChange);
    });

    document.querySelectorAll(".btn--copy").forEach((b) => {
      b.addEventListener("click", async () => {
        const text = $(b.dataset.copy).textContent;
        const ok = await copyText(text);
        toast(ok ? "복사했어요" : "복사하지 못했어요");
      });
    });

    $("quitBtn").addEventListener("click", () => openModal("quitModal"));
    $("quitConfirm").addEventListener("click", () => app.Quit());
    document.querySelectorAll('[data-close="quit"]').forEach((el) => {
      el.addEventListener("click", () => closeModal("quitModal"));
    });

    $("updateBtn").addEventListener("click", applyUpdate);
    $("updateDot").addEventListener("click", () => $("updateBanner").scrollIntoView({ behavior: "smooth" }));
    $("updateClose").addEventListener("click", () => closeModal("updateModal"));

    if (rt && rt.EventsOn) {
      rt.EventsOn("update:available", showUpdate);
      // Drive the progress bar from the updater's stages so it doesn't sit at
      // the initial 10% for the whole download.
      rt.EventsOn("update:progress", (p) => {
        if (!p) return;
        $("updateModalBody").textContent = stageLabel(p.stage);
        $("updateProgress").style.width = Math.round((p.pct || 0) * 100) + "%";
      });
      // The app emits update:error if the post-update relaunch fails, so the
      // modal doesn't sit on "곧 다시 시작돼요" forever.
      rt.EventsOn("update:error", (msg) => {
        $("updateModalBody").textContent =
          String(msg || "업데이트 후 자동 재시작에 실패했어요. 앱을 직접 다시 실행해 주세요.");
        $("updateProgress").style.width = "0%";
        $("updateClose").hidden = false;
      });
      // System-proxy auto-config toasts, emitted by App.Start/Stop/Shutdown.
      rt.EventsOn("sysproxy:applied", (m) => toast(m || "시스템 프록시를 자동으로 맞췄어요"));
      rt.EventsOn("sysproxy:restored", (m) => toast(m || "시스템 프록시를 원래대로 되돌렸어요"));
      rt.EventsOn("sysproxy:error", (m) => toast(m || "시스템 프록시 설정에 실패했어요"));
    }
  }

  // ---- init ------------------------------------------------------------------
  async function init() {
    wire();
    try {
      const [ver, cfg, st] = await Promise.all([app.Version(), app.GetConfig(), app.GetStatus()]);
      $("versionText").textContent = "버전 " + ver;
      ui = cfg;
      if (ui.frag && ui.frag !== "none") lastFrag = ui.frag;
      running = !!st.running;
      mode = st.mode || "proxy";
      lastState = st;
      // Hide the system-proxy auto-config card on platforms where the OS
      // automation can never take effect, so the user isn't offered a toggle
      // whose only outcome is an error toast.
      if (app.SystemProxySupported) {
        try {
          const supported = await app.SystemProxySupported();
          const card = $("sysProxyCard");
          if (card) card.hidden = !supported;
        } catch (e) { /* older backend: leave the card visible */ }
      }
    } catch (e) {
      showError("초기화에 실패했어요: " + String(e.message || e));
      ui = await mock.GetConfig();
    }
    render();
    checkUpdate(true); // silent background check (event also covers this)
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
