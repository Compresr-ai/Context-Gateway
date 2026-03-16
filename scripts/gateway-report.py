#!/usr/bin/env python3
"""Context Gateway statistics report.

Reads telemetry.jsonl and compression.jsonl produced by the gateway
and prints savings, compression ratios, daily usage, and threshold analysis.

Usage:
  ./gateway-report.py                        # auto-detect: logs/ in cwd, then ./logs/
  ./gateway-report.py path/to/logs/          # read from a specific directory
  ./gateway-report.py telemetry.jsonl        # read a specific file (compression.jsonl next to it)
  ./gateway-report.py --docker [container]   # read from a running Docker container
"""

import json, subprocess, sys, statistics, urllib.request
from pathlib import Path
from dataclasses import dataclass, field

TELEMETRY_FILE = "telemetry.jsonl"
COMPRESSION_FILE = "compression.jsonl"
DOCKER_LOG_DIR = "/app/logs"
LITELLM_PRICES_URL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
FALLBACK_PRICES = {"opus": 15.0, "sonnet": 3.0, "haiku": 1.0}


def fetch_prices() -> dict[str, float]:
    """Fetch per-model input prices ($/MTok) from LiteLLM."""
    try:
        with urllib.request.urlopen(LITELLM_PRICES_URL, timeout=5) as resp:
            data = json.loads(resp.read())
        prices = {}
        for model_id, info in data.items():
            cost = info.get("input_cost_per_token")
            if cost and "claude" in model_id:
                prices[model_id] = cost * 1e6  # per-token → per-MTok
        return prices
    except Exception:
        return {}


LIVE_PRICES: dict[str, float] = fetch_prices()


# ── Data structures ──────────────────────────────────────────────

@dataclass
class ModelStats:
    name: str
    count: int = 0
    orig_tokens: int = 0
    saved_tokens: int = 0
    money_saved: float = 0.0


@dataclass
class TokenStats:
    total_requests: int = 0
    compressed_requests: int = 0
    passthrough_requests: int = 0
    total_orig_tokens: int = 0


@dataclass
class DailyStats:
    active_days: int = 0
    avg_tokens_per_day: float = 0.0
    min_tokens_per_day: int = 0
    max_tokens_per_day: int = 0
    median_tokens_per_day: float = 0.0
    daily_values: list[int] = field(default_factory=list)
    total_saved_tokens: int = 0
    total_money_saved: float = 0.0
    total_shadows: int = 0
    total_expands: int = 0
    models: dict = field(default_factory=dict)


@dataclass
class ToolStats:
    name: str
    total: int = 0
    compressed: int = 0
    orig_bytes: int = 0
    saved_bytes: int = 0


@dataclass
class ThresholdResult:
    threshold: int
    calls: int
    ratio: float
    saved: int
    wasted: int
    roi: float


@dataclass
class SizeStats:
    all_sizes: list = field(default_factory=list)
    compressed_sizes: list = field(default_factory=list)
    passthrough_sizes: list = field(default_factory=list)
    ratios: list = field(default_factory=list)
    tools: dict = field(default_factory=dict)
    status_counts: dict = field(default_factory=dict)
    current_threshold: int = 0
    thresholds: list = field(default_factory=list)
    sweet_spot: int = 0
    compression_cost: float = 0.0


# ── Formatting helpers ───────────────────────────────────────────

def get_price(model: str) -> float:
    if LIVE_PRICES and model in LIVE_PRICES:
        return LIVE_PRICES[model]
    for key, price in FALLBACK_PRICES.items():
        if key in model:
            return price
    return 3.0


def fmt_num(n: int) -> str:
    if n >= 1_000_000:
        return f"{n / 1_000_000:.1f}M"
    if n >= 1_000:
        return f"{n / 1_000:.1f}K"
    return str(n)


def fmt_tokens(n: int) -> str:
    return fmt_num(n)


def fmt_bytes(n: int) -> str:
    if n >= 1_048_576:
        return f"{n / 1_048_576:.1f}MB"
    if n >= 1024:
        return f"{n / 1024:.1f}KB"
    return f"{n}B"


