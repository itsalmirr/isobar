#!/usr/bin/env python3
"""Leave-one-day-out cross-validation for candidate filters.

Filters under test (from test_filters.py):
  H1.   not KXHIGHNY                                        (simplest exclusion)
  H2.   KXLOWT* only                                        (low-temp brackets)
  H3.   price ≤ 9¢ or ≥ 91¢                                 (extreme bands)
  H4.   H1 ∩ (H2 ∪ H3) — combined hypothesis-driven filter
  H10.  series in {KXLOWTMIA, KXLOWTLAX, KXLOWTCHI, KXHIGHLAX} OR extreme

For each filter F:
  for each held-out date D:
    compute PnL(F, train_dates = all - D)  — diagnostic only
    compute PnL(F, test_date = D)          — what counts

The filter passes if PnL on each held-out day is positive (or at
least: average daily PnL is positive and the variance is low).
"""

import csv
from collections import defaultdict

PATH = "reports/backtest-realized-bayes/edges-2026-04-29_2026-05-04.csv"


def load():
    rows = []
    with open(PATH) as f:
        for r in csv.DictReader(f):
            if not r["our_side"]:
                continue
            rows.append(r)
    return rows


def is_high_ny(r): return r["series"] == "KXHIGHNY"
def is_low(r):     return r["series"].startswith("KXLOWT")
def to_int(s):
    try: return int(s)
    except: return 0
def price(r): return to_int(r["trade_price_cents"])
def edge(r):  return to_int(r["edge_cents"])
def is_extreme(r): p = price(r); return p <= 9 or p >= 91


FILTERS = {
    "H1: not KXHIGHNY":
        lambda r: not is_high_ny(r),
    "H2: KXLOWT* only":
        lambda r: is_low(r),
    "H3: extreme price ≤9 or ≥91":
        lambda r: is_extreme(r),
    "H4: H1 ∩ (H2 ∪ H3)":
        lambda r: not is_high_ny(r) and (is_low(r) or is_extreme(r)),
    "H10: select-series OR extreme":
        lambda r: r["series"] in {"KXLOWTMIA", "KXLOWTLAX", "KXLOWTCHI", "KXHIGHLAX"} or is_extreme(r),
    "H1+edge≤20":
        lambda r: not is_high_ny(r) and edge(r) <= 20,
}


def evaluate(rows, pred):
    n = pnl = won = 0
    for r in rows:
        if not pred(r):
            continue
        n += 1
        p = to_int(r["our_pnl_cents"])
        pnl += p
        if p > 0:
            won += 1
    return n, won, pnl


def main():
    rows = load()
    days = sorted(set(r["valid_date"] for r in rows))
    print(f"days under CV: {days}\n")

    # Group rows by valid_date for fast leave-one-out
    by_day = defaultdict(list)
    for r in rows:
        by_day[r["valid_date"]].append(r)

    for name, pred in FILTERS.items():
        # Per-day on-test PnL
        print(f"\n## {name}")
        print(f"  {'held-out day':<14} {'n':>7} {'win':>6} {'PnL ¢':>10} {'PnL $':>10}")
        per_day = []
        for d in days:
            test_rows = by_day[d]
            n, won, pnl = evaluate(test_rows, pred)
            wr = f"{100*won/n:>5.1f}%" if n else "  —  "
            per_day.append((d, n, won, pnl))
            print(f"  {d:<14} {n:>7,} {wr:>6} {pnl:>+10,} {pnl/100:>+10.2f}")

        total_n = sum(p[1] for p in per_day)
        total_pnl = sum(p[3] for p in per_day)
        total_won = sum(p[2] for p in per_day)
        pos_days = sum(1 for p in per_day if p[3] > 0)
        neg_days = sum(1 for p in per_day if p[3] < 0)
        wr = f"{100*total_won/total_n:>5.1f}%" if total_n else "  —  "
        print(f"  {'TOTAL':<14} {total_n:>7,} {wr:>6} {total_pnl:>+10,} {total_pnl/100:>+10.2f}")
        print(f"  positive days: {pos_days}/{len(per_day)}  negative days: {neg_days}/{len(per_day)}")
        per_day_pnls = [p[3] for p in per_day]
        if per_day_pnls and total_n > 0:
            mean = sum(per_day_pnls) / len(per_day_pnls)
            var = sum((x - mean) ** 2 for x in per_day_pnls) / len(per_day_pnls)
            sd = var ** 0.5
            sharpe_per_day = mean / sd if sd > 0 else float("inf")
            print(f"  daily PnL mean={mean:+.0f}¢ sd={sd:.0f}¢ Sharpe(per-day)={sharpe_per_day:.2f}")


if __name__ == "__main__":
    main()
