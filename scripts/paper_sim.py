#!/usr/bin/env python3
"""paper_sim.py — paper-trade simulator over an edge-scan CSV.

Inputs an edge-scan CSV (with or without --truth), a time window, a
starting bankroll, and position caps. Walks the rows in time order,
applies the H4 entry filter (port of internal/edge.entryFilterAdmits),
and simulates entries subject to bankroll/per-ticker/per-series caps.

Outputs:
  - # paired trades in window
  - # H4-admitted
  - # actually entered (constrained by bankroll)
  - notional committed
  - realized PnL  (if has_truth populated for every row)
  - expected PnL  (computed from p_fair × payoff − cost − fee)
  - per-ticker breakdown of the largest 8 positions

Assumptions:
  - Each entry buys exactly 1 contract at the observed taker price
    (conservative: real maker-only execution would save ~1¢ + fees).
  - Fees are the Kalshi parabolic taker fee as already computed in
    the CSV.
  - The strategy "side" is chosen by which side has the larger
    post-fee edge ≥ threshold (matches internal/edge.Scan).
  - Settlement is end-of-civil-day; positions opened in the window
    resolve when the bracket's valid_date settles. Realized PnL
    requires has_truth=true on every row (truth available).
"""

from __future__ import annotations
import argparse
import csv
import datetime as dt
import sys
from collections import defaultdict
from dataclasses import dataclass, field


@dataclass
class Position:
    ticker: str
    series: str
    side: str            # "yes" or "no"
    contracts: int       # always positive
    entry_price_cents: int  # for the side chosen (YES = trade_price; NO = 100 − trade_price)
    fee_cents: int
    p_fair: float        # model's prob this bracket resolves YES
    yes_resolved: bool | None  # None = unknown (no truth)
    realized_pnl_cents: int = 0