def fmt_money(d: float) -> str:
    return f"${d:,.2f}"


def fmt_pct(n: float) -> str:
    return f"{n:.1f}%"


def bar(ratio: float, width: int = 20) -> str:
    filled = int(ratio * width)
    return "█" * filled + "░" * (width - filled)


def header(title: str, width: int = 72) -> str:
    pad = width - len(title) - 4
    left = pad // 2
    right = pad - left
    return f"╭{'─' * left}  {title}  {'─' * right}╮"


def print_table(headers: list[str], rows: list[list[str]], aligns: str = "",
                 annotations: list[str] | None = None) -> None:
    if not rows:
        return
    cols = len(headers)
    if not aligns:
        aligns = "l" * cols
    aligns = aligns.ljust(cols, "l")
    if not annotations:
        annotations = [""] * len(rows)

    widths = [len(h) for h in headers]
    for row in rows:
        for i, cell in enumerate(row):
            widths[i] = max(widths[i], len(cell))

    def fmt_row(cells: list[str], sep: str = "│") -> str:
        parts = []
        for i, cell in enumerate(cells):
            if aligns[i] == "r":
                parts.append(cell.rjust(widths[i]))
            else:
                parts.append(cell.ljust(widths[i]))
        return f"  {sep} " + f" {sep} ".join(parts) + f" {sep}"

    divider = "  ├─" + "─┼─".join("─" * w for w in widths) + "─┤"
    top =     "  ┌─" + "─┬─".join("─" * w for w in widths) + "─┐"
    bottom =  "  └─" + "─┴─".join("─" * w for w in widths) + "─┘"

    print(top)
    print(fmt_row(headers))
    print(divider)
    for row, ann in zip(rows, annotations):
        print(fmt_row(row) + ann)
    print(bottom)


# ── Data loading ─────────────────────────────────────────────────

def load_from_container(container: str, path: str) -> str:
    result = subprocess.run(
        ["docker", "exec", container, "cat", path],
        capture_output=True, text=True,
    )
    return result.stdout if result.returncode == 0 else ""


def find_container() -> str:
    result = subprocess.run(
        ["docker", "compose", "ps", "-q", "context-gateway"],
        capture_output=True, text=True,
    )
    cid = result.stdout.strip()
    if not cid:
        sys.exit("No running context-gateway container found.\nUsage: ./telemetry-report.py [container]")
    return cid


def parse_jsonl(raw: str) -> list[dict]:
    entries = []
    for line in raw.splitlines():
        line = line.strip()
        if line:
            try:
                entries.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return entries


# ── Analysis (no printing) ───────────────────────────────────────

def analyze_tokens(entries: list[dict]) -> TokenStats | None:
    msgs = [e for e in entries if e.get("path") == "/v1/messages"]
    if not msgs:
        return None

    stats = TokenStats()
    compressed = [m for m in msgs if m.get("compression_used")]
    passthrough = [m for m in msgs if not m.get("compression_used")]

    stats.total_requests = len(msgs)
    stats.compressed_requests = len(compressed)
    stats.passthrough_requests = len(passthrough)
    stats.total_orig_tokens = sum(m.get("original_tokens", 0) for m in msgs)
    stats.total_saved_tokens = sum(m.get("tokens_saved", 0) for m in compressed)
    stats.total_shadows = sum(m.get("shadow_refs_created", 0) for m in compressed)
    stats.total_expands = sum(m.get("expand_calls_found", 0) for m in compressed)
    stats.total_money_saved = sum(
        m.get("tokens_saved", 0) / 1e6 * get_price(m.get("model", ""))
        for m in compressed
    )

    models: dict[str, ModelStats] = {}
    for m in msgs:
        model = m.get("model", "unknown")
        s = models.setdefault(model, ModelStats(name=model))
        s.count += 1
        s.orig_tokens += m.get("original_tokens", 0)
        s.saved_tokens += m.get("tokens_saved", 0)
    for s in models.values():
        s.money_saved = s.saved_tokens / 1e6 * get_price(s.name)
    stats.models = models

    return stats


