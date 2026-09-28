/* Infra Manager: theme, action confirmations, status refresh. No dependencies. */
(function () {
  var root = document.documentElement;
  var themeBtn = document.getElementById("theme-toggle");

  function applyTheme(t) {
    root.dataset.theme = t;
    try { localStorage.setItem("portal-theme", t); } catch (e) {}
    if (themeBtn) {
      themeBtn.textContent = t === "dark" ? "Light mode" : "Dark mode";
      themeBtn.setAttribute("aria-pressed", String(t === "dark"));
    }
  }

  var saved = null;
  try { saved = localStorage.getItem("portal-theme"); } catch (e) {}
  if (saved === "dark" || saved === "light") {
    applyTheme(saved);
  } else {
    applyTheme(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  }
  if (themeBtn) {
    themeBtn.addEventListener("click", function () {
      applyTheme(root.dataset.theme === "dark" ? "light" : "dark");
    });
  }

  document.querySelectorAll("form[data-confirm]").forEach(function (f) {
    f.addEventListener("submit", function (e) {
      if (!window.confirm(f.getAttribute("data-confirm"))) e.preventDefault();
    });
  });

  // copy button on the instruction code blocks
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var block = btn.closest(".codeblock");
      var pre = block && block.querySelector("pre");
      if (!pre) return;
      var done = function () {
        var old = btn.textContent;
        btn.textContent = "Copied";
        setTimeout(function () { btn.textContent = old; }, 1500);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(pre.textContent).then(done, function () {});
      } else {
        var ta = document.createElement("textarea");
        ta.value = pre.textContent;
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand("copy"); done(); } catch (e) {}
        document.body.removeChild(ta);
      }
    });
  });

  // instruction page: poll for new connections -> auto claim
  var wait = document.querySelector("[data-wait]");
  if (wait) {
    var wName = wait.getAttribute("data-name");
    var wTunnel = wait.getAttribute("data-tunnel");
    var wOrigin = wait.getAttribute("data-origin") || "";
    var wNotes = wait.getAttribute("data-notes") || "";
    var mark = document.getElementById("wait-mark");
    var text = document.getElementById("wait-text");
    var manual = document.getElementById("wait-manual");
    var baseline = null;
    var timer = null;

    function claim(clientId) {
      var body = new URLSearchParams({
        client_id: clientId, tunnel_id: wTunnel, name: wName, origin: wOrigin, notes: wNotes
      });
      mark.textContent = "DETECTED";
      mark.className = "mark drift";
      text.textContent = "New connection detected, binding it to " + wName + "\u2026";
      fetch("/nodes/claim", {
        method: "POST",
        headers: { "X-Requested-With": "fetch", "Content-Type": "application/x-www-form-urlencoded" },
        body: body.toString()
      })
        .then(function (res) {
          return res.json().then(function (data) {
            if (!res.ok || !data.ok) throw new Error(data.error || "HTTP " + res.status);
            mark.textContent = "CONNECTED";
            mark.className = "mark ok";
            text.textContent = "Node " + wName + " connected. Opening the node list\u2026";
            setTimeout(function () {
              location.href = "/nodes?ok=" + encodeURIComponent("Node " + wName + " connected");
            }, 900);
          });
        })
        .catch(function (err) {
          mark.textContent = "FAILED";
          mark.className = "mark drift";
          text.textContent = "Connection detected but the claim failed: " + err.message;
          if (timer) clearInterval(timer);
        });
    }

    function manualButton(c) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "small";
      b.textContent = "Use " + c.short_id;
      b.addEventListener("click", function () {
        if (timer) clearInterval(timer);
        claim(c.client_id);
      });
      return b;
    }

    function poll() {
      fetch("/api/nodes", { headers: { Accept: "application/json" } })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (view) {
          var cands = (view.candidates || []).filter(function (c) { return c.tunnel_id === wTunnel; });
          if (baseline === null) {
            baseline = {};
            cands.forEach(function (c) { baseline[c.client_id] = true; });
            if (cands.length) {
              manual.hidden = false;
              manual.innerHTML = '<p class="hint">There was a connection on this tunnel before the page opened — use it if it is yours:</p>';
              cands.forEach(function (c) { manual.appendChild(manualButton(c)); });
            }
            return;
          }
          var fresh = cands.filter(function (c) { return !baseline[c.client_id]; });
          if (fresh.length) {
            if (timer) clearInterval(timer);
            claim(fresh[0].client_id);
            return;
          }
          mark.textContent = "WAITING";
          mark.className = "mark off";
          text.textContent = cands.length
            ? "An old connection is recorded but not bound \u2014 use the button below or run the command above."
            : "Waiting for cloudflared from this machine to connect\u2026 checked every 3 seconds.";
        })
        .catch(function (err) {
          text.textContent = "Polling failed: " + err.message + " (will retry)";
        });
    }
    timer = setInterval(poll, 3000);
    poll();
  }

  // hostname form: filter nodes by tunnel + fill origin from the node
  var tunnelSel = document.getElementById("tunnel_id");
  var nodeSel = document.getElementById("node_id");
  var originInput = document.getElementById("origin");
  if (tunnelSel && nodeSel && originInput) {
    var applyFilter = function () {
      var tid = tunnelSel.value;
      var visible = false;
      Array.prototype.forEach.call(nodeSel.querySelectorAll("option[data-tunnel]"), function (o) {
        var match = o.getAttribute("data-tunnel") === tid;
        o.hidden = !match;
        o.disabled = !match;
        if (match && o.selected) visible = true;
      });
      if (!visible) nodeSel.value = "";
    };
    tunnelSel.addEventListener("change", applyFilter);
    applyFilter();
    nodeSel.addEventListener("change", function () {
      var opt = nodeSel.options[nodeSel.selectedIndex];
      if (opt && opt.value && opt.getAttribute("data-origin")) {
        originInput.value = opt.getAttribute("data-origin");
      }
    });
  }

  var live = document.getElementById("live");
  var refresh = document.getElementById("refresh-drift");
  if (!live || !refresh) return;

  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function render(report) {
    var drift = (report.services || []).filter(function (s) { return (s.drift || []).length > 0; });
    var html;
    if (!(report.services || []).length) {
      html = '<p class="empty">No services registered yet. <a href="/services/new">Add your first hostname</a>.</p>';
    } else if (!drift.length) {
      html = '<p class="empty ok-text">No drift. Every hostname matches Cloudflare Tunnel and Traefik. Checked ' +
        esc(new Date(report.checked_at).toLocaleTimeString("en-GB")) + ".</p>";
    } else {
      html = '<ul class="drift-list">' + drift.map(function (s) {
        return '<li class="drift-item">' +
          '<div class="drift-top"><span class="mono host">' + esc(s.hostname) + '</span><span class="mark drift">DRIFT</span></div>' +
          '<ul class="reasons">' + (s.drift || []).map(function (d) { return "<li>" + esc(d) + "</li>"; }).join("") + "</ul>" +
          '<form method="post" action="/services/' + esc(s.name) + '/reconcile" class="inline">' +
          '<button type="submit" class="small">Re-sync</button></form></li>';
      }).join("") + "</ul>";
    }
    live.dataset.state = "ok";
    live.innerHTML = html;
    live.querySelectorAll("form[data-confirm], form").forEach(function (f) {
      f.addEventListener("submit", function () {});
    });
  }

  refresh.addEventListener("click", function () {
    refresh.disabled = true;
    live.dataset.state = "loading";
    live.innerHTML = '<p class="loading">Loading status from Cloudflare, DNS and Traefik...</p>';
    fetch("/api/report", { headers: { Accept: "application/json" } })
      .then(function (res) {
        return res.json().then(function (data) {
          if (!res.ok) throw new Error(data.error || "HTTP " + res.status);
          return data;
        });
      })
      .then(render)
      .catch(function (err) {
        live.dataset.state = "error";
        live.innerHTML = '<p class="empty err-text">Failed to load status: ' + esc(err.message) + ".</p>" +
          '<p class="empty">Check the Cloudflare token permissions or the Traefik service, then hit Refresh status again.</p>';
      })
      .then(function () { refresh.disabled = false; });
  });
})();
