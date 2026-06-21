const DL2_API = "http://localhost:8787";

async function refresh() {
  const statusEl = document.getElementById("serverStatus");
  const jobsEl = document.getElementById("jobs");

  let jobs = [];
  try {
    const resp = await fetch(`${DL2_API}/jobs`);
    if (!resp.ok) throw new Error("bad status");
    jobs = await resp.json();
    statusEl.textContent = "dl2 server connected";
    statusEl.className = "status ok";
  } catch (err) {
    statusEl.textContent = "dl2 server not running (start server.exe). Falling back to normal downloads.";
    statusEl.className = "status err";
    jobsEl.innerHTML = "";
    return;
  }

  if (!jobs || jobs.length === 0) {
    jobsEl.innerHTML = '<div class="empty">No downloads yet</div>';
    return;
  }

  jobsEl.innerHTML = jobs
    .slice()
    .reverse()
    .map((j) => {
      const pct = j.progress && j.progress.TotalBytes
        ? Math.min(100, (j.progress.DownloadedBytes / j.progress.TotalBytes) * 100)
        : 0;
      const speed = j.progress && j.progress.SpeedBytesPerS
        ? (j.progress.SpeedBytesPerS / 1e6).toFixed(2) + " MB/s"
        : "--";
      const stateLabel = j.state === "done" ? "Done" : j.state === "failed" ? "Failed" : `${pct.toFixed(0)}%`;
      const name = j.output ? j.output.split(/[\\/]/).pop() : j.url;

      return `
        <div class="job">
          <div class="job-name" title="${name}">${name}</div>
          <div class="bar-bg">
            <div class="bar-fill" style="width:${j.state === 'done' ? 100 : pct}%; background:${j.state === 'failed' ? '#f87171' : '#6366f1'}"></div>
          </div>
          <div class="job-meta">
            <span>${stateLabel}</span>
            <span>${j.state === "running" ? speed : ""}</span>
          </div>
        </div>
      `;
    })
    .join("");
}

refresh();
setInterval(refresh, 1500);
