/* Infra Manager: tema, konfirmasi aksi, muat ulang status. Tanpa dependensi. */
(function () {
  var root = document.documentElement;
  var themeBtn = document.getElementById("theme-toggle");

  function applyTheme(t) {
    root.dataset.theme = t;
    try { localStorage.setItem("portal-theme", t); } catch (e) {}
    if (themeBtn) {
      themeBtn.textContent = t === "dark" ? "Mode terang" : "Mode gelap";
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

  // tombol salin pada blok kode instruksi
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var block = btn.closest(".codeblock");
      var pre = block && block.querySelector("pre");
      if (!pre) return;
      var done = function () {
        var old = btn.textContent;
        btn.textContent = "Tersalin";
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

  // halaman instruksi: polling koneksi baru -> klaim otomatis
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
      mark.textContent = "TERDETEKSI";
      mark.className = "mark drift";
      text.textContent = "Koneksi baru terdeteksi, sedang diikat ke " + wName + "\u2026";
      fetch("/nodes/claim", {
        method: "POST",
        headers: { "X-Requested-With": "fetch", "Content-Type": "application/x-www-form-urlencoded" },
        body: body.toString()
      })
        .then(function (res) {
          return res.json().then(function (data) {
            if (!res.ok || !data.ok) throw new Error(data.error || "HTTP " + res.status);
            mark.textContent = "TERHUBUNG";
            mark.className = "mark ok";
            text.textContent = "Node " + wName + " terhubung. Membuka daftar node\u2026";
            setTimeout(function () {
              location.href = "/nodes?ok=" + encodeURIComponent("Node " + wName + " terhubung");
            }, 900);
          });
        })
        .catch(function (err) {
          mark.textContent = "GAGAL";
          mark.className = "mark drift";
          text.textContent = "Koneksi terdeteksi tapi klaim gagal: " + err.message;
          if (timer) clearInterval(timer);
        });
    }

    function manualButton(c) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "small";
      b.textContent = "Pakai " + c.short_id;
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
              manual.innerHTML = '<p class="hint">Ada koneksi di tunnel ini sebelum halaman dibuka; pakai bila memang milikmu:</p>';
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
          mark.textContent = "MENUNGGU";
          mark.className = "mark off";
          text.textContent = cands.length
            ? "Koneksi lama tercatat tapi belum diikat \u2014 pakai tombol di bawah atau jalankan perintah di atas."
            : "Menunggu cloudflared dari mesin ini terhubung\u2026 dicek tiap 3 detik.";
        })
        .catch(function (err) {
          text.textContent = "Polling gagal: " + err.message + " (akan dicoba lagi)";
        });
    }
    timer = setInterval(poll, 3000);
    poll();
  }

  // form hostname: filter node mengikuti tunnel + isi origin dari node
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
      html = '<p class="empty">Belum ada service terdaftar. <a href="/services/new">Tambah hostname pertama</a>.</p>';
    } else if (!drift.length) {
      html = '<p class="empty ok-text">Tidak ada drift. Semua hostname cocok dengan Cloudflare Tunnel dan Traefik. Diperiksa ' +
        esc(new Date(report.checked_at).toLocaleTimeString("id-ID")) + ".</p>";
    } else {
      html = '<ul class="drift-list">' + drift.map(function (s) {
        return '<li class="drift-item">' +
          '<div class="drift-top"><span class="mono host">' + esc(s.hostname) + '</span><span class="mark drift">DRIFT</span></div>' +
          '<ul class="reasons">' + (s.drift || []).map(function (d) { return "<li>" + esc(d) + "</li>"; }).join("") + "</ul>" +
          '<form method="post" action="/services/' + esc(s.name) + '/reconcile" class="inline">' +
          '<button type="submit" class="small">Sinkronkan ulang</button></form></li>';
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
    live.innerHTML = '<p class="loading">Memuat status dari Cloudflare, DNS, dan Traefik...</p>';
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
        live.innerHTML = '<p class="empty err-text">Gagal memuat status: ' + esc(err.message) + ".</p>" +
          '<p class="empty">Cek izin token Cloudflare atau layanan Traefik, lalu tekan Muat ulang status lagi.</p>';
      })
      .then(function () { refresh.disabled = false; });
  });
})();