def analyze_sizes(entries: list[dict]) -> SizeStats | None:
    if not entries:
        return None

    stats = SizeStats()

    for rec in entries:
        orig = rec.get("original_bytes", 0)
        if orig == 0:
            continue
        status = rec.get("status", "")
        tool = rec.get("tool_name", "unknown")

        stats.all_sizes.append(orig)
        stats.status_counts[status] = stats.status_counts.get(status, 0) + 1

        t = stats.tools.setdefault(tool, ToolStats(name=tool))
        t.total += 1
        t.orig_bytes += orig

        if status == "compressed":
            stats.compressed_sizes.append(orig)
            comp = rec.get("compressed_bytes", orig)
            t.compressed += 1
            t.saved_bytes += orig - comp
            if orig > 0:
                stats.ratios.append(comp / orig)
        else:
            stats.passthrough_sizes.append(orig)

    if not stats.all_sizes:
        return None

    # Current threshold
    for rec in reversed(entries):
        t = rec.get("min_threshold", 0)
        if t > 0:
            stats.current_threshold = t
            break

    # Threshold analysis
    compressed_data = []
    for rec in entries:
        orig = rec.get("original_bytes", 0)
        if orig == 0 or rec.get("status") != "compressed":
            continue
        comp = rec.get("compressed_bytes", orig)
        compressed_data.append({"orig": orig, "comp": comp, "ratio": comp / orig})

    if len(compressed_data) >= 10:
        total_comp_input = 0
        for threshold in [256, 512, 768, 1024, 1536, 2048, 3072, 4096]:
            eligible = [c for c in compressed_data if c["orig"] >= threshold]
            if not eligible:
                continue
            wasted = sum(1 for c in eligible if c["ratio"] >= 0.9)
            saved = sum(c["orig"] - c["comp"] for c in eligible if c["ratio"] < 0.9)
            avg_ratio = statistics.mean(c["ratio"] for c in eligible)
            api_input = sum(c["orig"] for c in eligible)
            api_cost = (api_input / 4) / 1e6 * get_price("claude-haiku-4-5-20251001")
            main_savings = (saved / 4) / 1e6 * 15.0
            roi = main_savings / api_cost if api_cost > 0 else 0
            r = ThresholdResult(threshold=threshold, calls=len(eligible),
                                ratio=avg_ratio, saved=saved, wasted=wasted, roi=roi)
            stats.thresholds.append(r)
            if threshold == stats.current_threshold:
                total_comp_input = api_input

        # Sweet spot
        for r in stats.thresholds:
            if r.wasted == 0:
                stats.sweet_spot = r.threshold
                break

        # Estimate compression cost (haiku input for all compressed outputs)
        if total_comp_input:
            stats.compression_cost = (total_comp_input / 4) / 1e6 * get_price("claude-haiku-4-5-20251001")

    return stats


def analyze_daily(entries: list[dict]) -> DailyStats | None:
    """Analyze daily token usage (only active days)."""
    from datetime import datetime
    from collections import defaultdict

    msgs = [e for e in entries if e.get("path") == "/v1/messages"]
    if not msgs:
        return None

    # Group by date
    daily_tokens: dict[str, int] = defaultdict(int)
    for m in msgs:
        ts = m.get("timestamp", "")
        if not ts:
            continue
        date = datetime.fromisoformat(ts[:19]).date()
        daily_tokens[str(date)] += m.get("original_tokens", 0)

    # Filter active days (> 0 tokens)
    active_values = [tokens for tokens in daily_tokens.values() if tokens > 0]
    if not active_values:
        return None

    return DailyStats(
        active_days=len(active_values),
        avg_tokens_per_day=statistics.mean(active_values),
        min_tokens_per_day=min(active_values),
        max_tokens_per_day=max(active_values),
        median_tokens_per_day=statistics.median(active_values),
        daily_values=sorted(active_values),
    )


# ── Display ──────────────────────────────────────────────────────

