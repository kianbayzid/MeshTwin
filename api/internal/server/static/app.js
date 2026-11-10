// MeshTwin map: plain JS, no build step. It only calls the read-only API.
// Text from the API (node names come from public adverts) is always set with
// textContent, never as HTML, so a hostile name cannot inject markup.
"use strict";

const ROLE_COLOURS = { repeater: "#c0392b", room: "#7b3294", companion: "#2471a3", sensor: "#1e8449", unknown: "#7f8c8d" };
// Residual bands (observed - predicted SNR, dB): red = worse than the model, blue = better.
const RESIDUAL_BANDS = [
  { max: -10, colour: "#b2182b", label: "< -10 dB (much worse)" },
  { max: -3, colour: "#ef8a62", label: "-10 to -3 dB" },
  { max: 3, colour: "#999999", label: "-3 to 3 dB (as predicted)" },
  { max: 10, colour: "#67a9cf", label: "3 to 10 dB" },
  { max: Infinity, colour: "#2166ac", label: "> 10 dB (much better)" },
];
const GRID = 0.01;

const map = L.map("map").setView([47.6, -122.3], 10);
L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
  maxZoom: 18,
  attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors | ' +
    "Terrain: produced using Copernicus WorldDEM-30 &copy; DLR e.V. 2010-2014 and &copy; Airbus Defence and Space GmbH " +
    "2014-2018 provided under COPERNICUS by the European Union and ESA; all rights reserved.",
}).addTo(map);

const layers = {
  links: L.layerGroup().addTo(map),
  nodes: L.layerGroup().addTo(map),
  spofs: L.layerGroup().addTo(map),
  placement: L.layerGroup().addTo(map),
};
L.control.layers(null, {
  "Links": layers.links,
  "Nodes": layers.nodes,
  "Single points of failure": layers.spofs,
  "Placement candidates": layers.placement,
}, { collapsed: true }).addTo(map);

const el = (id) => document.getElementById(id);
const state = { region: null, topology: null, residuals: new Map(), notReady: new Set() };

// api fetches a path; a 503 (summary not produced yet) resolves to null.
async function api(path) {
  const res = await fetch(path);
  if (res.status === 503) return null;
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error?.message || `HTTP ${res.status}`);
  }
  return res.json();
}

function setStatus(text) { el("status").textContent = text; }

function showNotReady() {
  setStatus(state.notReady.size ? `Not ready yet for ${state.region}: ${[...state.notReady].join(", ")}.` : "");
}

// tooltip builds a text-only tooltip element from [label, value] pairs.
function tooltip(rows) {
  const div = document.createElement("div");
  for (const [k, v] of rows) {
    const line = document.createElement("div");
    line.textContent = `${k}: ${v ?? "-"}`;
    div.appendChild(line);
  }
  return div;
}

const linkKey = (a, b) => (a < b ? `${a}|${b}` : `${b}|${a}`);
const latlng = (loc) => [loc.lat, loc.lon];

function residualColour(r) {
  return RESIDUAL_BANDS.find((band) => r < band.max).colour;
}

function drawLinks() {
  layers.links.clearLayers();
  if (!state.topology) return;
  const byResidual = el("residuals").checked;
  const where = new Map(state.topology.nodes.filter((n) => n.location).map((n) => [n.id, n.location]));
  for (const link of state.topology.links) {
    const a = where.get(link.a), b = where.get(link.b);
    if (!a || !b) continue; // an endpoint without a location cannot be drawn
    const residual = state.residuals.get(linkKey(link.a, link.b));
    let style = { color: "#34495e", weight: 2, opacity: 0.15 + 0.7 * (link.confidence - 0.5) / 0.5 };
    if (byResidual) {
      style = residual === undefined
        ? { color: "#bbbbbb", weight: 1, opacity: 0.4 }
        : { color: residualColour(residual.residual_db), weight: 3, opacity: 0.9 };
    }
    L.polyline([latlng(a), latlng(b)], style).bindTooltip(tooltip([
      ["Link", `${link.a} – ${link.b}`],
      ["Confidence", link.confidence],
      ["Observations", link.observations],
      ["Median SNR (dB)", link.snr_db?.median],
      ["Residual (dB)", residual?.residual_db],
    ]), { sticky: true }).addTo(layers.links);
  }
}

