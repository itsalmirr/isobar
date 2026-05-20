#!/usr/bin/env python3
"""Slice MOS+Bayes backtest CSV to find profitable subsets.

We want to discover an entry-filter rule of the form:
  enter only if (h_remain ≤ X) AND (obs_weight ≥ Y) AND (edge ≥ Z) AND ...
that yields positive realized PnL on the 2026-04-29..2026-05-04 window.

If a subset exists, it becomes the strategy. If no subset is reliably
positive, the calibrated thesis is dead.
"""

from __future__ import annotations
import csv
import statistics
import sys
from collections import defaultdict
from dataclasses import dataclass

PATH = "reports/backtest-realized-bayes/edges-2026-04-29_2026-05-04.csv"


@dataclass
class Row:
    valid_date: str
    series: str
    ticker: str
    bracket_kind: str
    trade_price_cents: int
    p_fair: float
    p_fair_bayes: float
    edge_cents: int
    bayes_applied: bool
    bayes_h_remain: int
    bayes_obs_weight: float
    has_truth: bool
    our_side: str
    our_pnl_cents: int
    fee_cents: int
    lead_hours: float  # ts_ms vs civil-day close


def load() -> list[Row]:
    out: list[Row] = []
    with open(PATH) as f:
        for r in csv.DictReader(f):
            if not r["our_side"]:
                continue
            out.append(
                Row(
                    valid_date=r["valid_date"],
                    series=r["series"],
                    ticker=r["ticker"],
                    bracket_kind=r["bracket_kind"],
                    trade_price_cents=int(r["trade_price_cents"]),
                    p_fair=float(r["p_fair"]),
                    p_fair_bayes=float(r["p_fair_bayes"]) if r["p_fair_bayes"] else 0,
                    edge_cents=int(r["edge_cents"]),
                    bayes_applied=r["bayes_applied"] == "true",
                    bayes_h_remain=int(r["bayes_h_remain"]) if r["bayes_h_remain"] else 0,
                    bayes_obs_weight=float(r["bayes_obs_weight"]) if r["bayes_obs_weight"] else 0,
                    has_truth=r["has_truth"] == "true",
                    our_side=r["our_side"],
                    our_pnl_cents=int(r["our_pnl_cents"]),
                    fee_cents=int(r["fee_cents"]),
                    lead_hours=0.0,
                )
            )
    return out


def report(label: str, rows: list[Row]) -> tuple[int, int, int]:
    """Print summary for a slice; return (n, won, pnl)."""
    n = len(rows)
    if n == 0:
        print(f"  {label:<40} (no trades)")
        return 0, 0, 0
    pnl = sum(r.our_pnl_cents for r in rows)
    won = sum(1 for r in rows if r.our_pnl_cents > 0)
    avg = pnl / n
    print(f"  {label:<40} n={n:>6,}  win={100*won/n:>5.1f}%  PnL={pnl:>+9,}¢  ${pnl/100:>+9.2f}  avg={avg:>+6.2f}¢")
    return n, won, pnl


def by_dim(rows: list[Row], dim_fn, name: str, buckets: list):
    print(f"\n## by {name}")
    for b in buckets:
        label, predicate = b
        slice_ = [r for r in rows if predicate(r)]
        report(label, slice_)


def slice_metric(rows, predicate):
    """Returns (n, pnl) for any slice."""
    s = [r for r in rows if predicate(r)]
    if not s:
        return 0, 0
    return len(s), sum(r.our_pnl_cents for r in s)