def print_business_summary(tokens: TokenStats | None, sizes: SizeStats | None) -> None:
    if not tokens:
        return

    print()
    print(header("Bottom Line"))
    print()

    saved = tokens.total_money_saved
    cost = sizes.compression_cost if sizes else 0
    net = saved - cost

    print(f"  Saved on main models    {fmt_money(saved):>10}")
    if cost > 0:
        print(f"  Compression cost        {fmt_money(cost):>10}  (Haiku API calls)")
        print(f"                          ──────────")
        print(f"  Net savings             {fmt_money(net):>10}")
    print()

    if cost > 0:
        ratio = saved / cost
        print(f"  Every $1 spent on Haiku saves ${ratio:,.0f} on Opus")
    print(f"  {fmt_tokens(tokens.total_saved_tokens)} tokens saved across {tokens.compressed_requests:,} compressed requests")

    if tokens.total_shadows:
        sufficiency = (1 - tokens.total_expands / tokens.total_shadows) * 100
        print(f"  {tokens.total_shadows:,} compressions delivered, {tokens.total_expands} needed the original ({fmt_pct(sufficiency)} good enough)")

    if sizes and sizes.ratios:
        avg_r = statistics.mean(sizes.ratios)
        print(f"  Average output compressed to {fmt_pct(avg_r * 100)} of original size")

    print()


def print_daily(stats: DailyStats | None) -> None:
    if not stats:
        return

    print(header("Daily Usage (Active Days Only)"))
    print()
    print(f"  Active days    {stats.active_days:>8,}   (days with any token usage)")
    print(f"  Avg per day    {fmt_tokens(int(stats.avg_tokens_per_day)):>8}")
    print(f"  Median         {fmt_tokens(int(stats.median_tokens_per_day)):>8}")
    print(f"  Range          {fmt_tokens(stats.min_tokens_per_day):>8} — {fmt_tokens(stats.max_tokens_per_day)}")
    print()


def print_tokens(stats: TokenStats) -> None:
    pct = (stats.total_saved_tokens / stats.total_orig_tokens * 100) if stats.total_orig_tokens else 0

    print(header("Token Savings"))
    print()
    print(f"  Requests       {stats.total_requests:>8,}   ({stats.compressed_requests:,} compressed, {stats.passthrough_requests:,} passthrough)")
    print(f"  Tokens in      {fmt_tokens(stats.total_orig_tokens):>8}")
    print(f"  Tokens saved   {fmt_tokens(stats.total_saved_tokens):>8}   {bar(pct / 100, 15)} {fmt_pct(pct)}")
    print(f"  Money saved    {fmt_money(stats.total_money_saved):>8}")
    print()

    rows = []
    for s in sorted(stats.models.values(), key=lambda x: -x.money_saved):
        p = (s.saved_tokens / s.orig_tokens * 100) if s.orig_tokens else 0
        short = s.name.replace("claude-", "").replace("-20250929", "").replace("-20251001", "")
        rows.append([short, str(s.count), fmt_tokens(s.saved_tokens), fmt_pct(p), fmt_money(s.money_saved)])

    print_table(["Model", "Reqs", "Saved", "%", "Value"], rows, "lrrrr")
    print()


