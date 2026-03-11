"use strict";

(function () {
  // ---- Configuration ----
  const CHART_COLORS = [
    "#0969da",
    "#cf222e",
    "#1a7f37",
    "#8250df",
    "#bf8700",
    "#0550ae",
    "#da3633",
    "#2da44e",
  ];
  const POINT_RADIUS = 4;
  const POINT_HOVER_RADIUS = 6;

  // ---- DOM references ----
  const branchSelect = document.getElementById("branch-select");
  const cpuSelect = document.getElementById("cpu-select");
  const cpuModelSelect = document.getElementById("cpu-model-select");
  const cpuModelGroup = document.getElementById("cpu-model-group");
  const goosSelect = document.getElementById("goos-select");
  const goosGroup = document.getElementById("goos-group");
  const goarchSelect = document.getElementById("goarch-select");
  const goarchGroup = document.getElementById("goarch-group");
  const goversionSelect = document.getElementById("goversion-select");
  const goversionGroup = document.getElementById("goversion-group");
  const cgoCheckbox = document.getElementById("cgo-checkbox");
  const cgoGroup = document.getElementById("cgo-group");
  const filterInput = document.getElementById("filter-input");
  const packageTabsEl = document.getElementById("package-tabs");
  const mainEl = document.getElementById("main");
  const loadingMsg = document.getElementById("loading-msg");
  const lastUpdateEl = document.getElementById("last-update");
  const repoLinkEl = document.getElementById("repo-link");
  const dlButton = document.getElementById("dl-button");

  // ---- State ----
  let currentBranchData = null; // raw array of BenchmarkEntry
  let currentBranch = null;
  let currentPackage = null; // null = "All" or first tab
  let chartInstances = []; // keep references so we can destroy on re-render
  let goModulePath = ""; // Go module path from metadata, used to shorten package names

  // ---- Helpers ----

  function getBasePath() {
    const path = window.location.pathname;
    const base = path.replace(/\/index\.html$/, "");
    return base.endsWith("/") ? base : base + "/";
  }

  async function fetchJSON(url) {
    const resp = await fetch(url);
    if (!resp.ok) {
      throw new Error("HTTP " + resp.status + " fetching " + url);
    }
    return resp.json();
  }

  function showMessage(html) {
    mainEl.innerHTML = '<div class="state-message">' + html + "</div>";
  }

  function destroyCharts() {
    for (const c of chartInstances) {
      c.destroy();
    }
    chartInstances = [];
  }

  function formatDate(isoOrTimestamp) {
    try {
      const d =
        typeof isoOrTimestamp === "number"
          ? new Date(isoOrTimestamp)
          : new Date(isoOrTimestamp);
      return d.toLocaleString();
    } catch {
      return String(isoOrTimestamp);
    }
  }

  function shortSHA(sha) {
    return sha ? sha.slice(0, 7) : "?";
  }

  /**
   * Extract all unique packages from the data entries.
   * Falls back to legacy name parsing if `package` field is absent.
   */
  function extractPackages(entries) {
    const pkgs = new Set();
    for (const entry of entries) {
      for (const bench of entry.benchmarks) {
        if (bench.package) {
          pkgs.add(bench.package);
        }
      }
    }
    // Sort for stable ordering; shorter paths first
    return Array.from(pkgs).sort(function (a, b) {
      if (a.length !== b.length) return a.length - b.length;
      return a.localeCompare(b);
    });
  }

  /**
   * Extract all unique CPU/procs values from data entries.
   * Returns sorted array of numbers.
   */
  function extractCPUs(entries) {
    const cpus = new Set();
    for (const entry of entries) {
      for (const bench of entry.benchmarks) {
        if (bench.procs && bench.procs > 0) {
          cpus.add(bench.procs);
        }
      }
    }
    return Array.from(cpus).sort(function (a, b) {
      return a - b;
    });
  }

  /**
   * Extract all unique CPU model strings from data entries.
   * Returns sorted array of strings.
   */
  function extractCPUModels(entries) {
    const models = new Set();
    for (const entry of entries) {
      var cpu = entry.params ? entry.params.cpu : entry.cpu;
      if (cpu) {
        models.add(cpu);
      }
    }
    return Array.from(models).sort();
  }

  /**
   * Extract all unique GOOS values from data entries.
   * Returns sorted array of strings.
   */
  function extractGOOSValues(entries) {
    const values = new Set();
    for (const entry of entries) {
      var goos = entry.params ? entry.params.goos : entry.goos;
      if (goos) {
        values.add(goos);
      }
    }
    return Array.from(values).sort();
  }

  /**
   * Extract all unique GOARCH values from data entries.
   * Returns sorted array of strings.
   */
  function extractGOARCHValues(entries) {
    const values = new Set();
    for (const entry of entries) {
      var goarch = entry.params ? entry.params.goarch : entry.goarch;
      if (goarch) {
        values.add(goarch);
      }
    }
    return Array.from(values).sort();
  }

  /**
   * Extract all unique Go version strings from data entries.
   * Returns sorted array of strings.
   */
  function extractGoVersions(entries) {
    const values = new Set();
    for (const entry of entries) {
      var goVersion = entry.params ? entry.params.goVersion : "";
      if (goVersion) {
        values.add(goVersion);
      }
    }
    return Array.from(values).sort();
  }

  /**
   * Extract the set of CGO values present in data entries.
   * Returns a Set of booleans.
   */
  function extractCGOValues(entries) {
    const values = new Set();
    for (const entry of entries) {
      var cgo = entry.params ? entry.params.cgo : entry.cgo;
      values.add(!!cgo);
    }
    return values;
  }

  /**
   * Get the base benchmark name (for grouping).
   * Strips the " - unit" suffix if present.
   */
  function baseBenchName(name) {
    const idx = name.indexOf(" - ");
    return idx >= 0 ? name.substring(0, idx) : name;
  }

  /**
   * Get the metric label (the part after " - ", or the unit from the first metric).
   */
  function metricLabel(name) {
    const idx = name.indexOf(" - ");
    return idx >= 0 ? name.substring(idx + 3) : null;
  }

  /**
   * Collect benchmark data points per test case name, filtered by package and cpu.
   * Returns a Map<string, Array<{commit, date, bench}>>
   */
  function collectBenchesPerTestCase(
    entries,
    filterPkg,
    filterCPU,
    filterCPUModel,
    filterGOOS,
    filterGOARCH,
    filterGoVersion,
    filterCGO,
  ) {
    const map = new Map();
    for (const entry of entries) {
      var commit = entry.commit;
      var date = entry.date;
      var params = entry.params || {};
      var entryCPU = params.cpu || entry.cpu || "";

      // Filter by CPU model at entry level
      if (filterCPUModel !== null && entryCPU !== filterCPUModel) {
        continue;
      }

      // Filter by GOOS at entry level
      var entryGOOS = params.goos || entry.goos || "";
      if (filterGOOS !== null && entryGOOS !== filterGOOS) {
        continue;
      }

      // Filter by GOARCH at entry level
      var entryGOARCH = params.goarch || entry.goarch || "";
      if (filterGOARCH !== null && entryGOARCH !== filterGOARCH) {
        continue;
      }

      // Filter by Go version at entry level
      var entryGoVersion = params.goVersion || "";
      if (filterGoVersion !== null && entryGoVersion !== filterGoVersion) {
        continue;
      }

      // Filter by CGO status at entry level
      var entryCGO = entry.params ? params.cgo : entry.cgo;
      if (filterCGO !== null && !!entryCGO !== filterCGO) {
        continue;
      }

      for (const bench of entry.benchmarks) {
        // Filter by package
        if (filterPkg !== null && bench.package !== filterPkg) {
          continue;
        }
        // Filter by CPU count
        if (filterCPU !== null && bench.procs !== filterCPU) {
          continue;
        }

        var result = {
          commit: commit,
          date: date,
          bench: bench,
          cpu: entryCPU,
          params: params,
        };
        var arr = map.get(bench.name);
        if (!arr) {
          arr = [];
          map.set(bench.name, arr);
        }
        arr.push(result);
      }
    }
    return map;
  }

  /**
   * Group benchmark names by their base name.
   * Returns an array of { baseName, benchNames: string[] } in insertion order.
   */
  function groupBenchmarks(benchMap) {
    var groups = new Map(); // baseName -> string[]
    var groupOrder = [];

    for (const benchName of benchMap.keys()) {
      var base = baseBenchName(benchName);
      var arr = groups.get(base);
      if (!arr) {
        arr = [];
        groups.set(base, arr);
        groupOrder.push(base);
      }
      arr.push(benchName);
    }

    return groupOrder.map(function (base) {
      return { baseName: base, benchNames: groups.get(base) };
    });
  }

  // ---- Rendering ----

  function getChartColor(index) {
    return CHART_COLORS[index % CHART_COLORS.length];
  }

  function renderChart(container, name, displayTitle, dataset, colorIndex) {
    var card = document.createElement("div");
    card.className = "chart-card";

    var titleEl2 = document.createElement("h2");
    titleEl2.textContent = displayTitle;
    card.appendChild(titleEl2);

    var wrapper = document.createElement("div");
    wrapper.className = "chart-wrapper";
    card.appendChild(wrapper);

    var canvas = document.createElement("canvas");
    wrapper.appendChild(canvas);
    container.appendChild(card);

    var isReleases = currentBranch === "releases";
    var labels = dataset.map(function (d) {
      if (isReleases && d.commit.tag) {
        return d.commit.tag;
      }
      return shortSHA(d.commit.sha);
    });
    var values = dataset.map(function (d) {
      return d.bench.value;
    });
    var unit = dataset.length > 0 ? dataset[0].bench.unit : "";
    var displayUnit = unit;
    var scaleFactor = 1;

    // Convert B/op to human-readable SI units based on max value
    if (unit === "B/op") {
      var maxVal = values.length > 0 ? Math.max.apply(null, values) : 0;
      if (maxVal >= 1e9) {
        scaleFactor = 1e9;
        displayUnit = "GB/op";
      } else if (maxVal >= 1e6) {
        scaleFactor = 1e6;
        displayUnit = "MB/op";
      } else if (maxVal >= 1e3) {
        scaleFactor = 1e3;
        displayUnit = "KB/op";
      }
      if (scaleFactor > 1) {
        values = values.map(function (v) {
          return v / scaleFactor;
        });
      }
    }

    // Update chart card title to show actual (scaled) unit
    titleEl2.textContent = displayUnit;

    // Compute min value for log scale clipping
    var positiveValues = values.filter(function (v) {
      return v > 0;
    });
    var minVal =
      positiveValues.length > 0 ? Math.min.apply(null, positiveValues) : 1;
    // Clip lower bound: round down to nearest power of 10 below the minimum
    var logMin = Math.pow(10, Math.floor(Math.log10(minVal)));
    var hasZeros = values.some(function (v) {
      return v <= 0;
    });

    var color = getChartColor(colorIndex || 0);
    var colorAlpha = color + "30";

    var isDarkMode =
      window.matchMedia &&
      window.matchMedia("(prefers-color-scheme: dark)").matches;
    var gridColor = isDarkMode ? "rgba(255,255,255,0.1)" : "rgba(0,0,0,0.08)";
    var textColor = isDarkMode ? "#8b949e" : "#656d76";

    var chart = new Chart(canvas, {
      type: "line",
      data: {
        labels: labels,
        datasets: [
          {
            label: name,
            data: values,
            borderColor: color,
            backgroundColor: colorAlpha,
            borderWidth: 2,
            pointRadius: POINT_RADIUS,
            pointHoverRadius: POINT_HOVER_RADIUS,
            pointBackgroundColor: color,
            fill: true,
            tension: 0.15,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        interaction: {
          mode: "index",
          intersect: false,
        },
        scales: {
          x: {
            title: {
              display: true,
              text: isReleases ? "Release" : "Commit",
              color: textColor,
            },
            ticks: { color: textColor },
            grid: { color: gridColor },
          },
          y: {
            type: hasZeros ? "linear" : "logarithmic",
            title: {
              display: false,
            },
            min: hasZeros ? 0 : logMin,
            ticks: { color: textColor },
            grid: { color: gridColor },
          },
        },
        plugins: {
          legend: {
            display: false,
          },
          tooltip: {
            callbacks: {
              title: function (items) {
                if (!items.length) return "";
                var idx = items[0].dataIndex;
                var d = dataset[idx];
                if (isReleases && d.commit.tag) {
                  return d.commit.tag + " (" + shortSHA(d.commit.sha) + ")";
                }
                return "Commit: " + shortSHA(d.commit.sha);
              },
              beforeBody: function (items) {
                if (!items.length) return "";
                var idx = items[0].dataIndex;
                var d = dataset[idx];
                var lines = [];
                if (d.commit.message) {
                  lines.push(d.commit.message);
                }
                lines.push("");
                if (d.cpu) {
                  lines.push("CPU: " + d.cpu);
                }
                if (d.params.goos) {
                  lines.push("GOOS: " + d.params.goos);
                }
                if (d.params.goarch) {
                  lines.push("GOARCH: " + d.params.goarch);
                }
                if (d.params.goVersion) {
                  lines.push("Go: " + d.params.goVersion);
                }
                lines.push("CGO: " + !!d.params.cgo);
                if (d.commit.date) {
                  lines.push("Date: " + formatDate(d.commit.date));
                }
                if (d.commit.author) {
                  lines.push("Author: @" + d.commit.author);
                }
                return lines.join("\n");
              },
              label: function (item) {
                return item.formattedValue + " " + displayUnit;
              },
              afterLabel: function (item) {
                var idx = item.dataIndex;
                var d = dataset[idx];
                return d.bench.extra ? "\n" + d.bench.extra : "";
              },
            },
          },
        },
        onClick: function (_event, elements) {
          if (!elements || elements.length === 0) return;
          var idx = elements[0].index;
          var url = dataset[idx].commit.url;
          if (url) {
            window.open(url, "_blank");
          }
        },
      },
    });

    chartInstances.push(chart);
  }

  function renderBranch(entries) {
    destroyCharts();
    mainEl.innerHTML = "";

    if (!entries || entries.length === 0) {
      showMessage("No benchmark data available for this branch.");
      return;
    }

    // Determine active filters
    var filterCPU = null;
    var cpuVal = cpuSelect.value;
    if (cpuVal) {
      filterCPU = parseInt(cpuVal, 10);
    }

    var filterCPUModel = null;
    var cpuModelVal = cpuModelSelect.value;
    if (cpuModelVal) {
      filterCPUModel = cpuModelVal;
    }

    var filterGOOS = null;
    var goosVal = goosSelect.value;
    if (goosVal) {
      filterGOOS = goosVal;
    }

    var filterGOARCH = null;
    var goarchVal = goarchSelect.value;
    if (goarchVal) {
      filterGOARCH = goarchVal;
    }

    var filterGoVersion = null;
    var goversionVal = goversionSelect.value;
    if (goversionVal) {
      filterGoVersion = goversionVal;
    }

    // CGO filter: only apply when both values exist in data
    var filterCGO = null;
    var cgoValues = extractCGOValues(entries);
    if (cgoValues.has(true) && cgoValues.has(false)) {
      filterCGO = cgoCheckbox.checked;
    }

    var filterPkg = currentPackage;

    var benchMap = collectBenchesPerTestCase(
      entries,
      filterPkg,
      filterCPU,
      filterCPUModel,
      filterGOOS,
      filterGOARCH,
      filterGoVersion,
      filterCGO,
    );

    // Remove benchmarks that don't have data for the latest commit.
    // This hides graphs for benchmarks that have been removed from the codebase
    // (e.g. memory reporting turned off, entire benchmarks deleted).
    // We find the latest commit by walking entries backwards (already sorted
    // chronologically) and applying the same entry-level filters.
    if (benchMap.size > 0) {
      var latestBenchNames = new Set();
      var latestCommitSHA = null;
      for (var ei = entries.length - 1; ei >= 0; ei--) {
        var ent = entries[ei];
        var entParams = ent.params || {};
        if (
          filterCPUModel !== null &&
          (entParams.cpu || ent.cpu || "") !== filterCPUModel
        )
          continue;
        if (
          filterGOOS !== null &&
          (entParams.goos || ent.goos || "") !== filterGOOS
        )
          continue;
        if (
          filterGOARCH !== null &&
          (entParams.goarch || ent.goarch || "") !== filterGOARCH
        )
          continue;
        if (
          filterGoVersion !== null &&
          (entParams.goVersion || "") !== filterGoVersion
        )
          continue;
        var entCGO = ent.params ? entParams.cgo : ent.cgo;
        if (filterCGO !== null && !!entCGO !== filterCGO) continue;
        var matched = false;
        for (var bi = 0; bi < ent.benchmarks.length; bi++) {
          var b = ent.benchmarks[bi];
          if (filterPkg !== null && b.package !== filterPkg) continue;
          if (filterCPU !== null && b.procs !== filterCPU) continue;
          matched = true;
        }
        if (matched) {
          latestCommitSHA = ent.commit.sha;
          break;
        }
      }
      // Collect all benchmark names present in entries with the latest commit SHA
      // (there may be multiple entries for the same commit, e.g. different CPU models).
      if (latestCommitSHA) {
        for (var ei2 = entries.length - 1; ei2 >= 0; ei2--) {
          var ent2 = entries[ei2];
          if (ent2.commit.sha !== latestCommitSHA) continue;
          var entParams2 = ent2.params || {};
          if (
            filterCPUModel !== null &&
            (entParams2.cpu || ent2.cpu || "") !== filterCPUModel
          )
            continue;
          if (
            filterGOOS !== null &&
            (entParams2.goos || ent2.goos || "") !== filterGOOS
          )
            continue;
          if (
            filterGOARCH !== null &&
            (entParams2.goarch || ent2.goarch || "") !== filterGOARCH
          )
            continue;
          if (
            filterGoVersion !== null &&
            (entParams2.goVersion || "") !== filterGoVersion
          )
            continue;
          var entCGO2 = ent2.params ? entParams2.cgo : ent2.cgo;
          if (filterCGO !== null && !!entCGO2 !== filterCGO) continue;
          for (var bi2 = 0; bi2 < ent2.benchmarks.length; bi2++) {
            var b2 = ent2.benchmarks[bi2];
            if (filterPkg !== null && b2.package !== filterPkg) continue;
            if (filterCPU !== null && b2.procs !== filterCPU) continue;
            latestBenchNames.add(b2.name);
          }
        }
        for (const [key] of benchMap) {
          if (!latestBenchNames.has(key)) {
            benchMap.delete(key);
          }
        }
      }
    }

    var filterText = (filterInput.value || "").toLowerCase().trim();

    // Apply text filter
    if (filterText) {
      for (const [key] of benchMap) {
        if (!key.toLowerCase().includes(filterText)) {
          benchMap.delete(key);
        }
      }
    }

    if (benchMap.size === 0) {
      showMessage("No benchmarks match the current filter.");
      return;
    }

    // Group benchmarks by base name
    var groups = groupBenchmarks(benchMap);
    var rendered = 0;

    for (var gi = 0; gi < groups.length; gi++) {
      var group = groups[gi];
      var groupEl = document.createElement("div");
      groupEl.className = "bench-group";

      // Group title
      var titleEl = document.createElement("div");
      titleEl.className = "bench-group-title";
      titleEl.textContent = group.baseName;
      groupEl.appendChild(titleEl);

      // Charts container (grid)
      var chartsEl = document.createElement("div");
      chartsEl.className = "bench-group-charts";
      groupEl.appendChild(chartsEl);

      for (var ci = 0; ci < group.benchNames.length; ci++) {
        var benchName = group.benchNames[ci];
        var dataset = benchMap.get(benchName);
        if (!dataset || dataset.length === 0) continue;

        // Display title: metric label if grouped, otherwise the unit
        var metric = metricLabel(benchName);
        var displayTitle = metric ? metric : dataset[0].bench.unit;

        renderChart(chartsEl, benchName, displayTitle, dataset, ci);
        rendered++;
      }

      mainEl.appendChild(groupEl);
    }

    if (rendered === 0) {
      showMessage("No benchmarks match the current filter.");
    }
  }

  // ---- Package tabs ----

  /**
   * Strip the Go module path prefix from a full package import path.
   * E.g. "github.com/user/repo/internal/storage" -> "internal/storage"
   * Falls back to the full path if the module prefix doesn't match.
   */
  function relativePackageName(fullPkg) {
    if (!goModulePath) return fullPkg;
    var prefix = goModulePath;
    if (!prefix.endsWith("/")) prefix += "/";
    if (fullPkg.startsWith(prefix)) {
      return fullPkg.substring(prefix.length) || fullPkg;
    }
    if (fullPkg === goModulePath) {
      return ".";
    }
    return fullPkg;
  }

  function renderPackageTabs(packages) {
    packageTabsEl.innerHTML = "";

    if (packages.length <= 1) {
      // Single or no package: use that package as default filter, no tabs needed
      currentPackage = packages.length === 1 ? packages[0] : null;
      return;
    }

    // "All" tab
    var allTab = document.createElement("button");
    allTab.className = "package-tab";
    allTab.textContent = "All";
    allTab.dataset.pkg = "__all__";
    packageTabsEl.appendChild(allTab);

    for (var i = 0; i < packages.length; i++) {
      var tab = document.createElement("button");
      tab.className = "package-tab";
      // Show relative path with Go module prefix stripped
      tab.textContent = relativePackageName(packages[i]);
      tab.title = packages[i]; // full path on hover
      tab.dataset.pkg = packages[i];
      packageTabsEl.appendChild(tab);
    }

    // Set initial active tab
    if (currentPackage === null) {
      // Default to "All"
      allTab.classList.add("active");
    } else {
      setActivePackageTab(currentPackage);
    }
  }

  function setActivePackageTab(pkg) {
    var tabs = packageTabsEl.querySelectorAll(".package-tab");
    for (var i = 0; i < tabs.length; i++) {
      var tab = tabs[i];
      if (pkg === null && tab.dataset.pkg === "__all__") {
        tab.classList.add("active");
      } else if (tab.dataset.pkg === pkg) {
        tab.classList.add("active");
      } else {
        tab.classList.remove("active");
      }
    }
  }

  packageTabsEl.addEventListener("click", function (e) {
    var tab = e.target.closest(".package-tab");
    if (!tab) return;

    var pkg = tab.dataset.pkg;
    currentPackage = pkg === "__all__" ? null : pkg;
    setActivePackageTab(currentPackage);

    if (currentBranchData) {
      renderBranch(currentBranchData);
    }
  });

  // ---- CPU selector ----

  function populateCPUSelector(entries) {
    var cpus = extractCPUs(entries);
    var currentVal = cpuSelect.value;

    cpuSelect.innerHTML = "";

    for (var i = 0; i < cpus.length; i++) {
      var opt = document.createElement("option");
      opt.value = String(cpus[i]);
      opt.textContent = String(cpus[i]);
      cpuSelect.appendChild(opt);
    }

    // Restore previous selection if still valid, otherwise select first available
    if (currentVal && cpus.indexOf(parseInt(currentVal, 10)) >= 0) {
      cpuSelect.value = currentVal;
    } else if (cpus.length > 0) {
      cpuSelect.value = String(cpus[0]);
    }
  }

  cpuSelect.addEventListener("change", function () {
    if (currentBranchData) {
      renderBranch(currentBranchData);
    }
  });

  // ---- CPU Model selector ----

  function populateCPUModelSelector(entries) {
    var models = extractCPUModels(entries);
    var currentVal = cpuModelSelect.value;

    cpuModelSelect.innerHTML = "";

    for (var i = 0; i < models.length; i++) {
      var opt = document.createElement("option");
      opt.value = models[i];
      opt.textContent = models[i];
      cpuModelSelect.appendChild(opt);
    }

    // Hide the selector when there are 0 or 1 CPU models (no choice to make)
    if (models.length <= 1) {
      cpuModelGroup.style.display = "none";
    } else {
      cpuModelGroup.style.display = "flex";
      if (currentVal && models.indexOf(currentVal) >= 0) {
        cpuModelSelect.value = currentVal;
      } else {
        cpuModelSelect.value = models[0];
      }
    }
  }

  cpuModelSelect.addEventListener("change", function () {
    if (currentBranchData) {
      renderBranch(currentBranchData);
    }
  });

  // ---- GOOS selector ----

  function populateGOOSSelector(entries) {
    var values = extractGOOSValues(entries);
    var currentVal = goosSelect.value;

    goosSelect.innerHTML = "";

    for (var i = 0; i < values.length; i++) {
      var opt = document.createElement("option");
      opt.value = values[i];
      opt.textContent = values[i];
      goosSelect.appendChild(opt);
    }

    if (values.length <= 1) {
      goosGroup.style.display = "none";
    } else {
      goosGroup.style.display = "flex";
      if (currentVal && values.indexOf(currentVal) >= 0) {
        goosSelect.value = currentVal;
      } else {
        goosSelect.value = values[0];
      }
    }
  }

  goosSelect.addEventListener("change", function () {
    if (currentBranchData) {
      renderBranch(currentBranchData);
    }
  });

  // ---- GOARCH selector ----

  function populateGOARCHSelector(entries) {
    var values = extractGOARCHValues(entries);
    var currentVal = goarchSelect.value;

    goarchSelect.innerHTML = "";

    for (var i = 0; i < values.length; i++) {
      var opt = document.createElement("option");
      opt.value = values[i];
      opt.textContent = values[i];
      goarchSelect.appendChild(opt);
    }

    if (values.length <= 1) {
      goarchGroup.style.display = "none";
    } else {
      goarchGroup.style.display = "flex";
      if (currentVal && values.indexOf(currentVal) >= 0) {
        goarchSelect.value = currentVal;
      } else {
        goarchSelect.value = values[0];
      }
    }
  }

  goarchSelect.addEventListener("change", function () {
    if (currentBranchData) {
      renderBranch(currentBranchData);
    }
  });

  // ---- Go Version selector ----

  function populateGoVersionSelector(entries) {
    var values = extractGoVersions(entries);
    var currentVal = goversionSelect.value;

    goversionSelect.innerHTML = "";

    for (var i = 0; i < values.length; i++) {
      var opt = document.createElement("option");
      opt.value = values[i];
      opt.textContent = values[i];
      goversionSelect.appendChild(opt);
    }

    if (values.length <= 1) {
      goversionGroup.style.display = "none";
    } else {
      goversionGroup.style.display = "flex";
      if (currentVal && values.indexOf(currentVal) >= 0) {
        goversionSelect.value = currentVal;
      } else {
        goversionSelect.value = values[0];
      }
    }
  }

  goversionSelect.addEventListener("change", function () {
    if (currentBranchData) {
      renderBranch(currentBranchData);
    }
  });

  // ---- CGO checkbox ----

  function populateCGOCheckbox(entries) {
    var values = extractCGOValues(entries);
    var hasBoth = values.has(true) && values.has(false);

    // Always show the checkbox
    cgoGroup.style.display = "flex";
    cgoCheckbox.checked = values.has(true);

    if (hasBoth) {
      // Data has both CGO states: allow the user to toggle
      cgoCheckbox.disabled = false;
    } else {
      // Only one CGO state: show current state but disable toggling
      cgoCheckbox.disabled = true;
    }
  }

  cgoCheckbox.addEventListener("change", function () {
    if (currentBranchData) {
      renderBranch(currentBranchData);
    }
  });

  // ---- Data loading ----

  async function loadMetadata() {
    try {
      var base = getBasePath();
      var metadata = await fetchJSON(base + "metadata.json");
      if (metadata.lastUpdate) {
        lastUpdateEl.textContent = formatDate(metadata.lastUpdate);
      }
      if (metadata.repoUrl) {
        repoLinkEl.href = metadata.repoUrl;
        repoLinkEl.textContent = metadata.repoUrl;
      }
      if (metadata.goModule) {
        goModulePath = metadata.goModule;
      }
    } catch {
      // metadata.json is optional
      lastUpdateEl.textContent = "\u2014";
    }
  }

  async function loadBranches() {
    var base = getBasePath();
    var branches = await fetchJSON(base + "branches.json");
    return branches;
  }

  async function loadBranchData(branch) {
    var base = getBasePath();
    var safeName = branch.replace(/[/\\:*?"<>|]/g, "_");
    var data = await fetchJSON(base + "data/" + safeName + ".json");

    // For the "releases" virtual branch, try to attach the tag name to each
    // entry by loading the tag map that the store command generates.
    if (branch === "releases") {
      try {
        var tagMap = await fetchJSON(base + "data/release_tags.json");
        if (tagMap) {
          for (var i = 0; i < data.length; i++) {
            var sha = data[i].commit && data[i].commit.sha;
            if (sha && tagMap[sha]) {
              data[i].commit.tag = tagMap[sha];
            }
          }
        }
      } catch (_e) {
        // release_tags.json is optional; entries will use short SHA labels
      }
    }

    return data;
  }

  async function selectBranch(branch) {
    if (!branch) return;
    currentBranch = branch;

    destroyCharts();
    mainEl.innerHTML = "";
    packageTabsEl.innerHTML = "";
    showMessage('<span class="spinner"></span> Loading benchmark data\u2026');
    dlButton.disabled = true;

    try {
      currentBranchData = await loadBranchData(branch);
      dlButton.disabled = false;

      // Populate CPU selectors and CGO checkbox from data
      populateCPUSelector(currentBranchData);
      populateCPUModelSelector(currentBranchData);
      populateGOOSSelector(currentBranchData);
      populateGOARCHSelector(currentBranchData);
      populateGoVersionSelector(currentBranchData);
      populateCGOCheckbox(currentBranchData);

      // Extract and render package tabs
      var packages = extractPackages(currentBranchData);
      currentPackage = null; // reset on branch change
      renderPackageTabs(packages);

      renderBranch(currentBranchData);
    } catch (err) {
      showMessage(
        "Error loading data for branch <b>" + branch + "</b>: " + err.message,
      );
    }
  }

  // ---- Event listeners ----

  branchSelect.addEventListener("change", function () {
    selectBranch(branchSelect.value);
    if (branchSelect.value) {
      updateHash();
    }
  });

  var filterTimeout = null;
  filterInput.addEventListener("input", function () {
    clearTimeout(filterTimeout);
    filterTimeout = setTimeout(function () {
      if (currentBranchData) {
        renderBranch(currentBranchData);
      }
    }, 200);
  });

  dlButton.addEventListener("click", function () {
    if (!currentBranchData) return;
    var json = JSON.stringify(currentBranchData, null, 2);
    var blob = new Blob([json], { type: "application/json" });
    var url = URL.createObjectURL(blob);
    var a = document.createElement("a");
    a.href = url;
    a.download = (currentBranch || "benchmark") + ".json";
    a.click();
    URL.revokeObjectURL(url);
  });

  // ---- URL hash persistence ----

  function updateHash() {
    var params = new URLSearchParams();
    if (currentBranch) {
      params.set("branch", currentBranch);
    }
    window.location.hash = params.toString();
  }

  // ---- Initialization ----

  async function init() {
    await loadMetadata();

    var branches;
    try {
      branches = await loadBranches();
    } catch (err) {
      showMessage(
        "Could not load branch list. Make sure benchmark data has been generated.<br><small>" +
          err.message +
          "</small>",
      );
      return;
    }

    if (!branches || branches.length === 0) {
      showMessage("No branches with benchmark data found.");
      return;
    }

    // Populate branch selector.
    // "releases" is always shown first with a special label; individual semver
    // tags are hidden (they are aggregated under "releases").
    branchSelect.innerHTML = "";
    for (var i = 0; i < branches.length; i++) {
      var brName = branches[i];
      var opt = document.createElement("option");
      opt.value = brName;
      if (brName === "releases") {
        opt.textContent = "📦 releases";
      } else {
        opt.textContent = brName;
      }
      branchSelect.appendChild(opt);
    }

    // Try to select from URL hash
    var initialBranch = branches[0];
    var hash = window.location.hash;
    if (hash) {
      var params = new URLSearchParams(hash.slice(1));
      var requested = params.get("branch");
      if (requested && branches.indexOf(requested) >= 0) {
        initialBranch = requested;
      }
    }

    branchSelect.value = initialBranch;
    await selectBranch(initialBranch);
  }

  init();
})();
