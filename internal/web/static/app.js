(() => {
  "use strict";
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const state = { catalog: { harnesses: [], cases: [] }, attempts: new Map(), filter: "all", stream: null, timer: null };
  const active = status => ["queued", "running"].includes(status);
  const number = value => typeof value === "number" ? value.toLocaleString() : "—";
  const elapsed = milliseconds => {
    if (milliseconds == null || milliseconds < 0) return "—";
    const seconds = Math.floor(milliseconds / 1000), minutes = Math.floor(seconds / 60);
    return minutes ? `${minutes}m ${String(seconds % 60).padStart(2, "0")}s` : `${seconds}s`;
  };
  const currentElapsed = attempt => active(attempt.status) && attempt.started_at
    ? Date.now() - new Date(attempt.started_at).getTime() : (attempt.elapsed_ms ?? attempt.elapsed_millis);
  const totalTokens = usage => {
    if (!usage || usage.token_quality === "unavailable") return null;
    // Cached input is included in input and reasoning is included in output; never double-count either.
    const values = [usage.input_tokens, usage.output_tokens];
    return values.some(value => typeof value !== "number") ? null : values.reduce((sum, value) => sum + value, 0);
  };
  const tokenQuality = usage => ({ provider_reported: "Provider-reported", client_estimated: "Client-estimated", unavailable: "Unavailable" })[usage?.token_quality] || "Awaiting provider report";
  const tokenMetric = (usage, field, status) => {
    if (typeof usage?.[field] === "number") return number(usage[field]);
    return active(status) ? "Awaiting provider report" : "—";
  };
  const usageFor = attempt => attempt.result?.usage || attempt.usage;
  const sumUsage = (attempts, field) => {
    const values = attempts.map(usageFor).map(usage => usage?.[field]).filter(value => typeof value === "number");
    return values.length ? number(values.reduce((sum, value) => sum + value, 0)) : "—";
  };
  const statusLabel = status => ({ queued: "Queued", running: "Running", completed: "Completed", failed: "Failed", timed_out: "Timed out", unsupported: "Unsupported", infrastructure_invalid: "Infrastructure invalid", interrupted: "Interrupted" })[status] || "Pending";

  async function request(path, options) {
    const response = await fetch(path, { headers: options?.body instanceof FormData ? {} : { "Content-Type": "application/json" }, ...options });
    if (!response.ok) { let message = `Request failed (${response.status})`; try { message = (await response.json()).error || message; } catch (_) {} throw new Error(message); }
    return response.status === 204 ? null : response.json();
  }
  function setConnection(kind, label) { const node = $("#connection"); node.className = `connection ${kind}`; $("span", node).textContent = label; }
  function renderCatalog() {
    const selectedHarnesses = new Set($$("#harness-list input:checked").map(input => input.value));
    const selectedCases = new Set($$("#case-list input:checked").map(input => input.value));
    $("#harness-list").replaceChildren(...state.catalog.harnesses.map(item => choice(item, selectedHarnesses.has(item.id))));
    const caseItems = state.catalog.cases.map(item => choice(item, selectedCases.has(item.id)));
    $("#case-list").replaceChildren(...(caseItems.length ? caseItems : [empty("No test cases found. Import one to get started.")]));
    updateLaunch();
  }
  function choice(item, checked) {
    const label = document.createElement("label"); label.className = `choice${item.available === false ? " disabled" : ""}`;
    label.innerHTML = `<input type="checkbox" value="${escapeHTML(item.id)}" ${checked ? "checked" : ""} ${item.available === false ? "disabled" : ""}><span><b>${escapeHTML(item.name || item.id)}</b><small>${escapeHTML(item.description || item.prompt_preview || (item.source_type ? `${item.source_type} source` : item.id))}</small></span>`;
    $("input", label).addEventListener("change", updateLaunch); return label;
  }
  function updateLaunch() {
    const h = $$("#harness-list input:checked").length, c = $$("#case-list input:checked").length, count = h * c;
    $("#attempt-count").textContent = `${count} attempt${count === 1 ? "" : "s"}`;
    $("#start-run").disabled = !count;
    $("#launch-summary").textContent = count ? `${h} harness${h === 1 ? "" : "es"} × ${c} case${c === 1 ? "" : "s"}. Each attempt gets a fresh container.` : "Select a harness and a test case to continue.";
  }
  function escapeHTML(value) { const div = document.createElement("div"); div.textContent = value || ""; return div.innerHTML; }
  function empty(message) { const node = document.createElement("p"); node.className = "empty"; node.textContent = message; return node; }

  function renderAttempts() {
    const attempts = [...state.attempts.values()].sort((a, b) => new Date(b.started_at || b.created_at || 0) - new Date(a.started_at || a.created_at || 0));
    const visible = attempts.filter(attempt => state.filter === "all" || (state.filter === "active" ? active(attempt.status) : !active(attempt.status)));
    const parent = $("#attempts");
    if (!visible.length) { parent.innerHTML = `<div class="empty-state"><span aria-hidden="true">⌁</span><h3>${attempts.length ? "No matching attempts" : "No attempts yet"}</h3><p>${attempts.length ? "Try another filter." : "Choose harnesses and cases above to begin."}</p></div>`; return; }
    parent.replaceChildren(...visible.map(attemptCard)); renderMetrics(attempts);
  }
  function attemptCard(attempt) {
    const node = $("#attempt-template").content.firstElementChild.cloneNode(true);
    const status = attempt.status || "queued", usage = attempt.result?.usage || attempt.usage, tokenCount = totalTokens(usage), result = attempt.result || {};
    $(".status", node).classList.add(status); $(".status", node).setAttribute("title", statusLabel(status));
    $(".attempt-title", node).textContent = `${attempt.harness_name || attempt.harness_id || "Harness"} · ${attempt.case_name || attempt.case_id || "Test case"}`;
    $(".attempt-subtitle", node).textContent = `${statusLabel(status)}${attempt.id ? ` · ${attempt.id}` : ""}`;
    $("[data-stat=elapsed]", node).textContent = elapsed(currentElapsed(attempt));
    $("[data-stat=input-tokens]", node).textContent = tokenMetric(usage, "input_tokens", status);
    $("[data-stat=output-tokens]", node).textContent = tokenMetric(usage, "output_tokens", status);
    $("[data-stat=cached-input-tokens]", node).textContent = tokenMetric(usage, "cached_input_tokens", status);
    $("[data-stat=reasoning-output-tokens]", node).textContent = tokenMetric(usage, "reasoning_output_tokens", status);
    const verifier = attempt.verifier_passed ?? attempt.result?.verifier_passed;
    $("[data-stat=result]", node).textContent = verifier === true ? "Passed" : verifier === false ? "Failed" : statusLabel(status);
    $("[data-stat=token-quality]", node).textContent = tokenCount == null && active(status) ? "Awaiting provider report" : tokenQuality(usage);
    $("[data-stat=model]", node).textContent = result.model || attempt.model || "—";
    $("[data-stat=reasoning-effort]", node).textContent = result.reasoning_effort || attempt.reasoning_effort || "—";
    $("[data-stat=exit-code]", node).textContent = typeof result.exit_code === "number" ? result.exit_code : "—";
    const details = $(".attempt-details", node), toggle = $(".details", node), log = $(".log", node);
    log.textContent = Array.isArray(attempt.logs) && attempt.logs.length ? attempt.logs.join("\n") : "Raw provider output is not retained. Status, elapsed time, and provider usage appear above.";
    if (attempt.failure_reason || attempt.error || attempt.result?.failure_reason) { const failure = $(".failure", node); failure.hidden = false; failure.textContent = attempt.failure_reason || attempt.error || attempt.result?.failure_reason; }
    if (attempt.result_url) { const link = $(".result-link", node); link.hidden = false; link.href = attempt.result_url; }
    toggle.addEventListener("click", () => { details.hidden = !details.hidden; toggle.setAttribute("aria-expanded", String(!details.hidden)); toggle.innerHTML = `${details.hidden ? "Details" : "Hide details"} <span aria-hidden="true">⌄</span>`; });
    return node;
  }
  function renderMetrics(attempts) {
    const running = attempts.filter(a => active(a.status)), complete = attempts.filter(a => !active(a.status));
    const knownTokens = attempts.map(a => totalTokens(a.result?.usage || a.usage)).filter(v => v != null);
    const passed = complete.filter(a => (a.verifier_passed ?? a.result?.verifier_passed) === true);
    const failed = complete.filter(a => (a.verifier_passed ?? a.result?.verifier_passed) === false || ["failed", "timed_out", "infrastructure_invalid", "unsupported", "interrupted"].includes(a.status));
    $("#metric-running").textContent = running.length; $("#metric-completed").textContent = complete.length;
    $("#metric-passed").textContent = passed.length; $("#metric-failed").textContent = failed.length;
    $("#metric-input-tokens").textContent = sumUsage(attempts, "input_tokens");
    $("#metric-output-tokens").textContent = sumUsage(attempts, "output_tokens");
    $("#metric-tokens").textContent = knownTokens.length ? number(knownTokens.reduce((a,b) => a + b, 0)) : "—";
    const elapsedMs = attempts.map(currentElapsed).filter(v => typeof v === "number"); $("#metric-elapsed").textContent = elapsedMs.length ? elapsed(Math.max(...elapsedMs)) : "—";
    $("#filter-all").textContent = attempts.length; $("#filter-active").textContent = running.length; $("#filter-finished").textContent = complete.length;
  }
  function mergeAttempt(update) {
    if (!update || !update.id) return;
    update = { ...update, harness_id: update.harness_id || update.variant_id, elapsed_ms: update.elapsed_ms ?? update.elapsed_millis };
    const old = state.attempts.get(update.id) || {};
    state.attempts.set(update.id, { ...old, ...update, usage: { ...(old.usage || {}), ...(update.usage || {}) }, logs: update.logs || old.logs || [] });
    renderAttempts();
  }
  async function load() {
    try {
      const [catalog, runs] = await Promise.all([request("/api/catalog"), request("/api/runs")]);
      state.catalog = catalog || state.catalog;
      const batches = Array.isArray(runs) ? runs : [runs]; batches.flatMap(batch => batch?.attempts || []).forEach(mergeAttempt);
      renderCatalog(); renderAttempts(); setConnection("online", "Live");
    } catch (error) { setConnection("offline", "Offline"); console.warn(error); $("#harness-list").replaceChildren(empty("The dashboard service is unavailable.")); $("#case-list").replaceChildren(empty("The dashboard service is unavailable.")); }
  }
  function subscribe() {
    if (!("EventSource" in window)) return;
    state.stream = new EventSource("/api/events");
    state.stream.onopen = () => setConnection("online", "Live");
    state.stream.onerror = () => setConnection("offline", "Reconnecting");
    ["attempt", "progress"].forEach(type => state.stream.addEventListener(type, event => { try { mergeAttempt(JSON.parse(event.data).attempt); } catch (_) {} }));
    state.stream.addEventListener("log", event => { try { const update = JSON.parse(event.data), attempt = state.attempts.get(update.attempt_id); if (attempt) { attempt.logs = [...(attempt.logs || []), update.line].slice(-300); renderAttempts(); } } catch (_) {} });
  }
  async function start() {
    const harness_ids = $$("#harness-list input:checked").map(input => input.value), case_ids = $$("#case-list input:checked").map(input => input.value), button = $("#start-run");
    button.disabled = true; button.textContent = "Starting…";
    try { const result = await request("/api/runs", { method: "POST", body: JSON.stringify({ harness_ids, case_ids }) }); (result.attempts || []).forEach(mergeAttempt); state.filter = "all"; $$(".filter").forEach(x => x.classList.toggle("active", x.dataset.filter === "all")); }
    catch (error) { alert(error.message); } finally { button.textContent = "Start benchmark ↗"; updateLaunch(); }
  }
  function setupImport() {
    const dialog = $("#import-dialog"), form = $("#import-form"); $("#show-import").addEventListener("click", () => dialog.showModal());
    $$('[data-close-dialog]', dialog).forEach(button => button.addEventListener("click", () => dialog.close()));
    $$('input[name="source_type"]', form).forEach(input => input.addEventListener("change", () => { ["archive", "files", "repo"].forEach(name => $(`#${name}-field`, form).hidden = input.value !== (name === "repo" ? "repo_url" : name)); }));
    form.addEventListener("submit", async event => {
      event.preventDefault(); const submit = $("button[type=submit]", form), error = $("#import-error", form), data = new FormData(form), source = data.get("source_type"); data.set("id", data.get("name")); error.textContent = "";
      if (source === "files") { const files = [...form.elements.files.files]; if (!files.length) { error.textContent = "Choose at least one file."; return; } data.set("file_paths", JSON.stringify(files.map(file => { const path = file.webkitRelativePath || file.name; const slash = path.indexOf("/"); return slash < 0 ? path : path.slice(slash + 1); }))); }
      if (source === "archive" && !form.elements.archive.files.length) { error.textContent = "Choose a repository archive."; return; }
      if (source === "repo_url") { try { if (new URL(form.elements.repository.value).protocol !== "https:") throw new Error(); } catch (_) { error.textContent = "Enter an HTTPS repository URL."; return; } }
      submit.disabled = true; submit.textContent = "Importing…";
      try { const imported = await request("/api/cases", { method: "POST", body: data }); const item = imported.case || imported; state.catalog.cases.push({ ...item, name: item.name || item.id, prompt_preview: item.prompt_preview || item.prompt, source_type: item.source_type || source }); renderCatalog(); dialog.close(); form.reset(); }
      catch (err) { error.textContent = err.message; } finally { submit.disabled = false; submit.textContent = "Import test case"; }
    });
  }
  $("#start-run").addEventListener("click", start); $("#refresh").addEventListener("click", load);
  $$(".filter").forEach(button => button.addEventListener("click", () => { state.filter = button.dataset.filter; $$(".filter").forEach(item => item.classList.toggle("active", item === button)); renderAttempts(); }));
  setupImport(); load(); subscribe(); state.timer = setInterval(() => { if ([...state.attempts.values()].some(a => active(a.status))) renderAttempts(); }, 1000);
})();