def print_sizes(stats: SizeStats) -> None:
    print(header("Size Distribution"))
    print()

    def stat_row(label: str, data: list[int]) -> list[str]:
        if not data:
            return [label, "--", "--", "--", "--", "--"]
        data_sorted = sorted(data)
        n = len(data_sorted)
        p90 = fmt_bytes(data_sorted[int(n * 0.9)]) if n >= 20 else "--"
        return [
            label,
            f"{n:,}",
            fmt_bytes(int(statistics.median(data_sorted))),
            fmt_bytes(int(statistics.mean(data_sorted))),
            p90,
            f"{fmt_bytes(data_sorted[0])}..{fmt_bytes(data_sorted[-1])}",
        ]

    print_table(
        ["", "Count", "Median", "Avg", "p90", "Range"],
        [
            stat_row("All outputs", stats.all_sizes),
            stat_row("Compressed", stats.compressed_sizes),
            stat_row("Passthrough", stats.passthrough_sizes),
        ],
        "lrrrrr",
    )

    if stats.ratios:
        print()
        avg_r = statistics.mean(stats.ratios)
        med_r = statistics.median(stats.ratios)
        print(f"  Compression ratio    {bar(avg_r, 20)}  avg {avg_r:.2f}  median {med_r:.2f}  best {min(stats.ratios):.2f}  worst {max(stats.ratios):.2f}")
    print()

    # By Tool
    print(header("By Tool"))
    print()

    rows = []
    for t in sorted(stats.tools.values(), key=lambda x: -x.total):
        saved = fmt_bytes(t.saved_bytes) if t.saved_bytes else "--"
        comp_str = f"{t.compressed}" if t.compressed else "--"
        rows.append([t.name, f"{t.total:,}", comp_str, saved])

    print_table(["Tool", "Outputs", "Compressed", "Saved"], rows, "lrrr")
    print()

    # Status breakdown
    total_n = sum(stats.status_counts.values())
    rows = []
    for s, c in sorted(stats.status_counts.items(), key=lambda x: -x[1]):
        pct = c / total_n * 100 if total_n else 0
        rows.append([s, f"{c:,}", f"{bar(pct / 100, 12)} {fmt_pct(pct)}"])

    print_table(["Status", "Count", "Distribution"], rows, "lrl")
    print()


def print_thresholds(stats: SizeStats) -> None:
    if not stats.thresholds:
        return

    print(header("Threshold Analysis"))
    print()

    rows = []
    annotations = []
    for r in stats.thresholds:
        markers = []
        if stats.current_threshold and r.threshold == stats.current_threshold:
            markers.append("current")
        if stats.sweet_spot and r.threshold == stats.sweet_spot and stats.sweet_spot != stats.current_threshold:
            markers.append("sweet spot")
        ann = f"  <-- {', '.join(markers)}" if markers else ""
        rows.append([
            f"{r.threshold:,}",
            str(r.calls),
            f"{r.ratio:.2f}",
            fmt_bytes(r.saved),
            str(r.wasted),
            f"{r.roi:.0f}x",
        ])
        annotations.append(ann)

    print_table(["Threshold", "Calls", "Ratio", "Saved", "Wasted", "ROI"], rows, "rrrrrr", annotations)
    if stats.sweet_spot and stats.current_threshold:
        if stats.sweet_spot == stats.current_threshold:
            print(f"  Current threshold ({stats.current_threshold:,}B) is the sweet spot -- 0 wasted calls, maximum savings")
        elif stats.sweet_spot < stats.current_threshold:
            print(f"  Sweet spot is {stats.sweet_spot:,}B (0 wasted), current is {stats.current_threshold:,}B -- could lower for more savings")
        else:
            print(f"  Sweet spot is {stats.sweet_spot:,}B (0 wasted), current is {stats.current_threshold:,}B -- consider raising to eliminate waste")
    print()

    # Size bucket breakdown
    compressed_data = []
    for r in stats.thresholds:
        # Reconstruct from stats — use ratios and compressed_sizes
        pass

    # Use ratios + compressed_sizes directly
    # Need original compressed records — pass through stats
    print_size_buckets(stats)