# Port of internal/edge.entryFilterAdmits — exact same semantics.
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


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--csv", required=True, help="edge-scan output CSV")
    p.add_argument("--window-minutes", type=int, default=30,
                   help="trailing window length to simulate (default 30)")
    p.add_argument("--window-end-ts-ms", type=int, default=0,
                   help="end of window in ts_ms (default: latest row in CSV)")
    p.add_argument("--bankroll", type=float, default=20.00,
                   help="starting bankroll in dollars (default 20.00)")
    p.add_argument("--max-per-ticker", type=float, default=2.00,
                   help="max dollars deployable to a single ticker (default 2.00 = 10%% of bankroll)")
    p.add_argument("--max-per-series", type=float, default=5.00,
                   help="max dollars deployable to a single series (default 5.00 = 25%% of bankroll)")
    p.add_argument("--edge-threshold", type=int, default=5,
                   help="min post-fee edge (cents) required for entry (default 5; matches edge-scan default)")
    p.add_argument("--entry-filter", default="h4",
                   choices=["none", "h4"],
                   help="entry filter: 'h4' (Phase 0.5 v4) or 'none' (default 'h4')")
    p.add_argument("--label", default="paper-sim",
                   help="label for the run header")
    args = p.parse_args()

    # Read all rows; collect time range.
    with open(args.csv) as f:
        all_rows = list(csv.DictReader(f))
    if not all_rows:
        print(f"{args.label}: CSV has 0 rows")
        return 1

    all_rows.sort(key=lambda r: parse_int(r["ts_ms"]))
    ts_min = parse_int(all_rows[0]["ts_ms"])
    ts_max = parse_int(all_rows[-1]["ts_ms"])

    window_end = args.window_end_ts_ms or ts_max
    window_start = window_end - args.window_minutes * 60 * 1000
    rows = [r for r in all_rows if window_start <= parse_int(r["ts_ms"]) <= window_end]

    # Banner
    fmt_ts = lambda ms: dt.datetime.fromtimestamp(ms / 1000, dt.timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    print(f"\n{'='*70}")
    print(f"paper_sim — {args.label}")
    print(f"{'='*70}")
    print(f"CSV:          {args.csv}")
    print(f"CSV ts range: {fmt_ts(ts_min)} → {fmt_ts(ts_max)}")
    print(f"window:       {fmt_ts(window_start)} → {fmt_ts(window_end)}  ({args.window_minutes} min)")
    print(f"bankroll:     ${args.bankroll:.2f}")
    print(f"per-ticker $: ${args.max_per_ticker:.2f}   per-series $: ${args.max_per_series:.2f}")
    print(f"filter:       {args.entry_filter}   edge-threshold: {args.edge_threshold}¢ post-fee")
    print()
    print(f"paired trades in CSV:       {len(all_rows):,}")
    print(f"paired trades in window:    {len(rows):,}")

    # Walk rows in time order; simulate.
    bankroll_used_cents = 0
    per_ticker_used: dict[str, int] = defaultdict(int)
    per_series_used: dict[str, int] = defaultdict(int)
    positions: list[Position] = []
    admitted = 0
    skipped_no_edge = 0
    skipped_bankroll = 0
    skipped_per_ticker = 0
    skipped_per_series = 0

    bankroll_cents = int(args.bankroll * 100)
    max_per_ticker_cents = int(args.max_per_ticker * 100)
    max_per_series_cents = int(args.max_per_series * 100)

    truth_present = True

    for r in rows:
        series = r["series"]
        ticker = r["ticker"]
        trade_price = parse_int(r["trade_price_cents"])
        p_fair = parse_float(r["p_fair"])
        fee = parse_int(r["fee_cents"])
        has_truth = r.get("has_truth") == "true"
        if not has_truth:
            truth_present = False
        yes_resolved = r.get("yes_resolved") == "true" if has_truth else None

        # Filter
        if args.entry_filter == "h4" and not h4_admits(series, trade_price):
            continue
        admitted += 1

        # Decide side (replicate scan.go logic).
        no_price = 100 - trade_price
        edge_yes = int(round(p_fair * 100)) - trade_price - fee
        edge_no = int(round((1 - p_fair) * 100)) - no_price - fee
        if edge_yes >= args.edge_threshold and edge_yes >= edge_no:
            side = "yes"
            entry_price_cents = trade_price
        elif edge_no >= args.edge_threshold:
            side = "no"
            entry_price_cents = no_price
        else:
            skipped_no_edge += 1
            continue

        cost_cents = entry_price_cents + fee  # 1 contract
        if bankroll_used_cents + cost_cents > bankroll_cents:
            skipped_bankroll += 1
            continue
        if per_ticker_used[ticker] + cost_cents > max_per_ticker_cents:
            skipped_per_ticker += 1
            continue
        if per_series_used[series] + cost_cents > max_per_series_cents:
            skipped_per_series += 1
            continue

        bankroll_used_cents += cost_cents
        per_ticker_used[ticker] += cost_cents
        per_series_used[series] += cost_cents

        pos = Position(
            ticker=ticker, series=series, side=side, contracts=1,
            entry_price_cents=entry_price_cents, fee_cents=fee,
            p_fair=p_fair, yes_resolved=yes_resolved,
        )
        if yes_resolved is not None:
            yes_won = yes_resolved if side == "yes" else not yes_resolved
            pos.realized_pnl_cents = (100 - entry_price_cents - fee) if yes_won else (-entry_price_cents - fee)
        positions.append(pos)

    # Aggregate
    n_entered = len(positions)
    n_won = sum(1 for x in positions if x.realized_pnl_cents > 0)
    realized = sum(x.realized_pnl_cents for x in positions)
    # Expected PnL: E[payoff for the chosen side] − cost − fee
    expected = 0.0
    for x in positions:
        p_side = x.p_fair if x.side == "yes" else (1 - x.p_fair)
        expected_payoff = p_side * 100  # expected gross payoff on a 1-contract position
        expected += expected_payoff - x.entry_price_cents - x.fee_cents

    print()
    print(f"## filter + entry decision")
    print(f"  H4-admitted:                {admitted:,}  ({100*admitted/max(1,len(rows)):.1f}% of window)")
    print(f"  rejected: no_edge:           {skipped_no_edge:,}")
    print(f"  rejected: bankroll exhausted: {skipped_bankroll:,}")
    print(f"  rejected: per-ticker cap:    {skipped_per_ticker:,}")
    print(f"  rejected: per-series cap:    {skipped_per_series:,}")
    print(f"  ENTERED:                    {n_entered:,}")
    print()
    print(f"## bankroll utilization")
    print(f"  starting bankroll:  ${args.bankroll:.2f}")
    print(f"  deployed:           ${bankroll_used_cents/100:.2f}  ({100*bankroll_used_cents/max(1,bankroll_cents):.1f}%)")
    print(f"  cash remaining:     ${(bankroll_cents - bankroll_used_cents)/100:.2f}")
    print()
    print(f"## PnL")
    if truth_present and n_entered > 0:
        print(f"  REALIZED:           ${realized/100:+.2f}  ({realized:+,}¢, {n_won}/{n_entered} won = {100*n_won/n_entered:.1f}%)")
        print(f"  expected (model):   ${expected/100:+.2f}  ({expected:+.1f}¢)")
        print(f"  realized − expected: ${(realized-expected)/100:+.2f}  (alpha realization vs model EV)")
    elif n_entered > 0:
        print(f"  REALIZED:           pending settlement (truth not yet available)")
        print(f"  expected (model):   ${expected/100:+.2f}  ({expected:+.1f}¢)")
        # show the bracket valid_dates so user knows when truth lands
        valid_dates = sorted(set(p.ticker.split("-")[1] for p in positions))
        print(f"  truth pending for:  {', '.join(valid_dates)}  (CLI bulletin issues morning after)")
    else:
        print(f"  no positions entered")
        return 0

    # Per-ticker top positions
    by_ticker: dict[str, list[Position]] = defaultdict(list)
    for x in positions:
        by_ticker[x.ticker].append(x)
    rows_summary = []
    for tk, ps in by_ticker.items():
        n = len(ps)
        cost = sum(x.entry_price_cents + x.fee_cents for x in ps)
        real = sum(x.realized_pnl_cents for x in ps)
        avg_p_fair = sum(x.p_fair for x in ps) / n
        rows_summary.append((tk, ps[0].side, n, cost, real, avg_p_fair))
    rows_summary.sort(key=lambda r: -abs(r[4]) if truth_present else -r[3])
    print()
    print(f"## top {min(8, len(rows_summary))} tickers")
    print(f"  {'ticker':<28} {'side':>4} {'n':>4} {'cost ¢':>7} {'PnL ¢':>9} {'p_fair':>7}")
    for tk, side, n, cost, real, p_fair in rows_summary[:8]:
        pnl_field = f"{real:+,}" if truth_present else "pending"
        print(f"  {tk:<28} {side:>4} {n:>4} {cost:>7} {pnl_field:>9} {p_fair:>7.3f}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
