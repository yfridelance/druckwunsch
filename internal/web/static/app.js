// Small progressive enhancements; everything works without JS except the
// bookmarklet landing page.
(function () {
  "use strict";

  document.addEventListener("submit", function (e) {
    var msg = e.target.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) e.preventDefault();
  });

  document.addEventListener("change", function (e) {
    if (e.target.hasAttribute("data-autosubmit")) e.target.form.submit();
    var f = e.target.form;
    if (f && f.hasAttribute("data-autosave")) f.submit();
  });

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (t.matches(".thumbs img")) {
      var main = document.getElementById("main-img");
      if (main) main.src = t.getAttribute("data-full");
      document.querySelectorAll(".thumbs img.sel").forEach(function (x) { x.classList.remove("sel"); });
      t.classList.add("sel");
    }
    if (t.closest && t.closest("[data-bookmarklet]")) {
      e.preventDefault();
      alert("Zieh den Knopf in die Lesezeichenleiste – hier anklicken hat keine Wirkung.");
    }
  });

  // close the user menu when clicking elsewhere
  document.addEventListener("click", function (e) {
    document.querySelectorAll("details.menu[open]").forEach(function (d) {
      if (!d.contains(e.target)) d.removeAttribute("open");
    });
  });

  document.addEventListener("focusin", function (e) {
    if (e.target.hasAttribute("data-select")) e.target.select();
  });

  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch(function () {});
  }

  // Bookmarklet landing: the payload arrives in the URL fragment.
  var landing = document.getElementById("ingest-landing");
  if (landing) {
    var status = document.getElementById("ingest-status");
    var fail = function (msg) {
      status.removeAttribute("aria-busy");
      status.textContent = msg;
    };
    var raw = location.hash.slice(1);
    history.replaceState(null, "", location.pathname);
    if (!raw) {
      fail("Keine Daten erhalten. Bitte das Lesezeichen auf einer Modellseite verwenden.");
      return;
    }
    var body;
    try {
      body = decodeURIComponent(raw);
      JSON.parse(body);
    } catch (err) {
      fail("Die Daten konnten nicht gelesen werden.");
      return;
    }
    fetch("/ingest", { method: "POST", headers: { "Content-Type": "application/json" }, body: body })
      .then(function (r) {
        if (!r.ok) return r.text().then(function (t) { throw new Error(t || r.status); });
        return r.json();
      })
      .then(function (d) { location.replace(d.next); })
      .catch(function (err) { fail("Übernahme fehlgeschlagen: " + err.message); });
  }
})();