def print_size_buckets(stats: SizeStats) -> None:
    # Reconstruct compressed records from all_sizes won't work cleanly.
    # Instead, we store them during analysis. But to keep it simple,
    # just use compressed_sizes and ratios (same order).
    if not stats.compressed_sizes or not stats.ratios:
        return

    # Pair them up (they were appended in the same order during analysis)
    compressed = []
    for size, ratio in zip(stats.compressed_sizes, stats.ratios):
        comp = int(size * ratio)
        compressed.append({"orig": size, "comp": comp, "ratio": ratio})

    print("  Size buckets:")
    buckets = [(0, 512, "0B-512B"), (512, 1024, "512B-1KB"), (1024, 2048, "1KB-2KB"), (2048, 4096, "2KB-4KB"), (4096, 65536, "4KB-64KB")]
    for lo, hi, label in buckets:
        bucket = [c for c in compressed if lo <= c["orig"] < hi]
        if not bucket:
            continue
        avg = statistics.mean(c["ratio"] for c in bucket)
        bad = sum(1 for c in bucket if c["ratio"] >= 0.9)
        saved = sum(c["orig"] - c["comp"] for c in bucket)
        wasted_str = f"  wasted: {bad}" if bad else ""
        print(f"    {label:>8}   n={fmt_num(len(bucket)):>5}   {bar(avg, 12)} {avg:.2f}   saved {fmt_bytes(saved):>8}{wasted_str}")
    print()


# ── Main ─────────────────────────────────────────────────────────

def find_log_dir() -> Path | None:
    """Auto-detect log directory: check common locations."""
    candidates = [
        Path("logs"),           # cwd/logs/ (running from gateway root)
        Path("."),              # files in cwd directly
        Path("/app/logs"),      # inside container (if someone runs it there)
    ]
    for d in candidates:
        if (d / TELEMETRY_FILE).exists():
            return d
    return None


def load_from_dir(log_dir: Path) -> tuple[list[dict], list[dict]]:
    """Load telemetry and compression data from a directory."""
    tel_path = log_dir / TELEMETRY_FILE
    comp_path = log_dir / COMPRESSION_FILE
    telemetry = parse_jsonl(tel_path.read_text()) if tel_path.exists() else []
    compression = parse_jsonl(comp_path.read_text()) if comp_path.exists() else []
    return telemetry, compression


def load_from_file(file_path: Path) -> tuple[list[dict], list[dict]]:
    """Load from a specific telemetry file; look for compression.jsonl next to it."""
    telemetry = parse_jsonl(file_path.read_text())
    comp_path = file_path.parent / COMPRESSION_FILE
    compression = parse_jsonl(comp_path.read_text()) if comp_path.exists() else []
    return telemetry, compression


def load_from_docker(container: str | None = None) -> tuple[list[dict], list[dict]]:
    """Load from a running Docker container."""
    cid = container if container else find_container()
    telemetry = parse_jsonl(load_from_container(cid, f"{DOCKER_LOG_DIR}/{TELEMETRY_FILE}"))
    compression = parse_jsonl(load_from_container(cid, f"{DOCKER_LOG_DIR}/{COMPRESSION_FILE}"))
    return telemetry, compression


def main() -> None:
    args = sys.argv[1:]

    # --docker [container]
    if args and args[0] == "--docker":
        container = args[1] if len(args) > 1 else None
        telemetry_raw, compression_raw = load_from_docker(container)
    elif args:
        target = Path(args[0])
        if target.is_dir():
            telemetry_raw, compression_raw = load_from_dir(target)
        elif target.is_file():
            telemetry_raw, compression_raw = load_from_file(target)
        else:
            sys.exit(f"Not found: {target}")
    else:
        # Auto-detect
        log_dir = find_log_dir()
        if log_dir:
            telemetry_raw, compression_raw = load_from_dir(log_dir)
        else:
            sys.exit(
                "No log files found. Usage:\n"
                "  ./gateway-report.py                        # auto-detect logs/ in cwd\n"
                "  ./gateway-report.py path/to/logs/          # specific directory\n"
                "  ./gateway-report.py telemetry.jsonl         # specific file\n"
                "  ./gateway-report.py --docker [container]    # from Docker container"
            )

    # Analyze
    token_stats = analyze_tokens(telemetry_raw)
    size_stats = analyze_sizes(compression_raw)
    daily_stats = analyze_daily(telemetry_raw)

    # Display: business summary first
    print_business_summary(token_stats, size_stats)
    print_daily(daily_stats)

    # Detailed reports
    if token_stats:
        print_tokens(token_stats)
    if size_stats:
        print_sizes(size_stats)
        print_thresholds(size_stats)


if __name__ == "__main__":
    main()
