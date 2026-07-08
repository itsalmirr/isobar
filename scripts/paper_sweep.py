#!/usr/bin/env python3
"""paper_sweep.py — distribution of paper-sim PnL across all 30-min
slices in a backtest CSV.

For each non-overlapping 30-minute window in the CSV's time range,
runs the same paper-trade simulation paper_sim.py runs and aggregates
the realized PnL distribution.

Answers: "If I started the bot at a random 30-minute window in the
backtest period with $20, what range of outcomes should I expect?"
"""

from __future__ import annotations
import argparse
import csv
import datetime as dt
import statistics
import sys
from collections import defaultdict


def h4_admits(series: str, trade_price_cents: int) -> bool:
    if series == "KXHIGHNY":
        return False
    if series.startswith("KXLOWT"):
        return True
    if trade_price_cents <= 9 or trade_price_cents >= 91:
        return True
    return False


def parse_int(x: str) -> int:
    try:
        return int(x)
    except (ValueError, TypeError):
        return 0


def parse_float(x: str) -> float:
    try:
        return float(x)
    except (ValueError, TypeError):
        return 0.0


def simulate_window(rows: list[dict], bankroll_cents: int,
                    max_per_ticker_cents: int, max_per_series_cents: int,
                    edge_threshold: int) -> tuple[int, int, int, int]:
    """Returns (n_entered, n_won, realized_pnl_cents, deployed_cents)."""
    bankroll_used = 0
    per_ticker = defaultdict(int)
    per_series = defaultdict(int)
    n_entered = 0
    n_won = 0
    realized = 0

    for r in rows:
        series = r["series"]
        ticker = r["ticker"]
        trade_price = parse_int(r["trade_price_cents"])
        p_fair = parse_float(r["p_fair"])
        fee = parse_int(r["fee_cents"])

        if not h4_admits(series, trade_price):
            continue

        no_price = 100 - trade_price
        edge_yes = int(round(p_fair * 100)) - trade_price - fee
        edge_no = int(round((1 - p_fair) * 100)) - no_price - fee
        if edge_yes >= edge_threshold and edge_yes >= edge_no:
            side = "yes"
            entry = trade_price
        elif edge_no >= edge_threshold:
            side = "no"
            entry = no_price
        else:
            continue

        cost = entry + fee
        if bankroll_used + cost > bankroll_cents:
            continue
        if per_ticker[ticker] + cost > max_per_ticker_cents:
            continue
        if per_series[series] + cost > max_per_series_cents:
            continue

        bankroll_used += cost
        per_ticker[ticker] += cost
        per_series[series] += cost
        n_entered += 1

        yes_resolved = r.get("yes_resolved") == "true"
        yes_won = yes_resolved if side == "yes" else not yes_resolved
        pnl = (100 - entry - fee) if yes_won else (-entry - fee)
        realized += pnl
        if pnl > 0:
            n_won += 1

    return n_entered, n_won, realized, bankroll_used


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--csv", required=True)
    p.add_argument("--window-minutes", type=int, default=30)
    p.add_argument("--step-minutes", type=int, default=30,
                   help="step size between consecutive windows; default = window-minutes (non-overlapping)")
    p.add_argument("--bankroll", type=float, default=20.00)
    p.add_argument("--max-per-ticker", type=float, default=2.00)
    p.add_argument("--max-per-series", type=float, default=5.00)
    p.add_argument("--edge-threshold", type=int, default=5)
    p.add_argument("--label", default="paper-sweep")
    args = p.parse_args()

    with open(args.csv) as f:
        all_rows = list(csv.DictReader(f))
    all_rows.sort(key=lambda r: parse_int(r["ts_ms"]))
    if not all_rows:
        print("empty CSV")
        return 1

    ts_min = parse_int(all_rows[0]["ts_ms"])
    ts_max = parse_int(all_rows[-1]["ts_ms"])
    window_ms = args.window_minutes * 60 * 1000
    step_ms = args.step_minutes * 60 * 1000
    bankroll_cents = int(args.bankroll * 100)
    max_pt = int(args.max_per_ticker * 100)
    max_ps = int(args.max_per_series * 100)

    # Build per-window row buckets in one pass.
    windows = []
    cur_start = ts_min
    idx = 0
    while cur_start < ts_max:
        cur_end = cur_start + window_ms
        bucket = []
        while idx < len(all_rows) and parse_int(all_rows[idx]["ts_ms"]) < cur_end:
            if parse_int(all_rows[idx]["ts_ms"]) >= cur_start:
                bucket.append(all_rows[idx])
            idx += 1
        if bucket:
            windows.append((cur_start, cur_end, bucket))
        cur_start += step_ms
        # rewind idx to start of next window (since step may < window or windows may have gaps)
        if step_ms < window_ms:
            # we'd need to support overlapping; for now require non-overlapping
            pass
        else:
            # if step == window, idx is already at the start of next window
            pass

    fmt = lambda ms: dt.datetime.fromtimestamp(ms / 1000, dt.timezone.utc).strftime("%m-%d %H:%M UTC")

    pnls = []
    deploys = []
    n_entered_list = []
    n_won_list = []
    per_window_log = []
    for ws, we, bucket in windows:
        n_ent, n_won, pnl, dep = simulate_window(bucket, bankroll_cents, max_pt, max_ps, args.edge_threshold)
        if n_ent > 0:
            pnls.append(pnl)
            deploys.append(dep)
            n_entered_list.append(n_ent)
            n_won_list.append(n_won)
            per_window_log.append((ws, we, n_ent, n_won, pnl, dep))

    print(f"\n{'='*70}")
    print(f"paper_sweep — {args.label}")
    print(f"{'='*70}")
    print(f"CSV:               {args.csv}")
    print(f"CSV ts range:      {fmt(ts_min)} → {fmt(ts_max)}")
    print(f"window size:       {args.window_minutes} min   step: {args.step_minutes} min")
    print(f"bankroll:          ${args.bankroll:.2f}")
    print(f"per-ticker $:      ${args.max_per_ticker:.2f}   per-series $: ${args.max_per_series:.2f}")
    print(f"edge threshold:    {args.edge_threshold}¢")
    print(f"filter:            H4 (Phase 0.5 v4)")
    print()
    print(f"windows with ≥1 entry: {len(pnls)} / {len(windows)} total ({100*len(pnls)/max(1,len(windows)):.1f}%)")

    if not pnls:
        print("no windows produced entries")
        return 0

    pnls_sorted = sorted(pnls)
    n_positive = sum(1 for x in pnls if x > 0)
    n_negative = sum(1 for x in pnls if x < 0)
    n_zero = sum(1 for x in pnls if x == 0)

    print()
    print(f"## PnL distribution (cents per 30-min window)")
    print(f"  n windows:          {len(pnls)}")
    print(f"  mean:               {statistics.mean(pnls):+.1f}¢  (${statistics.mean(pnls)/100:+.2f})")
    print(f"  median:             {statistics.median(pnls):+.1f}¢  (${statistics.median(pnls)/100:+.2f})")
    print(f"  stdev:              {statistics.stdev(pnls):.1f}¢")
    print(f"  min (worst):        {min(pnls):+,}¢")
    print(f"  p10:                {pnls_sorted[len(pnls)//10]:+,}¢")
    print(f"  p25:                {pnls_sorted[len(pnls)//4]:+,}¢")
    print(f"  p75:                {pnls_sorted[3*len(pnls)//4]:+,}¢")
    print(f"  p90:                {pnls_sorted[9*len(pnls)//10]:+,}¢")
    print(f"  max (best):         {max(pnls):+,}¢")
    print()
    print(f"  positive windows:   {n_positive} ({100*n_positive/len(pnls):.1f}%)")
    print(f"  negative windows:   {n_negative} ({100*n_negative/len(pnls):.1f}%)")
    print(f"  zero windows:       {n_zero}")
    print()
    print(f"## aggregate stats")
    total_pnl = sum(pnls)
    total_deploy = sum(deploys)
    total_entered = sum(n_entered_list)
    total_won = sum(n_won_list)
    print(f"  total PnL across all windows:  {total_pnl:+,}¢  (${total_pnl/100:+.2f})")
    print(f"  total entered:                 {total_entered:,}")
    print(f"  total won:                     {total_won:,}  ({100*total_won/max(1,total_entered):.1f}%)")
    print(f"  avg deploy/window:             ${statistics.mean(deploys)/100:.2f} of $20 ({100*statistics.mean(deploys)/bankroll_cents:.1f}%)")
    print(f"  avg entries/window:            {statistics.mean(n_entered_list):.1f}")

    # Best & worst windows
    per_window_log.sort(key=lambda x: x[4])
    print()
    print(f"## 3 worst windows")
    for ws, we, n_ent, n_won, pnl, dep in per_window_log[:3]:
        print(f"  {fmt(ws)} → {fmt(we)}  n={n_ent} won={n_won} dep=${dep/100:.2f} PnL={pnl:+,}¢")
    print(f"## 3 best windows")
    for ws, we, n_ent, n_won, pnl, dep in per_window_log[-3:]:
        print(f"  {fmt(ws)} → {fmt(we)}  n={n_ent} won={n_won} dep=${dep/100:.2f} PnL={pnl:+,}¢")

    return 0


if __name__ == "__main__":
    sys.exit(main())