function drawNodes() {
  layers.nodes.clearLayers();
  if (!state.topology) return;
  for (const n of state.topology.nodes) {
    if (!n.location) continue;
    L.circleMarker(latlng(n.location), {
      radius: n.role === "repeater" || n.role === "room" ? 7 : 5,
      color: "#fff", weight: 1, fillColor: ROLE_COLOURS[n.role] || ROLE_COLOURS.unknown, fillOpacity: 0.9,
    }).bindTooltip(tooltip([
      ["ID", n.id], ["Name", n.name], ["Role", n.role], ["Degree", n.degree], ["Last heard", n.last_heard],
    ])).addTo(layers.nodes);
  }
}

function drawSpofs(resilience) {
  layers.spofs.clearLayers();
  for (const s of resilience?.single_points_of_failure || []) {
    if (!s.location) continue;
    L.circleMarker(latlng(s.location), { radius: 13, color: "#000", weight: 3, dashArray: "4 3", fill: false })
      .bindTooltip(tooltip([
        ["Single point of failure", s.name || s.id],
        ["Reachable pairs lost", s.reachable_pairs_lost],
        ["Nodes cut off", s.nodes_cut_off],
        ["Confidence", s.confidence],
      ])).addTo(layers.spofs);
  }
}

// drawPlacement draws each candidate as the whole 0.01° cell it stands for.
function drawPlacement(placement) {
  layers.placement.clearLayers();
  for (const c of placement?.candidates || []) {
    const { lat, lon } = c.location;
    const h = GRID / 2;
    L.rectangle([[lat - h, lon - h], [lat + h, lon + h]], { color: "#1e8449", weight: 2, fillOpacity: 0.25 })
      // Top candidates are often neighbouring cells: only #1 keeps a permanent label, the rest show on hover.
      .bindTooltip(`#${Number(c.rank)}`, { permanent: c.rank === 1, direction: "center", className: "rank-label" })
      .bindPopup(tooltip([
        ["Rank", c.rank], ["Score", c.score], ["Nodes gained", c.coverage_gain_nodes],
        ["Reachable pairs gained", c.reachable_pairs_gained], ["SPOFs removed", (c.spofs_removed || []).join(", ")],
      ])).addTo(layers.placement);
  }
}

function drawLegend() {
  const legend = el("legend");
  legend.hidden = !el("residuals").checked;
  legend.replaceChildren();
  const title = document.createElement("strong");
  title.textContent = "Observed − predicted SNR";
  legend.appendChild(title);
  for (const band of [...RESIDUAL_BANDS, { colour: "#bbbbbb", label: "not measured" }]) {
    const row = document.createElement("div");
    const swatch = document.createElement("span");
    swatch.style.background = band.colour;
    row.append(swatch, band.label);
    legend.appendChild(row);
  }
}

async function loadTopology() {
  const minConf = el("minconf").value;
  state.topology = await api(`/api/v1/regions/${state.region}/topology?min_confidence=${minConf}`);
  state.topology ? state.notReady.delete("topology") : state.notReady.add("topology");
  drawLinks();
  drawNodes();
  showNotReady();
}

async function loadRegion(region) {
  state.region = region;
  state.notReady.clear();
  setStatus(`Loading ${region}…`);
  const base = `/api/v1/regions/${region}`;
  const [resilience, residuals, placement] = await Promise.all([
    api(`${base}/resilience`), api(`${base}/links/residuals?limit=1000`), api(`${base}/placement?top=10`),
  ]);
  if (!resilience) state.notReady.add("resilience");
  if (!residuals) state.notReady.add("residuals");
  if (!placement) state.notReady.add("placement");
  state.residuals = new Map((residuals?.links || []).map((l) => [linkKey(l.a, l.b), l]));
  drawSpofs(resilience);
  drawPlacement(placement);
  await loadTopology();
  fitToNodes();
}

function fitToNodes() {
  const points = (state.topology?.nodes || []).filter((n) => n.location).map((n) => latlng(n.location));
  if (points.length) map.fitBounds(points, { padding: [20, 20] });
}

async function init() {
  const { regions } = await api("/api/v1/regions") || { regions: [] };
  const select = el("region");
  for (const r of regions) select.add(new Option(r.region, r.region));
  if (!regions.length) {
    setStatus("No regions have summaries yet.");
    return;
  }
  select.addEventListener("change", () => loadRegion(select.value).catch(fail));
  el("minconf").addEventListener("input", () => { el("minconf-value").textContent = Number(el("minconf").value).toFixed(2); });
  el("minconf").addEventListener("change", () => loadTopology().catch(fail));
  el("residuals").addEventListener("change", () => { drawLinks(); drawLegend(); });
  drawLegend();
  await loadRegion(regions[0].region);
}

function fail(err) { setStatus(`Error: ${err.message}`); }

init().catch(fail);