def main():
    rows = load()
    print(f"loaded {len(rows):,} entered trades\n")
    print(f"## overall")
    report("ALL", rows)

    by_dim(rows, None, "h_remain bucket", [
        ("h ≤ 2  (settlement window)",  lambda r: r.bayes_applied and r.bayes_h_remain <= 2),
        ("h 3-4",                       lambda r: r.bayes_applied and 3 <= r.bayes_h_remain <= 4),
        ("h 5-6",                       lambda r: r.bayes_applied and 5 <= r.bayes_h_remain <= 6),
        ("h 7-8",                       lambda r: r.bayes_applied and 7 <= r.bayes_h_remain <= 8),
        ("h 9-12",                      lambda r: r.bayes_applied and 9 <= r.bayes_h_remain <= 12),
        ("h 13-18",                     lambda r: r.bayes_applied and 13 <= r.bayes_h_remain <= 18),
        ("h 19-23 (morning)",           lambda r: r.bayes_applied and 19 <= r.bayes_h_remain),
        ("bayes not applied",           lambda r: not r.bayes_applied),
    ])

    by_dim(rows, None, "obs_weight quintile", [
        ("ObsW ≥ 0.70 (obs-dominated)", lambda r: r.bayes_applied and r.bayes_obs_weight >= 0.70),
        ("ObsW 0.50-0.70",              lambda r: r.bayes_applied and 0.50 <= r.bayes_obs_weight < 0.70),
        ("ObsW 0.30-0.50",              lambda r: r.bayes_applied and 0.30 <= r.bayes_obs_weight < 0.50),
        ("ObsW 0.15-0.30",              lambda r: r.bayes_applied and 0.15 <= r.bayes_obs_weight < 0.30),
        ("ObsW < 0.15 (prior-dominated)", lambda r: r.bayes_applied and r.bayes_obs_weight < 0.15),
    ])

    by_dim(rows, None, "edge_cents threshold", [
        ("edge 5-7¢",                   lambda r: 5 <= r.edge_cents <= 7),
        ("edge 8-12¢",                  lambda r: 8 <= r.edge_cents <= 12),
        ("edge 13-20¢",                 lambda r: 13 <= r.edge_cents <= 20),
        ("edge 21-35¢",                 lambda r: 21 <= r.edge_cents <= 35),
        ("edge 36-60¢",                 lambda r: 36 <= r.edge_cents <= 60),
        ("edge > 60¢ (huge)",           lambda r: r.edge_cents > 60),
    ])

    by_dim(rows, None, "trade_price_cents band (market view)", [
        ("price 1-9¢ (deep no)",        lambda r: 1 <= r.trade_price_cents <= 9),
        ("price 10-24¢",                lambda r: 10 <= r.trade_price_cents <= 24),
        ("price 25-39¢",                lambda r: 25 <= r.trade_price_cents <= 39),
        ("price 40-60¢ (midrange)",     lambda r: 40 <= r.trade_price_cents <= 60),
        ("price 61-75¢",                lambda r: 61 <= r.trade_price_cents <= 75),
        ("price 76-90¢",                lambda r: 76 <= r.trade_price_cents <= 90),
        ("price 91-99¢ (deep yes)",     lambda r: 91 <= r.trade_price_cents <= 99),
    ])

    by_dim(rows, None, "station (series prefix)", [
        ("KXHIGHNY",   lambda r: r.series == "KXHIGHNY"),
        ("KXLOWTNYC",  lambda r: r.series == "KXLOWTNYC"),
        ("KXHIGHMIA",  lambda r: r.series == "KXHIGHMIA"),
        ("KXLOWTMIA",  lambda r: r.series == "KXLOWTMIA"),
        ("KXHIGHLAX",  lambda r: r.series == "KXHIGHLAX"),
        ("KXLOWTLAX",  lambda r: r.series == "KXLOWTLAX"),
        ("KXHIGHCHI",  lambda r: r.series == "KXHIGHCHI"),
        ("KXLOWTCHI",  lambda r: r.series == "KXLOWTCHI"),
    ])

    by_dim(rows, None, "bracket_kind", [
        ("B (between)", lambda r: r.bracket_kind == "B"),
        ("T (threshold)", lambda r: r.bracket_kind == "T"),
    ])

    by_dim(rows, None, "side", [
        ("our_side = yes", lambda r: r.our_side == "yes"),
        ("our_side = no",  lambda r: r.our_side == "no"),
    ])

    # Best 2-way combos: filter on h_remain × edge_cents grid
    print("\n## 2-way: h_remain × edge_cents (PnL ¢)")
    h_buckets = [(1, 4), (5, 8), (9, 12), (13, 18), (19, 24)]
    e_buckets = [(5, 9), (10, 19), (20, 39), (40, 100)]
    print(f"  {'h_remain':<10}", end="")
    for el, eh in e_buckets:
        print(f"  {'edge':<5}{el:>2}-{eh:>2}  ", end="")
    print()
    for hl, hh in h_buckets:
        print(f"  h {hl:>2}-{hh:>2}   ", end="")
        for el, eh in e_buckets:
            n, pnl = slice_metric(rows, lambda r, hl=hl, hh=hh, el=el, eh=eh:
                                  r.bayes_applied and hl <= r.bayes_h_remain <= hh and el <= r.edge_cents <= eh)
            tag = f"{pnl:>+8,}" if n > 50 else "(<50)"
            print(f"  {tag:>11}  ", end="")
        print()

    # Find the single best subset by PnL among 3-way h × edge × price filters
    print("\n## 3-way subset search (best PnL with n ≥ 500)")
    best = []
    for hl, hh in h_buckets:
        for el, eh in e_buckets:
            for pl, ph in [(1, 24), (25, 49), (50, 74), (75, 99)]:
                n, pnl = slice_metric(rows, lambda r, hl=hl, hh=hh, el=el, eh=eh, pl=pl, ph=ph:
                                      r.bayes_applied
                                      and hl <= r.bayes_h_remain <= hh
                                      and el <= r.edge_cents <= eh
                                      and pl <= r.trade_price_cents <= ph)
                if n >= 500:
                    best.append((pnl, n, hl, hh, el, eh, pl, ph))
    best.sort(reverse=True)
    print(f"  {'pnl ¢':>8} {'n':>6}  h_range  edge_range  price_range")
    for pnl, n, hl, hh, el, eh, pl, ph in best[:10]:
        print(f"  {pnl:>+8,} {n:>6}  h {hl:>2}-{hh:<2}    edge {el:>2}-{eh:<2}   price {pl:>2}-{ph:<2}")
    print("\n  bottom 5 (worst):")
    for pnl, n, hl, hh, el, eh, pl, ph in best[-5:]:
        print(f"  {pnl:>+8,} {n:>6}  h {hl:>2}-{hh:<2}    edge {el:>2}-{eh:<2}   price {pl:>2}-{ph:<2}")


if __name__ == "__main__":
    main()
