(() => { "use strict";
const button = document.querySelector("#delete-all");
const completed = async () => { let response = await fetch("/api/runs"), batches = await response.json(); return batches.filter(batch => !["queued", "running"].includes(batch.status)).map(batch => batch.id) };
const refresh = async () => { try { button.disabled = !(await completed()).length } catch (_) { button.disabled = true } };
button.onclick = async () => { let ids; try { ids = await completed() } catch (_) { alert("Unable to load completed runs"); return } if (!ids.length || !confirm(`Delete all ${ids.length} completed run${ids.length === 1 ? "" : "s"} and their saved artifacts? This cannot be undone.`)) return; try { let response = await fetch("/api/runs", { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids }) }); if (!response.ok) throw Error((await response.json()).error || "Delete failed"); location.reload() } catch (error) { alert(error.message) } };
refresh();
})();
