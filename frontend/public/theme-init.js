(function () {
    var theme = "slate";
    try {
        var stored = localStorage.getItem("opsagent_theme");
        if (stored === "slate" || stored === "graphite" || stored === "classic") theme = stored;
    } catch (e) {}
    document.documentElement.dataset.theme = theme;
})();
